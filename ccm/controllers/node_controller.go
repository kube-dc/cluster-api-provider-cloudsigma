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
	"strings"
	"time"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/auth"
)

// NodeReconciler reconciles nodes in the tenant cluster
// It connects to the tenant cluster using a kubeconfig and manages node initialization
type NodeReconciler struct {
	// TenantKubeconfig is the path to the kubeconfig file for the tenant cluster
	TenantKubeconfig string
	// ClusterName is the name of the cluster being managed
	ClusterName string
	// CloudSigma legacy credentials (fallback)
	CloudSigmaUsername string
	CloudSigmaPassword string
	CloudSigmaRegion   string
	// Impersonation config (preferred)
	ImpersonationClient  *auth.ImpersonationClient
	ImpersonationEnabled bool
	UserEmail            string

	tenantClient     kubernetes.Interface
	cloudsigmaClient *cloudsigma.Client
}

// Start initializes the tenant client and starts the node sync loop
func (r *NodeReconciler) Start(ctx context.Context) error {
	// Load tenant cluster kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", r.TenantKubeconfig)
	if err != nil {
		return fmt.Errorf("failed to load tenant kubeconfig: %w", err)
	}

	// Create tenant client
	r.tenantClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create tenant client: %w", err)
	}

	klog.Infof("Connected to tenant cluster: %s", r.ClusterName)

	// Create CloudSigma client
	region := r.CloudSigmaRegion
	if region == "" {
		region = "zrh"
	}

	if r.ImpersonationEnabled && r.ImpersonationClient != nil && r.UserEmail != "" {
		// Use impersonation (preferred)
		klog.Infof("Using impersonation for user: %s in region: %s", r.UserEmail, region)
		token, err := r.ImpersonationClient.GetImpersonatedToken(ctx, r.UserEmail, region)
		if err != nil {
			return fmt.Errorf("failed to get impersonated token: %w", err)
		}
		cred := cloudsigma.NewTokenCredentialsProvider(token)
		r.cloudsigmaClient = cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
		klog.Infof("CloudSigma client initialized with impersonation for region: %s", region)
	} else if r.CloudSigmaUsername != "" && r.CloudSigmaPassword != "" {
		// Fallback to legacy credentials
		klog.Info("Using legacy username/password credentials")
		cred := cloudsigma.NewUsernamePasswordCredentialsProvider(r.CloudSigmaUsername, r.CloudSigmaPassword)
		r.cloudsigmaClient = cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
		klog.Infof("CloudSigma client initialized for region: %s", region)
	} else {
		klog.Warning("No CloudSigma credentials provided, node addresses will not be updated")
	}

	// Start node sync loop
	go r.syncLoop(ctx)

	return nil
}

// syncLoop periodically syncs nodes
func (r *NodeReconciler) syncLoop(ctx context.Context) {
	// Initial sync
	if err := r.syncNodes(ctx); err != nil {
		klog.Errorf("Initial node sync failed: %v", err)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Info("Node sync loop stopped")
			return
		case <-ticker.C:
			if err := r.syncNodes(ctx); err != nil {
				klog.Errorf("Node sync failed: %v", err)
			}
		}
	}
}

// syncNodes syncs all nodes - removes cloud-provider taint and updates addresses
func (r *NodeReconciler) syncNodes(ctx context.Context) error {
	nodes, err := r.tenantClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		if err := r.reconcileNode(ctx, node); err != nil {
			klog.Errorf("Failed to reconcile node %s: %v", node.Name, err)
		}
	}

	return nil
}

