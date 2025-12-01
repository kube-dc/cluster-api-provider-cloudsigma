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
	"strconv"
	"strings"

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

// ControllerPublishVolume attaches a volume to a node (server)
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

	klog.Infof("Attaching volume %s to node %s", req.VolumeId, req.NodeId)

	// Get the server
	server, _, err := d.cloudClient.Servers.Get(ctx, req.NodeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "node not found: %v", err)
	}

	// Check if already attached
	for _, sd := range server.Drives {
		if sd.Drive != nil && sd.Drive.UUID == req.VolumeId {
			klog.Infof("Volume %s already attached to node %s", req.VolumeId, req.NodeId)
			return &csi.ControllerPublishVolumeResponse{
				PublishContext: map[string]string{
					"devicePath": getDevicePathFromChannel(sd.DevChannel),
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
	if drive.Status == "mounted" && len(drive.MountedOn) > 0 {
		for _, mount := range drive.MountedOn {
			if mount.UUID != req.NodeId {
				return nil, status.Errorf(codes.FailedPrecondition, "volume %s is already attached to another node %s", req.VolumeId, mount.UUID)
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
			"devicePath": getDevicePathFromChannel(devChannel),
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
		return nil, status.Errorf(codes.Internal, "failed to detach volume: %v", err)
	}

	klog.Infof("Volume %s detached from node %s", req.VolumeId, req.NodeId)
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

	// Resize the drive
	updateReq := &cloudsigma.DriveUpdateRequest{
		Drive: &cloudsigma.Drive{
			Size: int(newSize),
		},
	}
	_, _, err := d.cloudClient.Drives.Resize(ctx, req.VolumeId, updateReq)
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

	// Try channels 0:1 through 0:15 (skip 0:0 which is usually boot drive)
	for i := 1; i < 16; i++ {
		channel := fmt.Sprintf("0:%d", i)
		if !usedChannels[channel] {
			return channel
		}
	}

	return "0:15" // Fallback
}

func getDevicePathFromChannel(channel string) string {
	// Convert CloudSigma channel (e.g., "0:2") to Linux device path
	// CloudSigma maps channel 0:N directly to /dev/vd{N}
	// Examples: 0:0→vda, 0:2→vdc, 0:3→vdd, 0:4→vde
	parts := strings.Split(channel, ":")
	if len(parts) != 2 {
		return "/dev/vdb"
	}

	idx, err := strconv.Atoi(parts[1])
	if err != nil {
		return "/dev/vdb"
	}

	// Direct mapping: channel index to device letter
	letter := byte('a' + idx)
	return fmt.Sprintf("/dev/vd%c", letter)
}
