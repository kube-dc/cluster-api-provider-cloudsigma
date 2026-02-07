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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/auth"
)

const (
	// AnnotationIPPoolType specifies which IP pool to use for LoadBalancer
	// Values: "static" (default), "dynamic"
	AnnotationIPPoolType = "cloudsigma.com/ip-pool"

	// IPPoolStatic uses static IPs (owned IPs with subscription)
	IPPoolStatic = "static"
	// IPPoolDynamic uses dynamic IPs (unassigned IPs without server attachment)
	IPPoolDynamic = "dynamic"
)

// LoadBalancerController manages LoadBalancer service IPs using CloudSigma's
// "manual" NIC mode. With manual mode, the cloud firewall allows traffic for
// ALL subscribed IPs, so no per-IP NIC attachment is needed. The controller
// only needs to configure IPs locally on nodes via privileged pods.
type LoadBalancerController struct {
	// TenantClient is the Kubernetes client for the tenant cluster
	TenantClient kubernetes.Interface

	// ImpersonationClient for CloudSigma API access
	ImpersonationClient *auth.ImpersonationClient

	// UserEmail for impersonation
	UserEmail string

	// Region for CloudSigma API
	Region string

	// ClusterName for tagging IPs in CloudSigma
	ClusterName string

	// Disabled allows disabling the controller (enabled by default)
	Disabled bool

	// mutex for thread safety
	mutex sync.RWMutex

	// staticIPs is the list of owned static IPs (with subscription)
	staticIPs []string

	// dynamicIPs is the list of available dynamic IPs (no server attached, no subscription)
	dynamicIPs []string

	// ipAssignments tracks which IP is assigned to which node
	// key: IP address, value: server UUID
	ipAssignments map[string]string

	// serviceIPs tracks which service has which IP
	// key: namespace/name, value: IP address
	serviceIPs map[string]string

	// manualModeNodes tracks which nodes have already been switched to manual NIC mode
	// key: server UUID
	manualModeNodes map[string]bool

	// done is closed after shutdown cleanup completes, so main() can wait
	done chan struct{}
}

// CloudSigmaIP represents an IP from the CloudSigma API
type CloudSigmaIP struct {
	UUID         string            `json:"uuid"`
	Server       *CloudSigmaServer `json:"server"`
	Subscription *struct {
		ID int `json:"id"`
	} `json:"subscription"`
}

// CloudSigmaServer represents a server reference
type CloudSigmaServer struct {
	UUID string `json:"uuid"`
}


// WaitForShutdown blocks until the controller's shutdown cleanup is complete.
// Must be called after Start() and after the context is cancelled.
func (c *LoadBalancerController) WaitForShutdown() {
	if c.done != nil {
		<-c.done
	}
}

// Start initializes and starts the LoadBalancer controller
func (c *LoadBalancerController) Start(ctx context.Context) error {
	if c.Disabled {
		klog.Info("LoadBalancer IP pool controller is disabled")
		return nil
	}

	c.ipAssignments = make(map[string]string)
	c.serviceIPs = make(map[string]string)
	c.manualModeNodes = make(map[string]bool)
	c.done = make(chan struct{})

	// Discover owned IPs from CloudSigma API and recover state
	if err := c.discoverOwnedIPs(ctx); err != nil {
		klog.Errorf("Failed to discover owned IPs: %v", err)
		// Continue anyway, will retry in sync loop
	}

	// Recover serviceIPs mapping from existing services
	if err := c.recoverServiceState(ctx); err != nil {
		klog.Errorf("Failed to recover service state: %v", err)
	}

	klog.Infof("Starting LoadBalancer controller with static IPs: %v, dynamic IPs: %v", c.staticIPs, c.dynamicIPs)

	// Initial sync
	if err := c.syncLoadBalancers(ctx); err != nil {
		klog.Errorf("Initial LoadBalancer sync failed: %v", err)
	}

	// Start sync loop
	go c.syncLoop(ctx)

	return nil
}

// discoverOwnedIPs queries CloudSigma API to find owned IPs (with subscription) and recover assignment state
func (c *LoadBalancerController) discoverOwnedIPs(ctx context.Context) error {
	token, err := c.ImpersonationClient.GetImpersonatedToken(ctx, c.UserEmail, c.Region)
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	url := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/ips/detail/", c.Region)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list IPs: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Objects []CloudSigmaIP `json:"objects"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse IPs: %w", err)
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.staticIPs = nil
	c.dynamicIPs = nil

	for _, ip := range result.Objects {
		// Static IPs: owned IPs with subscription
		if ip.Subscription != nil {
			c.staticIPs = append(c.staticIPs, ip.UUID)
			klog.V(2).Infof("Discovered static IP: %s (subscription: %d)", ip.UUID, ip.Subscription.ID)
		} else {
			// Dynamic IPs: IPs without subscription (available for temporary use)
			c.dynamicIPs = append(c.dynamicIPs, ip.UUID)
			klog.V(2).Infof("Discovered dynamic IP: %s", ip.UUID)
		}
	}

	klog.Infof("Discovered %d static IPs and %d dynamic IPs", len(c.staticIPs), len(c.dynamicIPs))
	return nil
}

