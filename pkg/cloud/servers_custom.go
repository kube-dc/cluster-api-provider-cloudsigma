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

package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"k8s.io/klog/v2"
)

// CustomServerDrive represents a server drive with string drive reference
type CustomServerDrive struct {
	BootOrder  int    `json:"boot_order,omitempty"`
	DevChannel string `json:"dev_channel,omitempty"`
	Device     string `json:"device,omitempty"`
	Drive      string `json:"drive"` // UUID string instead of object
}

// CustomServerNIC represents a server NIC with string VLAN reference
type CustomServerNIC struct {
	VLAN     string          `json:"vlan,omitempty"`       // UUID string
	IPv4Conf *CustomIPv4Conf `json:"ip_v4_conf,omitempty"` // IPv4 configuration (CloudSigma uses ip_v4_conf with underscores)
}

// CustomIPv4Conf represents IPv4 configuration for a NIC
type CustomIPv4Conf struct {
	Conf string       `json:"conf"`         // dhcp, static, or manual
	IP   *CustomIPRef `json:"ip,omitempty"` // IP reference for static config
}

// CustomIPRef represents an IP address reference
type CustomIPRef struct {
	UUID string `json:"uuid"` // IP UUID string
}

// CustomServer represents a server for creation
type CustomServer struct {
	Name        string              `json:"name"`
	CPU         int                 `json:"cpu"`
	Memory      int                 `json:"mem"`
	VNCPassword string              `json:"vnc_password"`
	Drives      []CustomServerDrive `json:"drives"`
	NICs        []CustomServerNIC   `json:"nics,omitempty"` // Omit if empty - CloudSigma auto-assigns public IP
	Meta        map[string]string   `json:"meta,omitempty"`
}

// CustomServerCreateRequest wraps servers for creation
type CustomServerCreateRequest struct {
	Servers []CustomServer `json:"objects"`
}

// createServerDirect creates a server using direct HTTP API call to work around SDK limitations
func (c *Client) createServerDirect(ctx context.Context, server *CustomServer) (*cloudsigma.Server, error) {
	klog.Infof("Creating server via direct API call: %s", server.Name)

	req := &CustomServerCreateRequest{
		Servers: []CustomServer{*server},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	klog.Infof("Request body: %s", string(body))

	// Construct API URL (CloudSigma SDK doesn't expose BaseURL)
	// We'll use the environment variable or default
	apiEndpoint := c.apiEndpoint
	if apiEndpoint == "" {
		apiEndpoint = "https://next.cloudsigma.com/api/2.0"
	}
	url := fmt.Sprintf("%s/servers/", apiEndpoint)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// Add authentication - use Bearer token for impersonation, Basic Auth for legacy
	if c.useImpersonation && c.accessToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.accessToken)
		klog.V(4).Infof("Using Bearer token authentication for user: %s", c.impersonatedUser)
	} else {
		httpReq.SetBasicAuth(c.username, c.password)
		klog.V(4).Info("Using Basic Auth authentication")
	}

	// Execute request
	httpClient := &http.Client{}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var result struct {
		Objects []cloudsigma.Server `json:"objects"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(result.Objects) == 0 {
		return nil, fmt.Errorf("no server returned in response")
	}

	klog.Infof("Server created successfully: %s (UUID: %s)", result.Objects[0].Name, result.Objects[0].UUID)
	return &result.Objects[0], nil
}

// UpdateServerNIC updates a server's NIC configuration
// This is used for IP failover - attaching/detaching static IPs
type NICUpdateRequest struct {
	NICs []CustomServerNIC `json:"nics"`
}

// UpdateServerNICs updates the NIC configuration for a server
// The server must be stopped for NIC changes to take effect
func (c *Client) UpdateServerNICs(ctx context.Context, serverUUID string, nics []CustomServerNIC) error {
	klog.Infof("Updating NICs for server %s", serverUUID)

	req := &NICUpdateRequest{
		NICs: nics,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	klog.Infof("NIC update request body: %s", string(body))

	apiEndpoint := c.apiEndpoint
	if apiEndpoint == "" {
		apiEndpoint = "https://next.cloudsigma.com/api/2.0"
	}
	url := fmt.Sprintf("%s/servers/%s/", apiEndpoint, serverUUID)

	httpReq, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	if c.useImpersonation && c.accessToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.accessToken)
	} else {
		httpReq.SetBasicAuth(c.username, c.password)
	}

	httpClient := &http.Client{}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	klog.Infof("NICs updated successfully for server %s", serverUUID)
	return nil
}

// AttachStaticIP attaches a static IP to a server's NIC
// ipUUID is the IP address itself (e.g., "31.171.254.211")
func (c *Client) AttachStaticIP(ctx context.Context, serverUUID, ipUUID string) error {
	klog.Infof("Attaching static IP %s to server %s", ipUUID, serverUUID)

	// Create NIC with static IP configuration
	nics := []CustomServerNIC{
		{
			IPv4Conf: &CustomIPv4Conf{
				Conf: "static",
				IP: &CustomIPRef{
					UUID: ipUUID,
				},
			},
		},
	}

	return c.UpdateServerNICs(ctx, serverUUID, nics)
}

// DetachStaticIP removes a static IP from a server and switches to DHCP
func (c *Client) DetachStaticIP(ctx context.Context, serverUUID string) error {
	klog.Infof("Detaching static IP from server %s, switching to DHCP", serverUUID)

	nics := []CustomServerNIC{
		{
			IPv4Conf: &CustomIPv4Conf{
				Conf: "dhcp",
			},
		},
	}

	return c.UpdateServerNICs(ctx, serverUUID, nics)
}

// GetServerNICs retrieves the current NIC configuration for a server
func (c *Client) GetServerNICs(ctx context.Context, serverUUID string) ([]cloudsigma.ServerNIC, error) {
	server, _, err := c.sdk.Servers.Get(ctx, serverUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get server: %w", err)
	}
	return server.NICs, nil
}