// reconcileNode handles a single node - removes initialization taint and sets addresses
func (r *NodeReconciler) reconcileNode(ctx context.Context, node *corev1.Node) error {
	// Check if node has the cloud-provider initialization taint
	hasTaint := false
	var newTaints []corev1.Taint
	for _, taint := range node.Spec.Taints {
		if taint.Key == "node.cloudprovider.kubernetes.io/uninitialized" {
			hasTaint = true
			continue
		}
		newTaints = append(newTaints, taint)
	}

	// Check if node needs address update
	needsAddressUpdate := !r.hasIPAddress(node)

	if !hasTaint && !needsAddressUpdate {
		// Node already initialized and has addresses
		return nil
	}

	klog.Infof("Reconciling node %s (hasTaint=%v, needsAddressUpdate=%v)", node.Name, hasTaint, needsAddressUpdate)

	nodeCopy := node.DeepCopy()

	// Get node addresses from providerID (CloudSigma VM UUID)
	if node.Spec.ProviderID != "" && r.cloudsigmaClient != nil && needsAddressUpdate {
		vmUUID := strings.TrimPrefix(node.Spec.ProviderID, "cloudsigma://")
		klog.V(2).Infof("Fetching VM details for node %s (UUID: %s)", node.Name, vmUUID)

		addresses, err := r.getVMAddresses(ctx, vmUUID)
		if err != nil {
			klog.Errorf("Failed to get VM addresses for %s: %v", vmUUID, err)
		} else if len(addresses) > 0 {
			nodeCopy.Status.Addresses = addresses
			klog.Infof("Setting addresses for node %s: %v", node.Name, addresses)
		}
	}

	// Remove the initialization taint if present
	if hasTaint {
		nodeCopy.Spec.Taints = newTaints
	}

	// Update node spec (taints)
	if hasTaint {
		_, err := r.tenantClient.CoreV1().Nodes().Update(ctx, nodeCopy, metav1.UpdateOptions{})
		if err != nil {
			if errors.IsConflict(err) {
				return nil
			}
			return fmt.Errorf("failed to update node spec: %w", err)
		}
		klog.Infof("Removed initialization taint from node %s", node.Name)
	}

	// Update node status (addresses)
	if needsAddressUpdate && len(nodeCopy.Status.Addresses) > 0 {
		_, err := r.tenantClient.CoreV1().Nodes().UpdateStatus(ctx, nodeCopy, metav1.UpdateOptions{})
		if err != nil {
			if errors.IsConflict(err) {
				return nil
			}
			return fmt.Errorf("failed to update node status: %w", err)
		}
		klog.Infof("Updated addresses for node %s", node.Name)
	}

	return nil
}

// hasIPAddress checks if the node has an InternalIP or ExternalIP address
func (r *NodeReconciler) hasIPAddress(node *corev1.Node) bool {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP || addr.Type == corev1.NodeExternalIP {
			return true
		}
	}
	return false
}

// getVMAddresses queries CloudSigma API to get VM IP addresses
func (r *NodeReconciler) getVMAddresses(ctx context.Context, vmUUID string) ([]corev1.NodeAddress, error) {
	server, _, err := r.cloudsigmaClient.Servers.Get(ctx, vmUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get server: %w", err)
	}

	var addresses []corev1.NodeAddress

	// Add hostname
	if server.Name != "" {
		addresses = append(addresses, corev1.NodeAddress{
			Type:    corev1.NodeHostName,
			Address: server.Name,
		})
	}

	// Get IP addresses by querying the IPs endpoint for IPs attached to this server
	ips, _, err := r.cloudsigmaClient.IPs.List(ctx)
	if err != nil {
		klog.Errorf("Failed to list IPs: %v", err)
	} else {
		for _, ip := range ips {
			// Check if this IP is attached to our server
			if ip.Server != nil && ip.Server.UUID == vmUUID {
				ipAddr := ip.UUID
				if ipAddr != "" {
					// Determine if internal or external based on IP range
					addrType := corev1.NodeExternalIP
					if strings.HasPrefix(ipAddr, "10.") || strings.HasPrefix(ipAddr, "192.168.") || strings.HasPrefix(ipAddr, "172.") {
						addrType = corev1.NodeInternalIP
					}
					addresses = append(addresses, corev1.NodeAddress{
						Type:    addrType,
						Address: ipAddr,
					})
					klog.V(2).Infof("Found IP %s (type: %s) for VM %s", ipAddr, addrType, vmUUID)
				}
			}
		}
	}

	return addresses, nil
}