// recoverServiceState recovers serviceIPs mapping from existing LoadBalancer services
func (c *LoadBalancerController) recoverServiceState(ctx context.Context) error {
	services, err := c.TenantClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, svc := range services.Items {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}

		svcKey := fmt.Sprintf("%s/%s", svc.Namespace, svc.Name)
		for _, ingress := range svc.Status.LoadBalancer.Ingress {
			if c.isPoolIPLocked(ingress.IP) {
				c.serviceIPs[svcKey] = ingress.IP
				klog.Infof("Recovered service IP mapping: %s -> %s", svcKey, ingress.IP)
			}
		}
	}

	klog.Infof("Recovered %d service IP mappings", len(c.serviceIPs))
	return nil
}

// isPoolIPLocked checks if an IP is in any pool (must hold mutex)
func (c *LoadBalancerController) isPoolIPLocked(ip string) bool {
	for _, poolIP := range c.staticIPs {
		if poolIP == ip {
			return true
		}
	}
	for _, poolIP := range c.dynamicIPs {
		if poolIP == ip {
			return true
		}
	}
	return false
}

// syncLoop periodically syncs LoadBalancer services
func (c *LoadBalancerController) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Refresh IP discovery every 5 minutes
	ipRefreshTicker := time.NewTicker(5 * time.Minute)
	defer ipRefreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Info("LoadBalancer sync loop stopping, cleaning up IP tags...")
			c.cleanupAllIPTags()
			klog.Info("LoadBalancer sync loop stopped")
			close(c.done)
			return
		case <-ipRefreshTicker.C:
			// Periodically refresh discovered IPs
			if err := c.discoverOwnedIPs(ctx); err != nil {
				klog.Errorf("Failed to refresh owned IPs: %v", err)
			}
		case <-ticker.C:
			if err := c.syncLoadBalancers(ctx); err != nil {
				klog.Errorf("LoadBalancer sync failed: %v", err)
			}
		}
	}
}

// syncLoadBalancers syncs all LoadBalancer services
func (c *LoadBalancerController) syncLoadBalancers(ctx context.Context) error {
	// Get all services
	services, err := c.TenantClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	// Get healthy nodes
	nodes, err := c.TenantClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	healthyNodes := c.getHealthyNodes(nodes.Items)
	if len(healthyNodes) == 0 {
		klog.Warning("No healthy nodes available for LoadBalancer IP assignment")
		return nil
	}

	// Build set of current LoadBalancer services
	currentServices := make(map[string]bool)
	for _, svc := range services.Items {
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			svcKey := fmt.Sprintf("%s/%s", svc.Namespace, svc.Name)
			currentServices[svcKey] = true
		}
	}

	// Cleanup deleted services - release IPs and untag them
	// Note: With manual NIC mode, no NIC detach is needed - the node's NIC stays in
	// manual mode and simply allows all subscribed IPs. We just remove the local config.
	c.mutex.Lock()
	for svcKey, ip := range c.serviceIPs {
		if !currentServices[svcKey] {
			klog.Infof("Service %s deleted, releasing IP %s", svcKey, ip)
			// Untag IP in CloudSigma
			if err := c.untagIPInCloudSigma(ctx, ip); err != nil {
				klog.Warningf("Failed to untag IP %s: %v", ip, err)
			}
			// Delete config pod (removes local IP + iptables rules)
			c.deleteIPConfigPod(ctx, ip)
			// Remove from assignments
			delete(c.serviceIPs, svcKey)
			delete(c.ipAssignments, ip)
		}
	}
	c.mutex.Unlock()

	// Process each LoadBalancer service
	for _, svc := range services.Items {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}

		if err := c.reconcileService(ctx, &svc, healthyNodes); err != nil {
			klog.Errorf("Failed to reconcile service %s/%s: %v", svc.Namespace, svc.Name, err)
		}
	}

	// Check for IP failover (if a node with assigned IP is unhealthy)
	if err := c.checkIPFailover(ctx, healthyNodes); err != nil {
		klog.Errorf("IP failover check failed: %v", err)
	}

	return nil
}

