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
	"sync"
	"time"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const (
	// MinVolumeSize is the minimum volume size (1 GB)
	MinVolumeSize = 1 * 1024 * 1024 * 1024
	// MaxVolumeSize is the maximum volume size (10 TB)
	MaxVolumeSize = 10 * 1024 * 1024 * 1024 * 1024
	// DefaultVolumeSize is the default volume size (10 GB)
	DefaultVolumeSize = 10 * 1024 * 1024 * 1024

	// Storage types
	StorageTypeDSSD     = "dssd"
	StorageTypeMagnetic = "zadara"
)

// CreateVolume creates a new CloudSigma drive
func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}

	if d.cloudClient == nil {
		return nil, status.Error(codes.Internal, "CloudSigma client not initialized")
	}

	// Check volume capabilities
	if req.VolumeCapabilities == nil || len(req.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}

	for _, cap := range req.VolumeCapabilities {
		if !d.isValidVolumeCapability(cap) {
			return nil, status.Errorf(codes.InvalidArgument, "unsupported volume capability: %v", cap)
		}
	}

	// Determine volume size
	size := int64(DefaultVolumeSize)
	if req.CapacityRange != nil {
		if req.CapacityRange.RequiredBytes > 0 {
			size = req.CapacityRange.RequiredBytes
		}
		if req.CapacityRange.LimitBytes > 0 && size > req.CapacityRange.LimitBytes {
			return nil, status.Errorf(codes.OutOfRange, "requested size %d exceeds limit %d", size, req.CapacityRange.LimitBytes)
		}
	}

	if size < MinVolumeSize {
		size = MinVolumeSize
	}
	if size > MaxVolumeSize {
		return nil, status.Errorf(codes.OutOfRange, "requested size %d exceeds maximum %d", size, MaxVolumeSize)
	}
	sizeInt := int(size)

	// Get storage type from parameters
	storageType := StorageTypeDSSD
	if req.Parameters != nil {
		if st, ok := req.Parameters["storageType"]; ok {
			storageType = st
		}
	}

	klog.Infof("Creating volume: name=%s, size=%d, storageType=%s", req.Name, size, storageType)

	// Check if volume already exists (idempotency)
	existingDrive, err := d.findDriveByName(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check existing volume: %v", err)
	}
	if existingDrive != nil {
		klog.Infof("Volume already exists: %s (%s)", req.Name, existingDrive.UUID)
		return &csi.CreateVolumeResponse{
			Volume: &csi.Volume{
				VolumeId:      existingDrive.UUID,
				CapacityBytes: int64(existingDrive.Size),
				VolumeContext: req.Parameters,
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							TopologyKey: d.region,
						},
					},
				},
			},
		}, nil
	}

	// Create the drive
	createReq := &cloudsigma.DriveCreateRequest{
		Drives: []cloudsigma.Drive{
			{
				Name:        req.Name,
				Size:        sizeInt,
				StorageType: storageType,
				Media:       "disk",
			},
		},
	}

	drives, _, err := d.cloudClient.Drives.Create(ctx, createReq)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create volume: %v", err)
	}

	if len(drives) == 0 {
		return nil, status.Error(codes.Internal, "no drive returned from create request")
	}

	drive := drives[0]
	klog.Infof("Volume created: %s (%s)", drive.Name, drive.UUID)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      drive.UUID,
			CapacityBytes: int64(drive.Size),
			VolumeContext: req.Parameters,
			AccessibleTopology: []*csi.Topology{
				{
					Segments: map[string]string{
						TopologyKey: d.region,
					},
				},
			},
		},
	}, nil
}

// DeleteVolume deletes a CloudSigma drive
func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if d.cloudClient == nil {
		return nil, status.Error(codes.Internal, "CloudSigma client not initialized")
	}

	klog.Infof("Deleting volume: %s", req.VolumeId)

	// Check if drive exists
	drive, _, err := d.cloudClient.Drives.Get(ctx, req.VolumeId)
	if err != nil {
		// If not found, consider it already deleted
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			klog.Infof("Volume already deleted: %s", req.VolumeId)
			return &csi.DeleteVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to get volume: %v", err)
	}

	// Check if drive is mounted
	if drive.Status == "mounted" {
		return nil, status.Errorf(codes.FailedPrecondition, "volume %s is still mounted", req.VolumeId)
	}

	// Delete the drive
	_, err = d.cloudClient.Drives.Delete(ctx, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete volume: %v", err)
	}

	klog.Infof("Volume deleted: %s", req.VolumeId)
	return &csi.DeleteVolumeResponse{}, nil
}

