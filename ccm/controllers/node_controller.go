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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// NodeReconciler reconciles nodes in the tenant cluster
// It connects to the tenant cluster using a kubeconfig and manages node initialization
type NodeReconciler struct {
	// TenantKubeconfig is the path to the kubeconfig file for the tenant cluster
	TenantKubeconfig string
	// ClusterName is the name of the cluster being managed
	ClusterName string

	tenantClient kubernetes.Interface
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

// reconcileNode handles a single node - removes initialization taint
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

	if !hasTaint {
		// Node already initialized
		return nil
	}

	klog.Infof("Initializing node %s", node.Name)

	// Get node addresses from providerID (CloudSigma VM UUID)
	// ProviderID format: cloudsigma://<uuid>
	if node.Spec.ProviderID != "" {
		vmUUID := strings.TrimPrefix(node.Spec.ProviderID, "cloudsigma://")
		klog.V(2).Infof("Node %s has providerID: %s (VM UUID: %s)", node.Name, node.Spec.ProviderID, vmUUID)
		// TODO: Query CloudSigma API for VM details if needed
	}

	// Remove the initialization taint
	nodeCopy := node.DeepCopy()
	nodeCopy.Spec.Taints = newTaints

	_, err := r.tenantClient.CoreV1().Nodes().Update(ctx, nodeCopy, metav1.UpdateOptions{})
	if err != nil {
		if errors.IsConflict(err) {
			// Retry on conflict
			return nil
		}
		return fmt.Errorf("failed to update node: %w", err)
	}

	klog.Infof("Removed initialization taint from node %s", node.Name)
	return nil
}