// reconcileService ensures a LoadBalancer service has an IP assigned
func (c *LoadBalancerController) reconcileService(ctx context.Context, svc *corev1.Service, healthyNodes []corev1.Node) error {
	svcKey := fmt.Sprintf("%s/%s", svc.Namespace, svc.Name)

	// Check if service already has an external IP from our pool
	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if c.isPoolIP(ingress.IP) {
			klog.V(2).Infof("Service %s already has pool IP %s", svcKey, ingress.IP)
			// Ensure IP is configured on the node (in case of CCM restart)
			c.mutex.RLock()
			serverUUID, hasAssignment := c.ipAssignments[ingress.IP]
			c.mutex.RUnlock()

			// If no assignment tracking, use first healthy node
			if !hasAssignment && len(healthyNodes) > 0 {
				serverUUID = c.getNodeUUID(&healthyNodes[0])
				if serverUUID != "" {
					c.mutex.Lock()
					c.ipAssignments[ingress.IP] = serverUUID
					c.serviceIPs[svcKey] = ingress.IP
					c.mutex.Unlock()
					hasAssignment = true
					klog.Infof("Recovered IP assignment: %s -> %s", ingress.IP, healthyNodes[0].Name)
				}
			}

			if hasAssignment && len(svc.Spec.Ports) > 0 {
				// Get endpoint IP (pod IP) for direct routing - ClusterIP routing may be broken
				endpointIP := c.getEndpointIP(ctx, svc)
				if endpointIP == "" {
					endpointIP = svc.Spec.ClusterIP // fallback to ClusterIP
				}
				c.ensureIPConfigured(ctx, ingress.IP, serverUUID, endpointIP, svc.Spec.Ports[0].Port)

				// Ensure IP is tagged (in case of CCM restart or missed tagging)
				if err := c.tagIPInCloudSigma(ctx, ingress.IP, svcKey); err != nil {
					klog.V(2).Infof("Failed to ensure tags for IP %s: %v", ingress.IP, err)
				}
			}
			return nil
		}
	}

	// Check if we already assigned an IP to this service
	c.mutex.RLock()
	existingIP, hasIP := c.serviceIPs[svcKey]
	c.mutex.RUnlock()

	if hasIP {
		// Update service status with the IP
		return c.updateServiceStatus(ctx, svc, existingIP)
	}

	// Allocate a new IP from the pool (static or dynamic based on annotation)
	ip, err := c.allocateIP(ctx, svc)
	if err != nil {
		return fmt.Errorf("failed to allocate IP: %w", err)
	}

	if ip == "" {
		poolType := c.getIPPoolType(svc)
		klog.Warningf("No available IPs in %s pool for service %s", poolType, svcKey)
		return nil
	}

	// Assign IP to a healthy node
	if len(healthyNodes) > 0 {
		nodeUUID := c.getNodeUUID(&healthyNodes[0])
		if nodeUUID != "" {
			// Ensure the node's NIC is in manual mode (one-time per node).
			// Manual mode opens the CloudSigma firewall for ALL subscribed IPs,
			// eliminating the need for per-IP NIC attachment.
			if err := c.ensureNodeManualMode(ctx, nodeUUID); err != nil {
				return fmt.Errorf("failed to switch node %s to manual NIC mode: %w", nodeUUID, err)
			}

			c.mutex.Lock()
			c.ipAssignments[ip] = nodeUUID
			c.serviceIPs[svcKey] = ip
			c.mutex.Unlock()

			// Tag IP in CloudSigma for tracking (non-blocking)
			if err := c.tagIPInCloudSigma(ctx, ip, svcKey); err != nil {
				klog.Warningf("Failed to tag IP %s in CloudSigma: %v", ip, err)
			}

			// Configure the IP on the node and set up iptables rules
			if len(svc.Spec.Ports) > 0 {
				port := svc.Spec.Ports[0].Port
				// Get endpoint IP (pod IP) for direct routing - ClusterIP routing may be broken
				endpointIP := c.getEndpointIP(ctx, svc)
				if endpointIP == "" {
					endpointIP = svc.Spec.ClusterIP // fallback to ClusterIP
				}
				if err := c.configureIPOnNode(ctx, ip, nodeUUID, endpointIP, port); err != nil {
					klog.Warningf("Failed to configure IP %s on node: %v", ip, err)
				}
			}

			klog.Infof("Assigned IP %s to service %s (node: %s)", ip, svcKey, healthyNodes[0].Name)
		}
	}

	// Update service status
	return c.updateServiceStatus(ctx, svc, ip)
}

// checkIPFailover checks if any IPs need to be moved due to node failure
func (c *LoadBalancerController) checkIPFailover(ctx context.Context, healthyNodes []corev1.Node) error {
	if len(healthyNodes) == 0 {
		return nil
	}

	healthyUUIDs := make(map[string]bool)
	for _, node := range healthyNodes {
		uuid := c.getNodeUUID(&node)
		if uuid != "" {
			healthyUUIDs[uuid] = true
		}
	}

	c.mutex.RLock()
	assignments := make(map[string]string)
	for ip, uuid := range c.ipAssignments {
		assignments[ip] = uuid
	}
	c.mutex.RUnlock()

	for ip, currentUUID := range assignments {
		if !healthyUUIDs[currentUUID] {
			// Current node is unhealthy, move IP to a healthy node
			klog.Warningf("Node %s with IP %s is unhealthy, initiating failover", currentUUID, ip)

			// Pick first healthy node
			newNode := &healthyNodes[0]
			newUUID := c.getNodeUUID(newNode)

			if newUUID == "" {
				continue
			}

			// Ensure new node is in manual mode (allows all subscribed IPs)
			if err := c.ensureNodeManualMode(ctx, newUUID); err != nil {
				klog.Errorf("Failed to switch node %s to manual mode: %v", newUUID, err)
				continue
			}

			// Force-delete old lb-ip pod with zero grace period to avoid race condition
			// where the pod is still terminating when we try to create the new one
			podName := fmt.Sprintf("lb-ip-%s", strings.ReplaceAll(ip, ".", "-"))
			gracePeriod := int64(0)
			if err := c.TenantClient.CoreV1().Pods("kube-system").Delete(ctx, podName, metav1.DeleteOptions{
				GracePeriodSeconds: &gracePeriod,
			}); err != nil {
				klog.V(2).Infof("Failed to delete old lb-ip pod %s: %v", podName, err)
			}

			c.mutex.Lock()
			c.ipAssignments[ip] = newUUID
			c.mutex.Unlock()

			// Find service for this IP and configure lb-ip pod on new node
			c.mutex.RLock()
			var svcKey string
			for key, svcIP := range c.serviceIPs {
				if svcIP == ip {
					svcKey = key
					break
				}
			}
			c.mutex.RUnlock()

			if svcKey != "" {
				parts := strings.SplitN(svcKey, "/", 2)
				if len(parts) == 2 {
					svc, err := c.TenantClient.CoreV1().Services(parts[0]).Get(ctx, parts[1], metav1.GetOptions{})
					if err == nil && len(svc.Spec.Ports) > 0 {
						port := svc.Spec.Ports[0].Port
						endpointIP := c.getEndpointIP(ctx, svc)
						if endpointIP == "" {
							endpointIP = svc.Spec.ClusterIP
						}
						if err := c.configureIPOnNode(ctx, ip, newUUID, endpointIP, port); err != nil {
							klog.Errorf("Failed to configure IP %s on new node: %v", ip, err)
						}
					}
				}
			}

			klog.Infof("IP failover complete: %s moved from %s to %s", ip, currentUUID, newUUID)
		}
	}

	return nil
}

