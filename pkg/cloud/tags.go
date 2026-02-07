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
	"strings"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"k8s.io/klog/v2"
)

// TagServer adds tags to a server in CloudSigma for tracking which cluster/pool owns it.
// Tags: cluster:<name>, pool:<name>, managed-by:cloudsigma-capcs
func (c *Client) TagServer(ctx context.Context, serverUUID, clusterName, poolName string) {
	if c.sdk == nil {
		klog.V(2).Info("CloudSigma SDK client not initialized, skipping server tagging")
		return
	}

	desiredTags := []string{
		"managed-by:cloudsigma-capcs",
	}
	if clusterName != "" {
		desiredTags = append(desiredTags, fmt.Sprintf("cluster:%s", clusterName))
	}
	if poolName != "" {
		desiredTags = append(desiredTags, fmt.Sprintf("pool:%s", poolName))
	}

	for _, tagName := range desiredTags {
		if err := c.ensureTagWithResource(ctx, tagName, serverUUID); err != nil {
			klog.Warningf("Failed to tag server %s with %s: %v", serverUUID, tagName, err)
		}
	}

	klog.Infof("Tagged server %s: cluster=%s, pool=%s", serverUUID, clusterName, poolName)
}

// UntagServer removes a server from all CAPCS-managed tags in CloudSigma.
func (c *Client) UntagServer(ctx context.Context, serverUUID string) {
	if c.sdk == nil {
		klog.V(2).Info("CloudSigma SDK client not initialized, skipping server untagging")
		return
	}

	tags, _, err := c.sdk.Tags.List(ctx)
	if err != nil {
		klog.Warningf("Failed to list tags for server cleanup %s: %v", serverUUID, err)
		return
	}

	for _, tag := range tags {
		if !isCAPCSManagedTag(tag.Name) {
			continue
		}

		var newResources []cloudsigma.TagResource
		found := false
		for _, r := range tag.Resources {
			if r.UUID == serverUUID {
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
		_, _, err := c.sdk.Tags.Update(ctx, tag.UUID, updateReq)
		if err != nil {
			klog.Warningf("Failed to remove server %s from tag %s: %v", serverUUID, tag.Name, err)
		} else {
			klog.V(2).Infof("Removed server %s from tag %s", serverUUID, tag.Name)
		}
	}

	klog.Infof("Untagged server %s from all CAPCS-managed tags", serverUUID)
}

// ensureTagWithResource creates a tag if it doesn't exist and adds the resource to it.
func (c *Client) ensureTagWithResource(ctx context.Context, tagName, resourceUUID string) error {
	tags, _, err := c.sdk.Tags.List(ctx)
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
			_, _, err := c.sdk.Tags.Update(ctx, tag.UUID, updateReq)
			if err != nil {
				return fmt.Errorf("failed to update tag %s: %w", tagName, err)
			}
			klog.V(2).Infof("Added resource %s to existing tag %s", resourceUUID, tagName)
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
	_, _, err = c.sdk.Tags.Create(ctx, createReq)
	if err != nil {
		return fmt.Errorf("failed to create tag %s: %w", tagName, err)
	}
	klog.V(2).Infof("Created tag %s with resource %s", tagName, resourceUUID)
	return nil
}

// isCAPCSManagedTag checks if a tag name is managed by the CAPCS controller.
func isCAPCSManagedTag(name string) bool {
	return name == "managed-by:cloudsigma-capcs" ||
		strings.HasPrefix(name, "cluster:") ||
		strings.HasPrefix(name, "pool:")
}
