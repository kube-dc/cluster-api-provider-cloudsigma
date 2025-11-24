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

	// Add basic auth (same as SDK uses)
	httpReq.SetBasicAuth(c.username, c.password)

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