// getIPPoolType returns the IP pool type from service annotation (default: static)
func (c *LoadBalancerController) getIPPoolType(svc *corev1.Service) string {
	if svc.Annotations != nil {
		if poolType, ok := svc.Annotations[AnnotationIPPoolType]; ok {
			if poolType == IPPoolDynamic {
				return IPPoolDynamic
			}
		}
	}
	return IPPoolStatic
}

// allocateIP finds an available IP from the appropriate pool based on service annotation
func (c *LoadBalancerController) allocateIP(ctx context.Context, svc *corev1.Service) (string, error) {
	poolType := c.getIPPoolType(svc)

	c.mutex.RLock()
	usedIPs := make(map[string]bool)
	for ip := range c.ipAssignments {
		usedIPs[ip] = true
	}

	// Select the appropriate pool based on annotation
	var pool []string
	if poolType == IPPoolDynamic {
		pool = make([]string, len(c.dynamicIPs))
		copy(pool, c.dynamicIPs)
	} else {
		pool = make([]string, len(c.staticIPs))
		copy(pool, c.staticIPs)
	}
	c.mutex.RUnlock()

	klog.V(2).Infof("Allocating IP from %s pool (%d IPs available) for service %s/%s",
		poolType, len(pool), svc.Namespace, svc.Name)

	for _, ip := range pool {
		if !usedIPs[ip] {
			// Verify IP is available via API
			available, err := c.isIPAvailable(ctx, ip)
			if err != nil {
				klog.Errorf("Failed to check IP %s availability: %v", ip, err)
				continue
			}
			if available {
				return ip, nil
			}
		}
	}

	return "", nil
}

// isIPAvailable checks if an IP is available by looking at CloudSigma tags.
// With manual NIC mode, IPs are not attached to servers, so we use service:* tags
// to determine if an IP is already assigned to a LoadBalancer service.
func (c *LoadBalancerController) isIPAvailable(ctx context.Context, ip string) (bool, error) {
	taggedIPs, err := c.getTaggedServiceIPs(ctx)
	if err != nil {
		return false, err
	}
	_, inUse := taggedIPs[ip]
	return !inUse, nil
}

// getTaggedServiceIPs returns a map of IPs that have service:* tags (i.e., assigned to LB services).
// This is used to check IP availability since IPs are no longer attached to servers with manual NIC mode.
func (c *LoadBalancerController) getTaggedServiceIPs(ctx context.Context) (map[string]string, error) {
	token, err := c.ImpersonationClient.GetImpersonatedToken(ctx, c.UserEmail, c.Region)
	if err != nil {
		return nil, err
	}

	listURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/", c.Region)
	req, _ := http.NewRequestWithContext(ctx, "GET", listURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tagList struct {
		Objects []struct {
			UUID      string `json:"uuid"`
			Name      string `json:"name"`
			Resources []struct {
				UUID string `json:"uuid"`
			} `json:"resources"`
		} `json:"objects"`
	}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &tagList)

	// Build map: IP -> service tag name (for IPs that have service:* tags)
	result := make(map[string]string)
	for _, tag := range tagList.Objects {
		if strings.HasPrefix(tag.Name, "service:") {
			for _, r := range tag.Resources {
				result[r.UUID] = tag.Name
			}
		}
	}
	return result, nil
}

