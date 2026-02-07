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
	"sync"
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
	// CloudSigma region
	CloudSigmaRegion string
	// Impersonation config (default mode)
	ImpersonationClient *auth.ImpersonationClient
	UserEmail           string
	// Legacy credentials (must be explicitly enabled)
	LegacyCredentialsEnabled bool
	CloudSigmaUsername       string
	CloudSigmaPassword       string

	tenantClient       kubernetes.Interface
	cloudsigmaClient   *cloudsigma.Client
	clientMutex        sync.RWMutex
	staleNodeFailures  map[string]int // tracks consecutive 403 failures per node
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

	// Initialize CloudSigma client (will be refreshed on each sync for impersonation)
	if err := r.refreshCloudSigmaClient(ctx); err != nil {
		klog.Warningf("Initial CloudSigma client creation failed: %v", err)
	}

	// Start node sync loop
	go r.syncLoop(ctx)

	return nil
}

// refreshCloudSigmaClient creates or refreshes the CloudSigma client
// For impersonation, this gets a fresh token (cached by ImpersonationClient)
func (r *NodeReconciler) refreshCloudSigmaClient(ctx context.Context) error {
	region := r.CloudSigmaRegion
	if region == "" {
		region = "zrh"
	}

	r.clientMutex.Lock()
	defer r.clientMutex.Unlock()

	// Use impersonation (default) if configured and userEmail is set
	if r.ImpersonationClient != nil && r.UserEmail != "" {
		klog.Infof("Refreshing CloudSigma client with impersonation for user: %s in region: %s", r.UserEmail, region)
		token, err := r.ImpersonationClient.GetImpersonatedToken(ctx, r.UserEmail, region)
		if err != nil {
			return fmt.Errorf("failed to get impersonated token: %w", err)
		}
		cred := cloudsigma.NewTokenCredentialsProvider(token)
		r.cloudsigmaClient = cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
		klog.V(2).Infof("CloudSigma client refreshed with impersonation for region: %s", region)
		return nil
	}

	// Fallback to legacy credentials only if explicitly enabled
	if r.LegacyCredentialsEnabled && r.CloudSigmaUsername != "" && r.CloudSigmaPassword != "" {
		if r.cloudsigmaClient == nil {
			klog.Info("Using legacy username/password credentials (explicitly enabled)")
			cred := cloudsigma.NewUsernamePasswordCredentialsProvider(r.CloudSigmaUsername, r.CloudSigmaPassword)
			r.cloudsigmaClient = cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
			klog.Infof("CloudSigma client initialized for region: %s", region)
		}
		return nil
	}

	// No valid auth method
	if r.cloudsigmaClient == nil {
		if r.ImpersonationClient != nil && r.UserEmail == "" {
			return fmt.Errorf("impersonation configured but userEmail not set")
		}
		klog.Warning("No CloudSigma authentication available, node addresses will not be updated")
	}

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
	// Refresh CloudSigma client (gets fresh token if using impersonation)
	if err := r.refreshCloudSigmaClient(ctx); err != nil {
		klog.Errorf("Failed to refresh CloudSigma client: %v", err)
	}

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
		if taint.Key == "node.cloudprovider.kubernetes.io/uninitialized" ||
			taint.Key == "node.cluster.x-k8s.io/uninitialized" {
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

			// Detect permission denied (403) - VM owned by a different user = stale node
			errStr := err.Error()
			if strings.Contains(errStr, "403") || strings.Contains(errStr, "permission") {
				return r.handleStaleNode(ctx, node, vmUUID, err)
			}
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

// handleStaleNode deletes a node from the tenant cluster when its VM is inaccessible (403).
// This happens when old VMs from a previous cluster (owned by a different user) re-register
// with the new cluster's API server via stale etcd data.
func (r *NodeReconciler) handleStaleNode(ctx context.Context, node *corev1.Node, vmUUID string, apiErr error) error {
	// Track consecutive failures per node to avoid deleting on transient errors
	r.clientMutex.Lock()
	if r.staleNodeFailures == nil {
		r.staleNodeFailures = make(map[string]int)
	}
	r.staleNodeFailures[node.Name]++
	failCount := r.staleNodeFailures[node.Name]
	r.clientMutex.Unlock()

	// Require 3 consecutive failures before deleting (covers ~90s with 30s sync interval)
	if failCount < 3 {
		klog.Warningf("Node %s: VM %s returned permission denied (%d/3 before deletion): %v",
			node.Name, vmUUID, failCount, apiErr)
		return nil
	}

	klog.Warningf("Deleting stale node %s: VM %s is not accessible by current user (owned by different account) - "+
		"this node likely belongs to a previously deleted cluster", node.Name, vmUUID)

	if err := r.tenantClient.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{}); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete stale node %s: %w", node.Name, err)
		}
	}

	// Clean up tracking
	r.clientMutex.Lock()
	delete(r.staleNodeFailures, node.Name)
	r.clientMutex.Unlock()

	klog.Infof("Deleted stale node %s (VM %s owned by different user)", node.Name, vmUUID)
	return nil
}

// GetTenantClient returns the tenant cluster Kubernetes client
func (r *NodeReconciler) GetTenantClient() kubernetes.Interface {
	return r.tenantClient
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
	r.clientMutex.RLock()
	client := r.cloudsigmaClient
	r.clientMutex.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("CloudSigma client not initialized")
	}

	server, _, err := client.Servers.Get(ctx, vmUUID)
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

	// Get IP addresses by listing IPs attached to this server
	// Use the first IP found as the node's primary IP
	ips, _, err := client.IPs.List(ctx)
	if err != nil {
		klog.Errorf("Failed to list IPs: %v", err)
	} else {
		klog.Infof("VM %s: Checking %d IPs for attachment", vmUUID, len(ips))
		for _, ip := range ips {
			// Log all IPs and their server attachments for debugging
			serverUUID := ""
			if ip.Server != nil {
				serverUUID = ip.Server.UUID
			}
			if serverUUID != "" {
				klog.V(2).Infof("IP %s attached to server %s (looking for %s)", ip.UUID, serverUUID, vmUUID)
			}
			
			// Check if this IP is attached to our server
			if ip.Server != nil && ip.Server.UUID == vmUUID {
				ipAddr := ip.UUID
				if ipAddr == "" {
					continue
				}
				
				// Use first IP attached to server as the node IP
				addrType := corev1.NodeExternalIP
				if strings.HasPrefix(ipAddr, "10.") || strings.HasPrefix(ipAddr, "192.168.") || strings.HasPrefix(ipAddr, "172.") {
					addrType = corev1.NodeInternalIP
				}
				addresses = append(addresses, corev1.NodeAddress{
					Type:    addrType,
					Address: ipAddr,
				})
				klog.Infof("Found IP %s (type: %s) for VM %s", ipAddr, addrType, vmUUID)
				break // Only use first IP
			}
		}
		if len(addresses) == 1 { // Only hostname, no IP
			klog.Warningf("VM %s: No IPs found attached to this server", vmUUID)
		}
	}

	return addresses, nil
}
