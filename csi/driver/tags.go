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

package driver

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"k8s.io/klog/v2"
)

// tagDrive adds tags to a drive in CloudSigma for tracking which cluster/volume is using it.
// Tags follow the same pattern as the LB controller: cluster:<name>, volume:<name>, managed-by:cloudsigma-csi
func (d *Driver) tagDrive(ctx context.Context, driveUUID, volumeName string) {
	if d.cloudClient == nil {
		klog.V(2).Info("CloudSigma client not initialized, skipping drive tagging")
		return
	}

	desiredTags := []string{
		"managed-by:cloudsigma-csi",
	}
	if d.clusterName != "" {
		desiredTags = append(desiredTags, fmt.Sprintf("cluster:%s", d.clusterName))
	}
	if volumeName != "" {
		desiredTags = append(desiredTags, fmt.Sprintf("volume:%s", volumeName))
	}

	for _, tagName := range desiredTags {
		if err := d.ensureTagWithResource(ctx, tagName, driveUUID); err != nil {
			klog.Warningf("Failed to tag drive %s with %s: %v", driveUUID, tagName, err)
		}
	}

	klog.Infof("Tagged drive %s: cluster=%s, volume=%s", driveUUID, d.clusterName, volumeName)
}

// untagDrive removes a drive from all CSI-managed tags in CloudSigma.
func (d *Driver) untagDrive(ctx context.Context, driveUUID string) {
	if d.cloudClient == nil {
		klog.V(2).Info("CloudSigma client not initialized, skipping drive untagging")
		return
	}

	tags, _, err := d.cloudClient.Tags.List(ctx)
	if err != nil {
		klog.Warningf("Failed to list tags for drive cleanup %s: %v", driveUUID, err)
		return
	}

	for _, tag := range tags {
		if !isCSIManagedTag(tag.Name) {
			continue
		}

		var newResources []cloudsigma.TagResource
		found := false
		for _, r := range tag.Resources {
			if r.UUID == driveUUID {
				found = true
			} else {
				newResources = append(newResources, r)
			}
		}

		if !found {
			continue
		}

		updateReq := &cloudsigma.TagUpdateRequest{
			Tag: &cloudsigma.Tag{
				Name:      tag.Name,
				Resources: newResources,
			},
		}
		_, _, err := d.cloudClient.Tags.Update(ctx, tag.UUID, updateReq)
		if err != nil {
			klog.Warningf("Failed to remove drive %s from tag %s: %v", driveUUID, tag.Name, err)
		} else {
			klog.V(2).Infof("Removed drive %s from tag %s", driveUUID, tag.Name)
		}
	}

	klog.Infof("Untagged drive %s from all CSI-managed tags", driveUUID)
}

// ensureTagWithResource creates a tag if it doesn't exist and adds the resource to it.
func (d *Driver) ensureTagWithResource(ctx context.Context, tagName, resourceUUID string) error {
	tags, _, err := d.cloudClient.Tags.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tags: %w", err)
	}

	// Check if tag already exists
	for _, tag := range tags {
		if tag.Name == tagName {
			// Check if resource already in tag
			for _, r := range tag.Resources {
				if r.UUID == resourceUUID {
					return nil // Already tagged
				}
			}
			// Add resource to existing tag
			tag.Resources = append(tag.Resources, cloudsigma.TagResource{UUID: resourceUUID})
			updateReq := &cloudsigma.TagUpdateRequest{
				Tag: &cloudsigma.Tag{
					Name:      tag.Name,
					Resources: tag.Resources,
				},
			}
			_, _, err := d.cloudClient.Tags.Update(ctx, tag.UUID, updateReq)
			if err != nil {
				return fmt.Errorf("failed to update tag %s: %w", tagName, err)
			}
			klog.V(2).Infof("Added drive %s to existing tag %s", resourceUUID, tagName)
			return nil
		}
	}

	// Create new tag with the resource
	createReq := &cloudsigma.TagCreateRequest{
		Tags: []cloudsigma.Tag{
			{
				Name:      tagName,
				Resources: []cloudsigma.TagResource{{UUID: resourceUUID}},
			},
		},
	}
	_, _, err = d.cloudClient.Tags.Create(ctx, createReq)
	if err != nil {
		return fmt.Errorf("failed to create tag %s: %w", tagName, err)
	}
	klog.V(2).Infof("Created tag %s with drive %s", tagName, resourceUUID)
	return nil
}

// isCSIManagedTag checks if a tag name is managed by the CSI driver.
func isCSIManagedTag(name string) bool {
	return name == "managed-by:cloudsigma-csi" ||
		strings.HasPrefix(name, "cluster:") ||
		strings.HasPrefix(name, "volume:")
}