// ensureNodeManualMode switches a server's NIC from dhcp/static to "manual" mode.
// With manual mode, the CloudSigma cloud firewall allows traffic for ALL IPs owned
// by the user (with subscription), eliminating the need for per-IP NIC attachment.
// This is a one-time operation per node - once in manual mode, all subscribed IPs
// can be used without further API calls.
func (c *LoadBalancerController) ensureNodeManualMode(ctx context.Context, serverUUID string) error {
	c.mutex.RLock()
	if c.manualModeNodes[serverUUID] {
		c.mutex.RUnlock()
		return nil // Already switched
	}
	c.mutex.RUnlock()

	token, err := c.ImpersonationClient.GetImpersonatedToken(ctx, c.UserEmail, c.Region)
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	// Get current server
	serverURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/servers/%s/", c.Region, serverUUID)
	req, _ := http.NewRequestWithContext(ctx, "GET", serverURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get server: %w", err)
	}
	defer resp.Body.Close()

	var server map[string]interface{}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &server); err != nil {
		return fmt.Errorf("failed to parse server: %w", err)
	}

	// Check if already in manual mode
	nics, _ := server["nics"].([]interface{})
	alreadyManual := false
	for _, nic := range nics {
		nicMap, ok := nic.(map[string]interface{})
		if !ok {
			continue
		}
		ipv4Conf, _ := nicMap["ip_v4_conf"].(map[string]interface{})
		if ipv4Conf == nil {
			continue
		}
		conf, _ := ipv4Conf["conf"].(string)
		if conf == "manual" {
			alreadyManual = true
			break
		}
	}

	if alreadyManual {
		klog.V(2).Infof("Server %s NIC already in manual mode", serverUUID)
		c.mutex.Lock()
		c.manualModeNodes[serverUUID] = true
		c.mutex.Unlock()
		return nil
	}

	// Switch first public NIC (ip_v4_conf) from dhcp/static to manual
	switched := false
	for _, nic := range nics {
		nicMap, ok := nic.(map[string]interface{})
		if !ok {
			continue
		}
		ipv4Conf, _ := nicMap["ip_v4_conf"].(map[string]interface{})
		if ipv4Conf == nil {
			continue
		}
		// Switch to manual mode - remove the specific IP binding
		ipv4Conf["conf"] = "manual"
		delete(ipv4Conf, "ip")
		switched = true
		break
	}

	if !switched {
		return fmt.Errorf("no public NIC found on server %s to switch to manual mode", serverUUID)
	}

	// Update server - preserve all required fields including vnc_password
	server["nics"] = nics
	// Remove read-only fields that can't be sent in update
	delete(server, "resource_uri")
	delete(server, "runtime")
	delete(server, "status")
	delete(server, "uuid")
	delete(server, "owner")
	delete(server, "permissions")
	delete(server, "mounted_on")
	delete(server, "grantees")

	updateBody, _ := json.Marshal(server)

	req, _ = http.NewRequestWithContext(ctx, "PUT", serverURL, strings.NewReader(string(updateBody)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update server NIC: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to switch NIC to manual mode: %s", string(respBody))
	}

	c.mutex.Lock()
	c.manualModeNodes[serverUUID] = true
	c.mutex.Unlock()

	klog.Infof("Switched server %s NIC to manual mode (all subscribed IPs now allowed)", serverUUID)
	return nil
}

// tagIPInCloudSigma adds tags to an IP in CloudSigma to track which cluster/service is using it.
// It also cleans stale tags from the IP (e.g., old service:* or cluster:* tags from previous assignments).
func (c *LoadBalancerController) tagIPInCloudSigma(ctx context.Context, ip, serviceName string) error {
	token, err := c.ImpersonationClient.GetImpersonatedToken(ctx, c.UserEmail, c.Region)
	if err != nil {
		return fmt.Errorf("failed to get token for IP tagging: %w", err)
	}

	// Desired tags for this IP
	desiredTags := map[string]bool{
		fmt.Sprintf("cluster:%s", c.ClusterName):                                true,
		fmt.Sprintf("service:%s", strings.ReplaceAll(serviceName, "/", "-")):     true,
		"managed-by:cloudsigma-ccm":                                             true,
	}

	// Clean stale tags: remove this IP from any CCM-managed tags that don't match current assignment
	if err := c.cleanStaleTags(ctx, token, ip, desiredTags); err != nil {
		klog.Warningf("Failed to clean stale tags from IP %s: %v", ip, err)
	}

	// Add IP to desired tags
	for tagName := range desiredTags {
		if err := c.ensureTagWithIP(ctx, token, tagName, ip); err != nil {
			klog.Warningf("Failed to add IP %s to tag %s: %v", ip, tagName, err)
		}
	}

	klog.Infof("Tagged IP %s with cluster=%s, service=%s", ip, c.ClusterName, serviceName)
	return nil
}

