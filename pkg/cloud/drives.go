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
	"time"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"k8s.io/klog/v2"
)

// CloneDrive clones a drive (typically a library image) to create a new drive
func (c *Client) CloneDrive(ctx context.Context, sourceUUID, name string, size int64) (*cloudsigma.Drive, error) {
	klog.V(2).Infof("Cloning drive %s to %s (size: %d bytes)", sourceUUID, name, size)

	req := &cloudsigma.DriveCloneRequest{
		Drive: &cloudsigma.Drive{
			Name: name,
			Size: int(size),
		},
	}

	drive, _, err := c.sdk.Drives.Clone(ctx, sourceUUID, req)
	if err != nil {
		return nil, fmt.Errorf("failed to clone drive: %w", err)
	}

	klog.V(2).Infof("Drive cloned successfully: %s (UUID: %s, Status: %s)", drive.Name, drive.UUID, drive.Status)

	// Wait for drive to be ready
	if drive.Status == "creating" || drive.Status == "cloning" {
		klog.V(2).Infof("Waiting for drive to be ready: %s", drive.UUID)
		drive, err = c.WaitForDriveReady(ctx, drive.UUID, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("drive did not become ready: %w", err)
		}
	}

	return drive, nil
}

// WaitForDriveReady waits for a drive to reach "mounted" or "unmounted" status
func (c *Client) WaitForDriveReady(ctx context.Context, uuid string, timeout time.Duration) (*cloudsigma.Drive, error) {
	klog.V(2).Infof("Waiting for drive to be ready: %s", uuid)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timeout waiting for drive to be ready")
			}

			drive, resp, err := c.sdk.Drives.Get(ctx, uuid)
			if err != nil {
				if resp != nil && resp.StatusCode == 404 {
					return nil, fmt.Errorf("drive not found")
				}
				klog.V(4).Infof("Error checking drive status: %v", err)
				continue
			}

			klog.V(4).Infof("Drive %s status: %s", uuid, drive.Status)

			// Drive is ready when status is "mounted" or "unmounted"
			if drive.Status == "mounted" || drive.Status == "unmounted" {
				klog.V(2).Infof("Drive is ready: %s (status: %s)", uuid, drive.Status)
				return drive, nil
			}

			// Check for error states
			if drive.Status == "unavailable" {
				return nil, fmt.Errorf("drive is unavailable")
			}
		}
	}
}

// GetDrive retrieves a drive by UUID
func (c *Client) GetDrive(ctx context.Context, uuid string) (*cloudsigma.Drive, error) {
	klog.V(4).Infof("Getting drive: %s", uuid)

	drive, resp, err := c.sdk.Drives.Get(ctx, uuid)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, nil // Drive not found
		}
		return nil, fmt.Errorf("failed to get drive: %w", err)
	}

	return drive, nil
}

// DeleteDrive deletes a drive
func (c *Client) DeleteDrive(ctx context.Context, uuid string) error {
	klog.V(2).Infof("Deleting drive: %s", uuid)

	_, err := c.sdk.Drives.Delete(ctx, uuid)
	if err != nil {
		return fmt.Errorf("failed to delete drive: %w", err)
	}

	klog.V(2).Infof("Drive deleted successfully: %s", uuid)
	return nil
}