// getServerLock returns a mutex for the given server ID, creating one if it doesn't exist
func (d *Driver) getServerLock(serverID string) *sync.Mutex {
	d.serverAttachMu.Lock()
	defer d.serverAttachMu.Unlock()

	if lock, exists := d.serverAttachLocks[serverID]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	d.serverAttachLocks[serverID] = lock
	return lock
}

// ControllerPublishVolume attaches a volume to a node
func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node ID is required")
	}

	if d.cloudClient == nil {
		return nil, status.Error(codes.Internal, "CloudSigma client not initialized")
	}

	// Serialize attachment operations per server to prevent race conditions
	serverLock := d.getServerLock(req.NodeId)
	serverLock.Lock()
	defer serverLock.Unlock()

	klog.Infof("Attaching volume %s to node %s", req.VolumeId, req.NodeId)

	// Get the server
	server, _, err := d.cloudClient.Servers.Get(ctx, req.NodeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "node not found: %v", err)
	}

	// Check if already attached
	for _, sd := range server.Drives {
		if sd.Drive != nil && sd.Drive.UUID == req.VolumeId {
			klog.Infof("Volume %s already attached to node %s at channel %s", req.VolumeId, req.NodeId, sd.DevChannel)
			return &csi.ControllerPublishVolumeResponse{
				PublishContext: map[string]string{
					"channel":  sd.DevChannel,
					"volumeId": req.VolumeId,
				},
			}, nil
		}
	}

	// Get the drive
	drive, _, err := d.cloudClient.Drives.Get(ctx, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "volume not found: %v", err)
	}

	// Check if drive is already mounted to another server
	// If so, we need to detach it first to allow pod migration across nodes
	if drive.Status == "mounted" && len(drive.MountedOn) > 0 {
		for _, mount := range drive.MountedOn {
			if mount.UUID != req.NodeId {
				klog.Warningf("Volume %s is currently attached to node %s, will attempt to detach before attaching to node %s", 
					req.VolumeId, mount.UUID, req.NodeId)
				
				// Try to detach from the old node
				// This handles the case where a pod is rescheduled to a different node
				// and the old volumeattachment hasn't been cleaned up yet
				oldServer, _, getErr := d.cloudClient.Servers.Get(ctx, mount.UUID)
				if getErr != nil {
					if strings.Contains(getErr.Error(), "404") {
						klog.Infof("Old node %s no longer exists, proceeding with attachment", mount.UUID)
						// Old server is gone, we can proceed
						break
					}
					return nil, status.Errorf(codes.Internal, "failed to get old node %s: %v", mount.UUID, getErr)
				}
				
				// Remove the drive from the old server
				newDrives := make([]cloudsigma.ServerDrive, 0, len(oldServer.Drives))
				for _, sd := range oldServer.Drives {
					if sd.Drive == nil || sd.Drive.UUID != req.VolumeId {
						newDrives = append(newDrives, sd)
					}
				}
				
				oldServer.Drives = newDrives
				updateReq := &cloudsigma.ServerUpdateRequest{Server: oldServer}
				_, _, updateErr := d.cloudClient.Servers.Update(ctx, mount.UUID, updateReq)
				if updateErr != nil {
					klog.Warningf("Failed to detach volume %s from old node %s: %v (will proceed anyway)", 
						req.VolumeId, mount.UUID, updateErr)
				} else {
					klog.Infof("Successfully detached volume %s from old node %s", req.VolumeId, mount.UUID)
				}
				break
			}
		}
	}

	// Find the next available device channel
	devChannel := findNextDeviceChannel(server.Drives)

	// Add drive to server (CloudSigma supports hotplug for running VMs)
	server.Drives = append(server.Drives, cloudsigma.ServerDrive{
		BootOrder:  0,
		DevChannel: devChannel,
		Device:     "virtio",
		Drive: &cloudsigma.Drive{
			UUID: req.VolumeId,
		},
	})

	klog.Infof("Hotplugging volume %s to node %s at channel %s (server status: %s)", req.VolumeId, req.NodeId, devChannel, server.Status)

	// Update server (hotplug - no stop/start required)
	updateReq := &cloudsigma.ServerUpdateRequest{Server: server}
	_, _, err = d.cloudClient.Servers.Update(ctx, req.NodeId, updateReq)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to attach volume: %v", err)
	}

	klog.Infof("Volume %s attached to node %s at channel %s", req.VolumeId, req.NodeId, devChannel)

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			"channel":  devChannel,     // Used by node to find device via /dev/disk/by-path/
			"volumeId": req.VolumeId,   // For logging and verification
		},
	}, nil
}