// cleanStaleTags removes an IP from any CCM-managed tags (cluster:*, service:*, managed-by:*)
// that are NOT in the desiredTags set. This cleans up stale tags from previous assignments.
func (c *LoadBalancerController) cleanStaleTags(ctx context.Context, token, ip string, desiredTags map[string]bool) error {
	listURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/", c.Region)
	req, _ := http.NewRequestWithContext(ctx, "GET", listURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list tags: %w", err)
	}
	defer resp.Body.Close()

	var tagList struct {
		Objects []struct {
			UUID      string `json:"uuid"`
			Name      string `json:"name"`
			Resources []struct {
				UUID string `json:"uuid"`
			} `json:"resources"`
		} `json:"objects"`
	}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &tagList)

	for _, tag := range tagList.Objects {
		// Only process CCM-managed tags
		if !strings.HasPrefix(tag.Name, "cluster:") &&
			!strings.HasPrefix(tag.Name, "service:") &&
			tag.Name != "managed-by:cloudsigma-ccm" {
			continue
		}

		// Skip tags that are in the desired set
		if desiredTags[tag.Name] {
			continue
		}

		// Check if this stale tag contains our IP
		var newResources []string
		found := false
		for _, r := range tag.Resources {
			if r.UUID == ip {
				found = true
			} else {
				newResources = append(newResources, r.UUID)
			}
		}

		if found {
			// Remove IP from this stale tag - use resource objects format [{"uuid": "..."}]
			updateURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/%s/", c.Region, tag.UUID)
			resourceObjects := make([]map[string]string, 0, len(newResources))
			for _, uuid := range newResources {
				resourceObjects = append(resourceObjects, map[string]string{"uuid": uuid})
			}
			payload := map[string]interface{}{
				"name":      tag.Name,
				"resources": resourceObjects,
			}
			body, _ := json.Marshal(payload)
			klog.V(4).Infof("Cleaning stale tag %s: PUT %s body=%s", tag.Name, updateURL, string(body))
			req, _ := http.NewRequestWithContext(ctx, "PUT", updateURL, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				klog.Warningf("Failed to remove IP %s from stale tag %s: %v", ip, tag.Name, err)
				continue
			}
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				klog.Warningf("Failed to clean stale tag %s from IP %s: HTTP %d: %s", tag.Name, ip, resp.StatusCode, string(respBody))
			} else {
				klog.Infof("Cleaned stale tag %s from IP %s (HTTP %d)", tag.Name, ip, resp.StatusCode)
			}
		}
	}
	return nil
}

// ensureTagWithIP creates a tag if it doesn't exist and adds the IP to it
func (c *LoadBalancerController) ensureTagWithIP(ctx context.Context, token, tagName, ip string) error {
	// First, list all tags and find by name
	listURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/", c.Region)
	req, _ := http.NewRequestWithContext(ctx, "GET", listURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list tags: %w", err)
	}
	defer resp.Body.Close()

	var tagList struct {
		Objects []struct {
			UUID      string `json:"uuid"`
			Name      string `json:"name"`
			Resources []struct {
				UUID string `json:"uuid"`
			} `json:"resources"`
		} `json:"objects"`
	}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &tagList)

	var tagUUID string
	var existingResourceUUIDs []string

	// Check if tag exists
	for _, t := range tagList.Objects {
		if t.Name == tagName {
			tagUUID = t.UUID
			for _, r := range t.Resources {
				existingResourceUUIDs = append(existingResourceUUIDs, r.UUID)
			}
			break
		}
	}

	if tagUUID == "" {
		// Create new tag with the IP
		createURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/", c.Region)
		payload := map[string]interface{}{
			"objects": []map[string]interface{}{
				{
					"name":      tagName,
					"resources": []string{ip},
				},
			},
		}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, "POST", createURL, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to create tag: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("failed to create tag: %s", string(respBody))
		}
		klog.V(2).Infof("Created tag %s with IP %s", tagName, ip)
	} else {
		// Check if IP already in tag
		for _, uuid := range existingResourceUUIDs {
			if uuid == ip {
				return nil // Already tagged
			}
		}

		// Update existing tag to add the IP - use resource objects format [{"uuid": "..."}]
		updateURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/%s/", c.Region, tagUUID)
		allUUIDs := append(existingResourceUUIDs, ip)
		resourceObjects := make([]map[string]string, 0, len(allUUIDs))
		for _, uuid := range allUUIDs {
			resourceObjects = append(resourceObjects, map[string]string{"uuid": uuid})
		}
		payload := map[string]interface{}{
			"name":      tagName,
			"resources": resourceObjects,
		}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, "PUT", updateURL, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to update tag: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("failed to update tag: %s", string(respBody))
		}
		klog.V(2).Infof("Added IP %s to existing tag %s", ip, tagName)
	}

	return nil
}

// cleanupAllIPTags removes all CCM-managed tags from IPs tracked by this controller.
// Called during shutdown to ensure IPs are released for reuse by new clusters.
func (c *LoadBalancerController) cleanupAllIPTags() {
	// Use a fresh context with timeout since the parent context is cancelled
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c.mutex.Lock()
	ipsToClean := make(map[string]string) // ip -> svcKey
	for svcKey, ip := range c.serviceIPs {
		ipsToClean[ip] = svcKey
	}
	c.mutex.Unlock()

	if len(ipsToClean) == 0 {
		klog.Info("No LB IPs to clean up on shutdown")
		return
	}

	klog.Infof("Cleaning up %d LB IP tags on shutdown", len(ipsToClean))
	for ip, svcKey := range ipsToClean {
		if err := c.untagIPInCloudSigma(ctx, ip); err != nil {
			klog.Warningf("Failed to untag IP %s (service %s) on shutdown: %v", ip, svcKey, err)
		} else {
			klog.Infof("Cleaned up tags for IP %s (service %s) on shutdown", ip, svcKey)
		}
	}
}

