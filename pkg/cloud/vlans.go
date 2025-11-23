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

// GetVLAN retrieves a VLAN by UUID
func (c *Client) GetVLAN(ctx context.Context, uuid string) (*cloudsigma.VLAN, error) {
	klog.V(4).Infof("Getting VLAN: %s", uuid)

	vlan, resp, err := c.sdk.VLANs.Get(ctx, uuid)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, nil // VLAN not found
		}
		return nil, fmt.Errorf("failed to get VLAN: %w", err)
	}

	return vlan, nil
}

// ListVLANs lists all VLANs
func (c *Client) ListVLANs(ctx context.Context) ([]cloudsigma.VLAN, error) {
	klog.V(4).Info("Listing VLANs")

	vlans, _, err := c.sdk.VLANs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list VLANs: %w", err)
	}

	klog.V(4).Infof("Found %d VLANs", len(vlans))
	return vlans, nil
}

// CreateVLAN creates a new VLAN
// Note: CloudSigma VLAN creation is typically done through the web UI or requires special permissions
// This is a placeholder for future implementation
func (c *Client) CreateVLAN(ctx context.Context, name string, meta map[string]string) (*cloudsigma.VLAN, error) {
	klog.V(2).Infof("VLAN creation not implemented - VLANs should be created through CloudSigma UI: %s", name)
	return nil, fmt.Errorf("VLAN creation not supported via SDK - please create VLAN through CloudSigma UI and specify UUID")
}

// DeleteVLAN deletes a VLAN
// Note: CloudSigma VLAN deletion is typically done through the web UI
// This is a placeholder for future implementation
func (c *Client) DeleteVLAN(ctx context.Context, uuid string) error {
	klog.V(2).Infof("VLAN deletion not implemented: %s", uuid)
	// VLANs are typically not deleted automatically to avoid breaking other servers
	return nil
}
