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
func (c *Client) CreateVLAN(ctx context.Context, name string, meta map[string]string) (*cloudsigma.VLAN, error) {
	klog.V(2).Infof("Creating VLAN: %s", name)

	req := &cloudsigma.VLANCreateRequest{
		Name: name,
	}

	if len(meta) > 0 {
		req.Meta = make(map[string]interface{})
		for k, v := range meta {
			req.Meta[k] = v
		}
	}

	vlan, _, err := c.sdk.VLANs.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create VLAN: %w", err)
	}

	klog.V(2).Infof("VLAN created successfully: %s (UUID: %s)", vlan.Name, vlan.UUID)
	return vlan, nil
}

// DeleteVLAN deletes a VLAN
func (c *Client) DeleteVLAN(ctx context.Context, uuid string) error {
	klog.V(2).Infof("Deleting VLAN: %s", uuid)

	_, err := c.sdk.VLANs.Delete(ctx, uuid)
	if err != nil {
		return fmt.Errorf("failed to delete VLAN: %w", err)
	}

	klog.V(2).Infof("VLAN deleted successfully: %s", uuid)
	return nil
}
