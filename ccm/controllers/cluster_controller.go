/*
Copyright 2025 Kube-DC Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// ClusterReconciler reconciles CAPI Cluster objects
// For each cluster, it manages Node IP addresses by syncing from Machine resources
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Track tenant cluster clients
	mu              sync.RWMutex
	tenantClients   map[string]kubernetes.Interface
	nodeControllers map[string]context.CancelFunc
}

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile handles Cluster resources and manages node IP synchronization
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get the Cluster
	cluster := &clusterv1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			// Cluster deleted, stop node controller if running
			r.stopNodeController(req.NamespacedName.String())
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch Cluster")
		return ctrl.Result{}, err
	}

	// Skip if cluster is being deleted
	if !cluster.DeletionTimestamp.IsZero() {
		r.stopNodeController(req.NamespacedName.String())
		return ctrl.Result{}, nil
	}

	// Skip if control plane not ready
	if !cluster.Status.ControlPlaneReady {
		log.V(2).Info("Control plane not ready yet", "cluster", cluster.Name)
		return ctrl.Result{}, nil
	}

	// Get kubeconfig secret for tenant cluster
	kubeconfigSecret := &corev1.Secret{}
	secretName := fmt.Sprintf("%s-kubeconfig", cluster.Name)
	if err := r.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: cluster.Namespace,
	}, kubeconfigSecret); err != nil {
		log.Error(err, "unable to get kubeconfig secret", "secret", secretName)
		return ctrl.Result{}, err
	}

	// Get kubeconfig data
	kubeconfigData, ok := kubeconfigSecret.Data["value"]
	if !ok {
		return ctrl.Result{}, fmt.Errorf("kubeconfig secret missing 'value' key")
	}

	// Create tenant cluster client
	tenantClient, err := r.getTenantClient(req.NamespacedName.String(), kubeconfigData)
	if err != nil {
		log.Error(err, "failed to create tenant cluster client")
		return ctrl.Result{}, err
	}

	// Start node controller for this cluster if not already running
	if err := r.ensureNodeController(ctx, cluster, tenantClient); err != nil {
		log.Error(err, "failed to ensure node controller")
		return ctrl.Result{}, err
	}

	log.V(2).Info("Cluster reconciled", "cluster", cluster.Name, "namespace", cluster.Namespace)
	return ctrl.Result{}, nil
}

// getTenantClient gets or creates a client for the tenant cluster
func (r *ClusterReconciler) getTenantClient(clusterKey string, kubeconfigData []byte) (kubernetes.Interface, error) {
	r.mu.RLock()
	if client, ok := r.tenantClients[clusterKey]; ok {
		r.mu.RUnlock()
		return client, nil
	}
	r.mu.RUnlock()

	// Create new client
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	tenantClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create tenant client: %w", err)
	}

	// Cache client
	r.mu.Lock()
	if r.tenantClients == nil {
		r.tenantClients = make(map[string]kubernetes.Interface)
	}
	r.tenantClients[clusterKey] = tenantClient
	r.mu.Unlock()

	return tenantClient, nil
}

// ensureNodeController starts a node controller for the tenant cluster if not running
func (r *ClusterReconciler) ensureNodeController(ctx context.Context, cluster *clusterv1.Cluster, tenantClient kubernetes.Interface) error {
	clusterKey := fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name)

	r.mu.RLock()
	_, exists := r.nodeControllers[clusterKey]
	r.mu.RUnlock()

	if exists {
		return nil // Already running
	}

	// Start node sync goroutine
	nodeCtx, cancel := context.WithCancel(context.Background())

	r.mu.Lock()
	if r.nodeControllers == nil {
		r.nodeControllers = make(map[string]context.CancelFunc)
	}
	r.nodeControllers[clusterKey] = cancel
	r.mu.Unlock()

	go r.syncNodesForCluster(nodeCtx, cluster, tenantClient)

	klog.Infof("Started node controller for cluster %s/%s", cluster.Namespace, cluster.Name)
	return nil
}

// stopNodeController stops the node controller for a cluster
func (r *ClusterReconciler) stopNodeController(clusterKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cancel, ok := r.nodeControllers[clusterKey]; ok {
		cancel()
		delete(r.nodeControllers, clusterKey)
		delete(r.tenantClients, clusterKey)
		klog.Infof("Stopped node controller for cluster %s", clusterKey)
	}
}

// syncNodesForCluster continuously syncs node IPs for a tenant cluster
func (r *ClusterReconciler) syncNodesForCluster(ctx context.Context, cluster *clusterv1.Cluster, tenantClient kubernetes.Interface) {
	// This will be called periodically to sync nodes
	// For now, simple implementation - can be enhanced with watches
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.syncNodes(ctx, cluster, tenantClient); err != nil {
				klog.Errorf("Failed to sync nodes for cluster %s/%s: %v", cluster.Namespace, cluster.Name, err)
			}
		}
	}
}

// syncNodes syncs node IPs from Machines to Nodes
func (r *ClusterReconciler) syncNodes(ctx context.Context, cluster *clusterv1.Cluster, tenantClient kubernetes.Interface) error {
	// List all machines for this cluster
	machineList := &clusterv1.MachineList{}
	if err := r.List(ctx, machineList, client.InNamespace(cluster.Namespace), client.MatchingLabels{
		clusterv1.ClusterNameLabel: cluster.Name,
	}); err != nil {
		return fmt.Errorf("failed to list machines: %w", err)
	}

	// Get all nodes from tenant cluster
	nodes, err := tenantClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	// Update nodes with IPs from machines
	for i := range nodes.Items {
		node := &nodes.Items[i]

		// Skip if node already has internal IP
		if hasInternalIP(node) {
			continue
		}

		// Find matching machine by providerID
		machine := findMachineByProviderID(machineList, node.Spec.ProviderID)
		if machine == nil {
			continue
		}

		// Update node addresses
		if err := r.updateNodeAddresses(ctx, tenantClient, node, machine); err != nil {
			klog.Errorf("Failed to update node %s: %v", node.Name, err)
		}
	}

	return nil
}

// updateNodeAddresses updates a node's addresses from machine
func (r *ClusterReconciler) updateNodeAddresses(ctx context.Context, tenantClient kubernetes.Interface, node *corev1.Node, machine *clusterv1.Machine) error {
	if len(machine.Status.Addresses) == 0 {
		return nil
	}

	updated := false
	for _, machineAddr := range machine.Status.Addresses {
		if machineAddr.Type == clusterv1.MachineInternalIP {
			// Check if address already exists
			found := false
			for _, nodeAddr := range node.Status.Addresses {
				if nodeAddr.Address == machineAddr.Address {
					found = true
					break
				}
			}

			if !found {
				node.Status.Addresses = append(node.Status.Addresses, corev1.NodeAddress{
					Type:    corev1.NodeInternalIP,
					Address: machineAddr.Address,
				})
				updated = true
			}
		}
	}

	if updated {
		if _, err := tenantClient.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update node status: %w", err)
		}
		klog.Infof("Updated node %s with address from machine %s", node.Name, machine.Name)
	}

	return nil
}

// hasInternalIP checks if a node already has an internal IP address
func hasInternalIP(node *corev1.Node) bool {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP && addr.Address != "" {
			return true
		}
	}
	return false
}

// findMachineByProviderID finds a machine with matching providerID
func findMachineByProviderID(machineList *clusterv1.MachineList, providerID string) *clusterv1.Machine {
	if providerID == "" {
		return nil
	}

	for i := range machineList.Items {
		machine := &machineList.Items[i]
		if machine.Spec.ProviderID != nil && *machine.Spec.ProviderID == providerID {
			return machine
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1.Cluster{}).
		Complete(r)
}
