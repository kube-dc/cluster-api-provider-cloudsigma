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
	klog.Infof("==> CreateServer called for: %s (CPU: %d MHz, Memory: %d MB, Disks: %d)",
		spec.Name, spec.CPU, spec.Memory, len(spec.Disks))

	// Clone drives first (CloudSigma requires unique drive per server)
	clonedDrives := make([]string, 0, len(spec.Disks))
	for i, disk := range spec.Disks {
		klog.Infof("==> Disk %d: UUID=%s, Size=%d", i, disk.UUID, disk.Size)
		driveName := fmt.Sprintf("%s-drive-%d", spec.Name, i)
		klog.Infof("==> Starting drive clone: source=%s, name=%s", disk.UUID, driveName)

		clonedDrive, err := c.CloneDrive(ctx, disk.UUID, driveName, disk.Size)
		if err != nil {
			klog.Errorf("==> Clone failed: %v", err)
			// Clean up any drives we created
			for _, uuid := range clonedDrives {
				_ = c.DeleteDrive(ctx, uuid)
			}
			return nil, fmt.Errorf("failed to clone drive %s: %w", disk.UUID, err)
		}
		klog.Infof("==> Clone succeeded: %s", clonedDrive.UUID)
		clonedDrives = append(clonedDrives, clonedDrive.UUID)
	}

	klog.Infof("==> All drives cloned: %v", clonedDrives)

	// Build custom server object (using strings for drive/VLAN references)
	server := &CustomServer{
		Name:        spec.Name,
		CPU:         spec.CPU,
		Memory:      spec.Memory * 1024 * 1024, // Convert MB to bytes
		VNCPassword: "kubernetes",              // Required by CloudSigma API
	}

	// Add cloned disks
	for i, disk := range spec.Disks {
		driveUUID := clonedDrives[i]
		klog.Infof("==> Adding drive %d: UUID=%s", i, driveUUID)

		serverDrive := CustomServerDrive{
			BootOrder:  disk.BootOrder,
			DevChannel: fmt.Sprintf("0:%d", disk.BootOrder),
			Device:     disk.Device,
			Drive:      driveUUID, // Just the UUID string
		}
		klog.Infof("==> ServerDrive: BootOrder=%d, DevChannel=%s, Device=%s, Drive=%s",
			serverDrive.BootOrder, serverDrive.DevChannel, serverDrive.Device, serverDrive.Drive)
		server.Drives = append(server.Drives, serverDrive)
	}

	klog.Infof("==> Total server drives: %d", len(server.Drives))

	// Add NICs with VLAN and IPv4 configuration (if specified)
	if len(spec.NICs) > 0 {
		klog.Infof("==> Configuring %d NIC(s)", len(spec.NICs))
		for i, nic := range spec.NICs {
			if nic.VLAN != "" {
				// NIC with VLAN
				customNIC := CustomServerNIC{
					VLAN: nic.VLAN, // VLAN UUID string
				}

				// Add IPv4 configuration if specified
				if nic.IPv4Conf.Conf != "" {
					customNIC.IPv4Conf = &CustomIPv4Conf{
						Conf: nic.IPv4Conf.Conf,
					}

					// Add static IP reference if provided
					if nic.IPv4Conf.IP != nil && nic.IPv4Conf.IP.UUID != "" {
						customNIC.IPv4Conf.IP = &CustomIPRef{
							UUID: nic.IPv4Conf.IP.UUID,
						}
					}

					klog.Infof("==> NIC %d: VLAN=%s, IPv4Conf=%s", i, nic.VLAN, nic.IPv4Conf.Conf)
				} else {
					klog.Warningf("==> NIC %d: VLAN specified but no IPv4 config", i)
				}

				server.NICs = append(server.NICs, customNIC)
			} else {
				// NIC without VLAN - create PUBLIC IP with DHCP
				klog.Infof("==> NIC %d: No VLAN specified, creating PUBLIC IP with DHCP", i)
				customNIC := CustomServerNIC{
					IPv4Conf: &CustomIPv4Conf{
						Conf: "dhcp",
					},
				}
				server.NICs = append(server.NICs, customNIC)
			}
		}
	} else {
		// No NICs array specified at all - add PUBLIC IP with DHCP
		// CloudSigma requires either VLAN or ip_v4_conf - we use ip_v4_conf for public network
		klog.Infof("==> No NICs specified in template, adding PUBLIC IP with DHCP")
		publicNIC := CustomServerNIC{
			IPv4Conf: &CustomIPv4Conf{
				Conf: "dhcp",
			},
		}
		server.NICs = append(server.NICs, publicNIC)
	}

	// Add metadata (cloud-init)
	if spec.BootstrapData != "" {
		if server.Meta == nil {
			server.Meta = make(map[string]string)
		}
		server.Meta["base64_fields"] = "cloudinit-user-data"
		server.Meta["cloudinit-user-data"] = spec.BootstrapData
	}

	// Add custom metadata
	if len(spec.Meta) > 0 {
		if server.Meta == nil {
			server.Meta = make(map[string]string)
		}
		for k, v := range spec.Meta {
			server.Meta[k] = v
		}
	}

	// Note: Tags are not directly supported in CustomServer structure
	// They would need to be added to CustomServer if required

	// Create server using direct API call (SDK has serialization issues)
	createdServer, err := c.createServerDirect(ctx, server)
	if err != nil {
		// Clean up cloned drives on failure
		for _, uuid := range clonedDrives {
			_ = c.DeleteDrive(ctx, uuid)
		}
		return nil, fmt.Errorf("failed to create server: %w", err)
	}
	klog.V(2).Infof("Server created successfully: %s (UUID: %s)", createdServer.Name, createdServer.UUID)
	return createdServer, nil
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