// untagIPInCloudSigma removes an IP from CCM-managed tags in CloudSigma when it's released
func (c *LoadBalancerController) untagIPInCloudSigma(ctx context.Context, ip string) error {
	token, err := c.ImpersonationClient.GetImpersonatedToken(ctx, c.UserEmail, c.Region)
	if err != nil {
		return fmt.Errorf("failed to get token for IP untagging: %w", err)
	}

	// List all tags to find ones containing this IP
	listURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/", c.Region)
	req, _ := http.NewRequestWithContext(ctx, "GET", listURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list tags: %w", err)
	}
	defer resp.Body.Close()

	var tagList struct {
		Objects []struct {
			UUID      string `json:"uuid"`
			Name      string `json:"name"`
			Resources []struct {
				UUID string `json:"uuid"`
			} `json:"resources"`
		} `json:"objects"`
	}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &tagList)

	// Remove IP from any CCM-managed tags
	for _, tag := range tagList.Objects {
		// Only process CCM-managed tags
		if !strings.HasPrefix(tag.Name, "cluster:") &&
			!strings.HasPrefix(tag.Name, "service:") &&
			tag.Name != "managed-by:cloudsigma-ccm" {
			continue
		}

		// Check if this tag contains the IP
		var newResources []string
		found := false
		for _, r := range tag.Resources {
			if r.UUID == ip {
				found = true
			} else {
				newResources = append(newResources, r.UUID)
			}
		}

		if found {
			// Update tag to remove the IP - use resource objects format [{"uuid": "..."}]
			updateURL := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0/tags/%s/", c.Region, tag.UUID)
			resourceObjects := make([]map[string]string, 0, len(newResources))
			for _, uuid := range newResources {
				resourceObjects = append(resourceObjects, map[string]string{"uuid": uuid})
			}
			payload := map[string]interface{}{
				"name":      tag.Name,
				"resources": resourceObjects,
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, "PUT", updateURL, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				klog.Warningf("Failed to remove IP %s from tag %s: %v", ip, tag.Name, err)
				continue
			}
			resp.Body.Close()

			if resp.StatusCode >= 400 {
				klog.Warningf("Failed to remove IP %s from tag %s: status %d", ip, tag.Name, resp.StatusCode)
			} else {
				klog.V(2).Infof("Removed IP %s from tag %s", ip, tag.Name)
			}
		}
	}

	klog.Infof("Removed tags from IP %s", ip)
	return nil
}

// deleteIPConfigPod deletes the LB IP config pod for an IP
func (c *LoadBalancerController) deleteIPConfigPod(ctx context.Context, ip string) {
	podName := fmt.Sprintf("lb-ip-%s", strings.ReplaceAll(ip, ".", "-"))
	err := c.TenantClient.CoreV1().Pods("kube-system").Delete(ctx, podName, metav1.DeleteOptions{})
	if err != nil {
		klog.V(2).Infof("Failed to delete config pod %s: %v", podName, err)
	} else {
		klog.Infof("Deleted config pod %s for IP %s", podName, ip)
	}
}

// getEndpointIP returns the first endpoint IP (pod IP) for a service
func (c *LoadBalancerController) getEndpointIP(ctx context.Context, svc *corev1.Service) string {
	endpoints, err := c.TenantClient.CoreV1().Endpoints(svc.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		klog.V(2).Infof("Failed to get endpoints for service %s/%s: %v", svc.Namespace, svc.Name, err)
		return ""
	}

	for _, subset := range endpoints.Subsets {
		for _, addr := range subset.Addresses {
			if addr.IP != "" {
				klog.V(2).Infof("Using endpoint IP %s for service %s/%s", addr.IP, svc.Namespace, svc.Name)
				return addr.IP
			}
		}
	}

	return ""
}

// ensureIPConfigured checks if the LB IP config pod exists and creates it if not
func (c *LoadBalancerController) ensureIPConfigured(ctx context.Context, ip, serverUUID, clusterIP string, port int32) {
	podName := fmt.Sprintf("lb-ip-%s", strings.ReplaceAll(ip, ".", "-"))

	// Check if pod already exists
	_, err := c.TenantClient.CoreV1().Pods("kube-system").Get(ctx, podName, metav1.GetOptions{})
	if err == nil {
		// Pod exists, nothing to do
		return
	}

	// Pod doesn't exist, create it
	klog.Infof("Creating LB IP config pod for %s (recovered state)", ip)
	if err := c.configureIPOnNode(ctx, ip, serverUUID, clusterIP, port); err != nil {
		klog.Warningf("Failed to configure IP %s on node: %v", ip, err)
	}
}

