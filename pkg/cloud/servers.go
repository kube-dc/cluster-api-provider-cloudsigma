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
	"context"
	"fmt"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"k8s.io/klog/v2"

	infrav1 "github.com/kube-dc/cluster-api-provider-cloudsigma/api/v1beta1"
)

// ServerSpec defines the specifications for creating a server
type ServerSpec struct {
	Name          string
	CPU           int
	Memory        int
	Disks         []infrav1.CloudSigmaDisk
	NICs          []infrav1.CloudSigmaNIC
	Tags          []string
	Meta          map[string]string
	BootstrapData string // Cloud-init user data
}

// CreateServer creates a new CloudSigma server
func (c *Client) CreateServer(ctx context.Context, spec ServerSpec) (*cloudsigma.Server, error) {
	klog.V(2).Infof("Creating CloudSigma server: %s (CPU: %d MHz, Memory: %d MB)", spec.Name, spec.CPU, spec.Memory)

	// Build server object
	server := &cloudsigma.Server{
		Name:   spec.Name,
		CPU:    spec.CPU,
		Memory: spec.Memory,
	}

	// Add disks
	for _, disk := range spec.Disks {
		server.Drives = append(server.Drives, cloudsigma.ServerDrive{
			BootOrder:  disk.BootOrder,
			DevChannel: fmt.Sprintf("0:%d", disk.BootOrder),
			Device:     disk.Device,
			Drive: &cloudsigma.Drive{
				UUID: disk.UUID,
				Size: int(disk.Size),
			},
		})
	}

	// Add NICs
	for _, nic := range spec.NICs {
		nicReq := cloudsigma.ServerNIC{
			VLAN: &cloudsigma.VLAN{
				UUID: nic.VLAN,
			},
		}

		// Configure IP
		if nic.IPv4Conf.Conf == "dhcp" {
			nicReq.IP4Configuration = &cloudsigma.ServerIPConfiguration{
				Type: "dhcp",
			}
		} else if nic.IPv4Conf.Conf == "static" && nic.IPv4Conf.IP != nil {
			nicReq.IP4Configuration = &cloudsigma.ServerIPConfiguration{
				Type: "static",
				IPAddress: &cloudsigma.IP{
					UUID: nic.IPv4Conf.IP.UUID,
				},
			}
		}

		server.NICs = append(server.NICs, nicReq)
	}

	// Add metadata (cloud-init)
	if spec.BootstrapData != "" {
		if server.Meta == nil {
			server.Meta = make(map[string]interface{})
		}
		server.Meta["base64_fields"] = "cloudinit-user-data"
		server.Meta["cloudinit-user-data"] = spec.BootstrapData
	}

	// Add custom metadata
	if len(spec.Meta) > 0 {
		if server.Meta == nil {
			server.Meta = make(map[string]interface{})
		}
		for k, v := range spec.Meta {
			server.Meta[k] = v
		}
	}

	// Add tags
	if len(spec.Tags) > 0 {
		server.Tags = make([]cloudsigma.Tag, len(spec.Tags))
		for i, tag := range spec.Tags {
			server.Tags[i] = cloudsigma.Tag{Name: tag}
		}
	}

	// Create server request
	req := &cloudsigma.ServerCreateRequest{
		Servers: []cloudsigma.Server{*server},
	}

	// Create server
	servers, _, err := c.sdk.Servers.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create server: %w", err)
	}

	if len(servers) == 0 {
		return nil, fmt.Errorf("server creation returned no servers")
	}

	createdServer := servers[0]
	klog.V(2).Infof("Server created successfully: %s (UUID: %s)", createdServer.Name, createdServer.UUID)
	return &createdServer, nil
}

// GetServer retrieves a server by UUID
func (c *Client) GetServer(ctx context.Context, uuid string) (*cloudsigma.Server, error) {
	klog.V(4).Infof("Getting server: %s", uuid)

	server, resp, err := c.sdk.Servers.Get(ctx, uuid)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, nil // Server not found
		}
		return nil, fmt.Errorf("failed to get server: %w", err)
	}

	return server, nil
}

// StartServer starts a stopped server
func (c *Client) StartServer(ctx context.Context, uuid string) error {
	klog.V(2).Infof("Starting server: %s", uuid)

	_, _, err := c.sdk.Servers.Start(ctx, uuid)
	if err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	klog.V(2).Infof("Server start initiated: %s", uuid)
	return nil
}

// StopServer stops a running server
func (c *Client) StopServer(ctx context.Context, uuid string) error {
	klog.V(2).Infof("Stopping server: %s", uuid)

	_, _, err := c.sdk.Servers.Stop(ctx, uuid)
	if err != nil {
		return fmt.Errorf("failed to stop server: %w", err)
	}

	klog.V(2).Infof("Server stop initiated: %s", uuid)
	return nil
}

// DeleteServer deletes a server
func (c *Client) DeleteServer(ctx context.Context, uuid string) error {
	klog.V(2).Infof("Deleting server: %s", uuid)

	// Stop server if running
	server, err := c.GetServer(ctx, uuid)
	if err != nil {
		return err
	}

	if server == nil {
		klog.V(2).Infof("Server not found, assuming already deleted: %s", uuid)
		return nil
	}

	if server.Status == "running" {
		klog.V(2).Infof("Stopping server before deletion: %s", uuid)
		if err := c.StopServer(ctx, uuid); err != nil {
			return fmt.Errorf("failed to stop server before deletion: %w", err)
		}

		// Wait for server to stop (TODO: add proper wait logic)
		klog.V(2).Info("Waiting for server to stop...")
	}

	// Delete server
	_, err = c.sdk.Servers.Delete(ctx, uuid)
	if err != nil {
		return fmt.Errorf("failed to delete server: %w", err)
	}

	klog.V(2).Infof("Server deleted successfully: %s", uuid)
	return nil
}

// ListServers lists all servers
func (c *Client) ListServers(ctx context.Context) ([]cloudsigma.Server, error) {
	klog.V(4).Info("Listing servers")

	servers, _, err := c.sdk.Servers.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	klog.V(4).Infof("Found %d servers", len(servers))
	return servers, nil
}
