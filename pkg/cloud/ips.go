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
)

// AllocatePublicIP allocates a new public IP address
func (c *Client) AllocatePublicIP(ctx context.Context, name string) (*cloudsigma.IP, error) {
	klog.V(2).Infof("Allocating public IP: %s", name)

	// Allocate IP using list operation (CloudSigma auto-assigns from pool)
	ips, _, err := c.sdk.IPs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list IPs: %w", err)
	}

	// Find an unassigned IP
	for _, availableIP := range ips {
		if availableIP.Server == nil {
			klog.V(2).Infof("Found available IP: %s (UUID: %s)", availableIP.Gateway, availableIP.UUID)
			return &availableIP, nil
		}
	}

	return nil, fmt.Errorf("no available public IPs in pool")
}

// GetIP retrieves an IP by UUID
func (c *Client) GetIP(ctx context.Context, uuid string) (*cloudsigma.IP, error) {
	klog.V(4).Infof("Getting IP: %s", uuid)

	ip, _, err := c.sdk.IPs.Get(ctx, uuid)
	if err != nil {
		return nil, fmt.Errorf("failed to get IP: %w", err)
	}

	if ip == nil {
		return nil, nil
	}

	return ip, nil
}

// DeleteIP releases a public IP address (returns it to pool)
func (c *Client) DeleteIP(ctx context.Context, uuid string) error {
	klog.V(2).Infof("Releasing IP back to pool: %s", uuid)

	// IPs from CloudSigma pool are automatically released when server is deleted
	// No explicit delete action needed
	klog.V(2).Infof("IP will be automatically released when server is deleted: %s", uuid)
	return nil
}