// configureIPOnNode adds the IP locally on the node and sets up iptables rules.
// With manual NIC mode, CloudSigma firewall already allows all subscribed IPs,
// so we only need to configure the IP at the OS level + iptables DNAT.
func (c *LoadBalancerController) configureIPOnNode(ctx context.Context, ip, serverUUID, clusterIP string, port int32) error {
	// Find the node by its providerID
	nodes, err := c.TenantClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	var targetNode *corev1.Node
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if strings.HasSuffix(node.Spec.ProviderID, serverUUID) {
			targetNode = node
			break
		}
	}

	if targetNode == nil {
		return fmt.Errorf("node with providerID containing %s not found", serverUUID)
	}

	// Create a privileged pod to configure the IP and iptables on the node
	podName := fmt.Sprintf("lb-ip-%s", strings.ReplaceAll(ip, ".", "-"))

	privileged := true
	hostNetwork := true

	// Script to:
	// 1. Add IP to primary interface (manual NIC mode allows all subscribed IPs at firewall level)
	// 2. Add iptables DNAT rules for external (PREROUTING) and local (OUTPUT) traffic
	// 3. Add iptables MASQUERADE for return traffic
	configScript := fmt.Sprintf(`
echo "Configuring LoadBalancer IP %s"

# Find primary interface (first non-lo, non-cilium interface)
PRIMARY_IF=$(ip -o link show | grep -v -E 'lo:|cilium|lxc|veth' | head -1 | awk -F': ' '{print $2}')
echo "Primary interface: $PRIMARY_IF"

# Add LoadBalancer IP to primary interface as secondary IP
# NIC is in manual mode - CloudSigma firewall allows all subscribed IPs
ip addr add %s/32 dev $PRIMARY_IF 2>/dev/null || echo "IP already configured on $PRIMARY_IF"

# Send gratuitous ARP to update upstream router/switch MAC table
# Critical for failover: without GARP, traffic still routes to old node's MAC
arping -U -c 3 -I $PRIMARY_IF %s 2>/dev/null &
arping -A -c 3 -I $PRIMARY_IF %s 2>/dev/null &

# Add iptables DNAT rules for external traffic (PREROUTING)
iptables -t nat -C PREROUTING -d %s -p tcp --dport %d -j DNAT --to-destination %s:%d 2>/dev/null || \
  iptables -t nat -I PREROUTING 1 -d %s -p tcp --dport %d -j DNAT --to-destination %s:%d

# Add iptables DNAT rules for local traffic (OUTPUT) - needed for traffic originating from the node
iptables -t nat -C OUTPUT -d %s -p tcp --dport %d -j DNAT --to-destination %s:%d 2>/dev/null || \
  iptables -t nat -I OUTPUT 1 -d %s -p tcp --dport %d -j DNAT --to-destination %s:%d

# Add MASQUERADE for return traffic
iptables -t nat -C POSTROUTING -d %s -p tcp --dport %d -j MASQUERADE 2>/dev/null || \
  iptables -t nat -A POSTROUTING -d %s -p tcp --dport %d -j MASQUERADE

echo "Configured LoadBalancer IP %s on $PRIMARY_IF with DNAT to %s:%d"
# Keep running to maintain the iptables rules
while true; do sleep 3600; done
`, ip, ip, ip, ip, ip, port, clusterIP, port, ip, port, clusterIP, port, ip, port, clusterIP, port, ip, port, clusterIP, port, clusterIP, port, clusterIP, port, ip, clusterIP, port)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "kube-system",
			Labels: map[string]string{
				"app":                "cloudsigma-lb-ip",
				"cloudsigma.com/ip":  ip,
				"cloudsigma.com/svc": clusterIP,
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      targetNode.Name,
			HostNetwork:   hostNetwork,
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{
				{
					Name:    "lb-config",
					Image:   "praqma/network-multitool:alpine-extra",
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{configScript},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
					},
				},
			},
			Tolerations: []corev1.Toleration{
				{Operator: corev1.TolerationOpExists},
			},
		},
	}

	// Delete existing pod if any
	_ = c.TenantClient.CoreV1().Pods("kube-system").Delete(ctx, podName, metav1.DeleteOptions{})

	// Wait briefly for deletion
	time.Sleep(2 * time.Second)

	// Create the pod
	_, err = c.TenantClient.CoreV1().Pods("kube-system").Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create LB IP config pod: %w", err)
	}

	klog.Infof("Created LB IP config pod %s to configure %s on node %s", podName, ip, targetNode.Name)
	return nil
}

// updateServiceStatus updates the LoadBalancer service status with the assigned IP
func (c *LoadBalancerController) updateServiceStatus(ctx context.Context, svc *corev1.Service, ip string) error {
	if ip == "" {
		klog.Warningf("Cannot update service %s/%s status: no IP assigned", svc.Namespace, svc.Name)
		return nil
	}

	svcCopy := svc.DeepCopy()
	svcCopy.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
		{IP: ip},
	}

	klog.Infof("Updating service %s/%s status with IP %s", svc.Namespace, svc.Name, ip)
	_, err := c.TenantClient.CoreV1().Services(svc.Namespace).UpdateStatus(ctx, svcCopy, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Failed to update service %s/%s status: %v", svc.Namespace, svc.Name, err)
		return fmt.Errorf("failed to update service status: %w", err)
	}

	klog.Infof("Successfully updated service %s/%s with LoadBalancer IP %s", svc.Namespace, svc.Name, ip)
	return nil
}

// getHealthyNodes returns nodes that are Ready
func (c *LoadBalancerController) getHealthyNodes(nodes []corev1.Node) []corev1.Node {
	var healthy []corev1.Node
	for _, node := range nodes {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				healthy = append(healthy, node)
				break
			}
		}
	}
	return healthy
}

// getNodeUUID extracts the CloudSigma VM UUID from a node's providerID
func (c *LoadBalancerController) getNodeUUID(node *corev1.Node) string {
	if node.Spec.ProviderID == "" {
		return ""
	}
	// Format: cloudsigma://UUID (prefix is 13 characters)
	const prefix = "cloudsigma://"
	if strings.HasPrefix(node.Spec.ProviderID, prefix) {
		return node.Spec.ProviderID[len(prefix):]
	}
	return ""
}

// isPoolIP checks if an IP is in any pool (static or dynamic)
func (c *LoadBalancerController) isPoolIP(ip string) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.isPoolIPLocked(ip)
}