// DeleteServer deletes a server and its associated drives
func (c *Client) DeleteServer(ctx context.Context, uuid string) error {
	klog.V(2).Infof("Deleting server: %s", uuid)

	// Get server to retrieve drive UUIDs
	server, err := c.GetServer(ctx, uuid)
	if err != nil {
		return err
	}

	if server == nil {
		klog.V(2).Infof("Server not found, assuming already deleted: %s", uuid)
		return nil
	}

	// Remember drive UUIDs and IP UUIDs for cleanup
	driveUUIDs := make([]string, 0, len(server.Drives))
	for _, drive := range server.Drives {
		if drive.Drive != nil {
			driveUUIDs = append(driveUUIDs, drive.Drive.UUID)
		}
	}

	// Remember IP UUIDs for cleanup (public IPs without VLAN)
	ipUUIDs := make([]string, 0)
	for _, nic := range server.NICs {
		// If NIC has no VLAN but has an IP, it's a public IP we allocated
		if nic.VLAN == nil && nic.IP4Configuration != nil && nic.IP4Configuration.IPAddress != nil {
			ipUUIDs = append(ipUUIDs, nic.IP4Configuration.IPAddress.UUID)
		}
	}

	// Stop server if running
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

	// Clean up cloned drives
	for _, driveUUID := range driveUUIDs {
		klog.V(2).Infof("Deleting drive: %s", driveUUID)
		if err := c.DeleteDrive(ctx, driveUUID); err != nil {
			klog.Errorf("Failed to delete drive %s: %v (continuing)", driveUUID, err)
			// Continue with other drives even if one fails
		}
	}

	// Clean up allocated public IPs
	for _, ipUUID := range ipUUIDs {
		klog.V(2).Infof("Releasing public IP: %s", ipUUID)
		if err := c.DeleteIP(ctx, ipUUID); err != nil {
			klog.Errorf("Failed to release IP %s: %v (continuing)", ipUUID, err)
			// Continue with other IPs even if one fails
		}
	}

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