// ControllerUnpublishVolume detaches a volume from a node
func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node ID is required")
	}

	if d.cloudClient == nil {
		return nil, status.Error(codes.Internal, "CloudSigma client not initialized")
	}

	klog.Infof("Detaching volume %s from node %s", req.VolumeId, req.NodeId)

	// Get the server
	server, _, err := d.cloudClient.Servers.Get(ctx, req.NodeId)
	if err != nil {
		// If server not found, consider volume already detached
		if strings.Contains(err.Error(), "404") {
			klog.Infof("Node %s not found, volume %s considered detached", req.NodeId, req.VolumeId)
			return &csi.ControllerUnpublishVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to get node: %v", err)
	}

	// Find and remove the drive
	found := false
	newDrives := make([]cloudsigma.ServerDrive, 0, len(server.Drives))
	for _, sd := range server.Drives {
		if sd.Drive != nil && sd.Drive.UUID == req.VolumeId {
			found = true
			continue
		}
		newDrives = append(newDrives, sd)
	}

	if !found {
		klog.Infof("Volume %s not attached to node %s", req.VolumeId, req.NodeId)
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}

	klog.Infof("Hot-unplugging volume %s from node %s (server status: %s)", req.VolumeId, req.NodeId, server.Status)

	// Update server with removed drive (hotplug - no stop/start required)
	server.Drives = newDrives
	updateReq := &cloudsigma.ServerUpdateRequest{Server: server}
	_, _, err = d.cloudClient.Servers.Update(ctx, req.NodeId, updateReq)
	if err != nil {
		// Log the error but don't fail - if the server API call fails,
		// the volume might already be detached or the server might be deleted
		klog.Warningf("Failed to detach volume %s from node %s via API (continuing anyway): %v", req.VolumeId, req.NodeId, err)
		
		// Verify if the volume is actually still attached by re-fetching the server
		verifyServer, _, verifyErr := d.cloudClient.Servers.Get(ctx, req.NodeId)
		if verifyErr != nil {
			if strings.Contains(verifyErr.Error(), "404") {
				klog.Infof("Node %s no longer exists, volume %s considered detached", req.NodeId, req.VolumeId)
				return &csi.ControllerUnpublishVolumeResponse{}, nil
			}
			// Server exists but we can't verify - return the original error
			return nil, status.Errorf(codes.Internal, "failed to detach volume: %v", err)
		}
		
		// Check if volume is still attached after the failed update
		stillAttached := false
		for _, sd := range verifyServer.Drives {
			if sd.Drive != nil && sd.Drive.UUID == req.VolumeId {
				stillAttached = true
				break
			}
		}
		
		if !stillAttached {
			klog.Infof("Volume %s not attached to node %s after verification, considering detachment successful", req.VolumeId, req.NodeId)
			return &csi.ControllerUnpublishVolumeResponse{}, nil
		}
		
		// Volume is still attached, return error
		return nil, status.Errorf(codes.Internal, "failed to detach volume: %v", err)
	}

	// Verify detachment by polling the drive status
	// CloudSigma detach is asynchronous - the API accepts the request but actual detachment takes time
	klog.Infof("Verifying volume %s is detached from node %s", req.VolumeId, req.NodeId)
	maxRetries := 30 // 30 seconds max
	for i := 0; i < maxRetries; i++ {
		drive, _, err := d.cloudClient.Drives.Get(ctx, req.VolumeId)
		if err != nil {
			if strings.Contains(err.Error(), "404") {
				// Drive deleted, consider it detached
				klog.Infof("Volume %s no longer exists, considered detached", req.VolumeId)
				return &csi.ControllerUnpublishVolumeResponse{}, nil
			}
			klog.Warningf("Failed to verify detachment of volume %s (retry %d/%d): %v", req.VolumeId, i+1, maxRetries, err)
		} else {
			// Check if drive is unmounted
			if drive.Status == "unmounted" && len(drive.MountedOn) == 0 {
				klog.Infof("Volume %s successfully detached from node %s (verified)", req.VolumeId, req.NodeId)
				return &csi.ControllerUnpublishVolumeResponse{}, nil
			}
			klog.V(4).Infof("Volume %s still mounted (status: %s, mounted_on: %d), waiting... (retry %d/%d)", 
				req.VolumeId, drive.Status, len(drive.MountedOn), i+1, maxRetries)
		}
		
		if i < maxRetries-1 {
			time.Sleep(1 * time.Second)
		}
	}

	// Timeout - log warning but don't fail as the detach API call succeeded
	klog.Warningf("Timeout waiting for volume %s detachment verification from node %s after %d seconds (API call succeeded, assuming eventual consistency)", 
		req.VolumeId, req.NodeId, maxRetries)
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities validates the requested capabilities
func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.VolumeCapabilities == nil || len(req.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}

	// Check if volume exists
	if d.cloudClient != nil {
		_, _, err := d.cloudClient.Drives.Get(ctx, req.VolumeId)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "volume not found: %v", err)
		}
	}

	// Validate capabilities
	for _, cap := range req.VolumeCapabilities {
		if !d.isValidVolumeCapability(cap) {
			return &csi.ValidateVolumeCapabilitiesResponse{}, nil
		}
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.VolumeCapabilities,
		},
	}, nil
}

// ControllerGetCapabilities returns the capabilities of the controller
func (d *Driver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	caps := make([]*csi.ControllerServiceCapability, 0, len(d.controllerCaps))
	for _, cap := range d.controllerCaps {
		caps = append(caps, &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: cap,
				},
			},
		})
	}
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: caps,
	}, nil
}

// ControllerExpandVolume expands a volume
func (d *Driver) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if d.cloudClient == nil {
		return nil, status.Error(codes.Internal, "CloudSigma client not initialized")
	}

	newSize := req.CapacityRange.RequiredBytes
	if newSize < MinVolumeSize {
		newSize = MinVolumeSize
	}

	klog.Infof("Expanding volume %s to %d bytes", req.VolumeId, newSize)

	// Get the drive to retrieve its name and media (required by CloudSigma API)
	drive, _, err := d.cloudClient.Drives.Get(ctx, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to get volume for resize: %v", err)
	}

	// Resize the drive
	updateReq := &cloudsigma.DriveUpdateRequest{
		Drive: &cloudsigma.Drive{
			Name:  drive.Name,
			Media: drive.Media,
			Size:  int(newSize),
		},
	}
	_, _, err = d.cloudClient.Drives.Resize(ctx, req.VolumeId, updateReq)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to expand volume: %v", err)
	}

	klog.Infof("Volume %s expanded to %d bytes", req.VolumeId, newSize)

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         newSize,
		NodeExpansionRequired: true,
	}, nil
}

// ListVolumes is not implemented
func (d *Driver) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListVolumes is not implemented")
}

// GetCapacity is not implemented
func (d *Driver) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetCapacity is not implemented")
}

// CreateSnapshot is not implemented yet
func (d *Driver) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "CreateSnapshot is not implemented yet")
}

// DeleteSnapshot is not implemented yet
func (d *Driver) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "DeleteSnapshot is not implemented yet")
}

// ListSnapshots is not implemented
func (d *Driver) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListSnapshots is not implemented")
}

// ControllerGetVolume is not implemented
func (d *Driver) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerGetVolume is not implemented")
}

// ControllerModifyVolume is not implemented
func (d *Driver) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume is not implemented")
}

// Helper functions

func (d *Driver) isValidVolumeCapability(cap *csi.VolumeCapability) bool {
	if cap.GetBlock() != nil {
		return true // Block volumes are supported
	}

	if mount := cap.GetMount(); mount != nil {
		// Check access mode
		accessMode := cap.GetAccessMode().GetMode()
		for _, mode := range d.volumeCaps {
			if accessMode == mode {
				return true
			}
		}
	}

	return false
}

func (d *Driver) findDriveByName(ctx context.Context, name string) (*cloudsigma.Drive, error) {
	drives, _, err := d.cloudClient.Drives.List(ctx, nil)
	if err != nil {
		return nil, err
	}

	for _, drive := range drives {
		if drive.Name == name {
			return &drive, nil
		}
	}

	return nil, nil
}

func (d *Driver) waitForServerStatus(ctx context.Context, serverID, targetStatus string) error {
	for i := 0; i < 60; i++ {
		server, _, err := d.cloudClient.Servers.Get(ctx, serverID)
		if err != nil {
			return err
		}
		if server.Status == targetStatus {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Continue polling
		}
	}
	return fmt.Errorf("timeout waiting for server %s to reach status %s", serverID, targetStatus)
}

func findNextDeviceChannel(drives []cloudsigma.ServerDrive) string {
	usedChannels := make(map[string]bool)
	for _, d := range drives {
		usedChannels[d.DevChannel] = true
	}

	// CloudSigma device channel allocation:
	// - Unit 3 is always skipped on each controller
	// - Controller 0: only unit 2 is available for data disks (0:0 is boot, 0:1 unused, 0:3 skipped)
	// - Controller 1+: units 0,1,2 are available (unit 3 is skipped)
	// This gives us: 0:2, then 1:0, 1:1, 1:2, then 2:0, 2:1, 2:2, etc.
	
	// Start with controller 0, unit 2 only
	if !usedChannels["0:2"] {
		return "0:2"
	}
	
	// Then try controllers 1-202, units 0-2 only (skip unit 3)
	for controller := 1; controller <= 202; controller++ {
		for unit := 0; unit < 3; unit++ { // Only 0, 1, 2 - skip unit 3
			channel := fmt.Sprintf("%d:%d", controller, unit)
			if !usedChannels[channel] {
				return channel
			}
		}
	}

	// Fallback (should never reach here unless all slots are used!)
	return "202:2" // Last available slot
}
