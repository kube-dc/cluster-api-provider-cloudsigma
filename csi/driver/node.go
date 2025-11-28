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
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	kmount "k8s.io/mount-utils"
)

// NodeStageVolume mounts the volume to a staging path
func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.StagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if req.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	stagingPath := req.StagingTargetPath

	// Get device path from publish context
	devicePath := ""
	if req.PublishContext != nil {
		devicePath = req.PublishContext["devicePath"]
	}
	if devicePath == "" {
		return nil, status.Error(codes.InvalidArgument, "device path not found in publish context")
	}

	klog.Infof("Staging volume %s at %s (device: %s)", req.VolumeId, stagingPath, devicePath)

	// Check if device exists
	if _, err := os.Stat(devicePath); os.IsNotExist(err) {
		return nil, status.Errorf(codes.NotFound, "device %s not found", devicePath)
	}

	// Handle block volume
	if req.VolumeCapability.GetBlock() != nil {
		// For block volumes, staging is not needed
		klog.Infof("Block volume %s staged (no-op)", req.VolumeId)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Handle filesystem volume
	mount := req.VolumeCapability.GetMount()
	if mount == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability must be mount or block")
	}

	fsType := mount.FsType
	if fsType == "" {
		fsType = "ext4"
	}

	// Create staging directory
	if err := os.MkdirAll(stagingPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create staging directory: %v", err)
	}

	// Check if already mounted
	mounter := kmount.New("")
	mounted, err := isMounted(mounter, stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check mount status: %v", err)
	}
	if mounted {
		klog.Infof("Volume %s already staged at %s", req.VolumeId, stagingPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Format if needed
	formatted, err := isFormatted(devicePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if device is formatted: %v", err)
	}
	if !formatted {
		klog.Infof("Formatting device %s with %s", devicePath, fsType)
		if err := formatDevice(devicePath, fsType); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to format device: %v", err)
		}
	}

	// Mount the device
	mountOptions := mount.MountFlags
	klog.Infof("Mounting %s to %s with fsType=%s, options=%v", devicePath, stagingPath, fsType, mountOptions)

	if err := mounter.Mount(devicePath, stagingPath, fsType, mountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount device: %v", err)
	}

	klog.Infof("Volume %s staged at %s", req.VolumeId, stagingPath)
	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeUnstageVolume unmounts the volume from the staging path
func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.StagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}

	stagingPath := req.StagingTargetPath

	klog.Infof("Unstaging volume %s from %s", req.VolumeId, stagingPath)

	mounter := kmount.New("")

	// Check if mounted
	mounted, err := isMounted(mounter, stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check mount status: %v", err)
	}

	if mounted {
		if err := mounter.Unmount(stagingPath); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to unmount staging path: %v", err)
		}
		klog.Infof("Volume %s unstaged from %s", req.VolumeId, stagingPath)
	} else {
		klog.Infof("Volume %s already unstaged from %s", req.VolumeId, stagingPath)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume bind mounts the staged volume to the target path
func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if req.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	targetPath := req.TargetPath
	stagingPath := req.StagingTargetPath

	klog.Infof("Publishing volume %s to %s", req.VolumeId, targetPath)

	// Handle block volume
	if req.VolumeCapability.GetBlock() != nil {
		devicePath := ""
		if req.PublishContext != nil {
			devicePath = req.PublishContext["devicePath"]
		}
		if devicePath == "" {
			return nil, status.Error(codes.InvalidArgument, "device path required for block volume")
		}

		// Create parent directory
		parentDir := filepath.Dir(targetPath)
		if err := os.MkdirAll(parentDir, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create parent directory: %v", err)
		}

		// Create the target file for block device
		file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR, 0660)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create target file: %v", err)
		}
		file.Close()

		// Bind mount the block device
		mounter := kmount.New("")
		if err := mounter.Mount(devicePath, targetPath, "", []string{"bind"}); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to bind mount block device: %v", err)
		}

		klog.Infof("Block volume %s published to %s", req.VolumeId, targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Handle filesystem volume
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required for filesystem volumes")
	}

	// Create target directory
	if err := os.MkdirAll(targetPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create target directory: %v", err)
	}

	mounter := kmount.New("")

	// Check if already mounted
	mounted, err := isMounted(mounter, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check mount status: %v", err)
	}
	if mounted {
		klog.Infof("Volume %s already published to %s", req.VolumeId, targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Bind mount from staging to target
	mountOptions := []string{"bind"}
	if req.Readonly {
		mountOptions = append(mountOptions, "ro")
	}

	klog.Infof("Bind mounting %s to %s", stagingPath, targetPath)
	if err := mounter.Mount(stagingPath, targetPath, "", mountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bind mount: %v", err)
	}

	klog.Infof("Volume %s published to %s", req.VolumeId, targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmounts the volume from the target path
func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}

	targetPath := req.TargetPath

	klog.Infof("Unpublishing volume %s from %s", req.VolumeId, targetPath)

	mounter := kmount.New("")

	// Check if mounted
	mounted, err := isMounted(mounter, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check mount status: %v", err)
	}

	if mounted {
		if err := mounter.Unmount(targetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to unmount target path: %v", err)
		}
		klog.Infof("Volume %s unpublished from %s", req.VolumeId, targetPath)
	} else {
		klog.Infof("Volume %s already unpublished from %s", req.VolumeId, targetPath)
	}

	// Clean up target path
	if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
		klog.Warningf("Failed to remove target path %s: %v", targetPath, err)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetVolumeStats returns volume statistics
func (d *Driver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.VolumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}

	volumePath := req.VolumePath

	// Check if path exists
	if _, err := os.Stat(volumePath); os.IsNotExist(err) {
		return nil, status.Errorf(codes.NotFound, "volume path %s not found", volumePath)
	}

	// Get filesystem stats
	var statfs unix.Statfs_t
	if err := unix.Statfs(volumePath, &statfs); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get volume stats: %v", err)
	}

	totalBytes := int64(statfs.Blocks) * int64(statfs.Bsize)
	availableBytes := int64(statfs.Bavail) * int64(statfs.Bsize)
	usedBytes := totalBytes - availableBytes

	totalInodes := int64(statfs.Files)
	availableInodes := int64(statfs.Ffree)
	usedInodes := totalInodes - availableInodes

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Total:     totalBytes,
				Used:      usedBytes,
				Available: availableBytes,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Total:     totalInodes,
				Used:      usedInodes,
				Available: availableInodes,
			},
		},
	}, nil
}

// NodeExpandVolume expands the filesystem on the node
func (d *Driver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.VolumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}

	volumePath := req.VolumePath

	klog.Infof("Expanding filesystem on volume %s at %s", req.VolumeId, volumePath)

	// Get device path from mount point
	devicePath, err := getDeviceFromMountPoint(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get device from mount point: %v", err)
	}

	// Resize the filesystem
	if err := resizeFilesystem(devicePath, volumePath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resize filesystem: %v", err)
	}

	klog.Infof("Filesystem expanded on volume %s", req.VolumeId)

	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: req.CapacityRange.RequiredBytes,
	}, nil
}

// NodeGetCapabilities returns the capabilities of the node server
func (d *Driver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	caps := make([]*csi.NodeServiceCapability, 0, len(d.nodeCaps))
	for _, cap := range d.nodeCaps {
		caps = append(caps, &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: cap,
				},
			},
		})
	}
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: caps,
	}, nil
}

// NodeGetInfo returns information about the node
func (d *Driver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId:            d.nodeID,
		MaxVolumesPerNode: 15, // CloudSigma limit per server
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				TopologyKey: d.region,
			},
		},
	}, nil
}

// Helper functions

func isMounted(mounter kmount.Interface, path string) (bool, error) {
	// Check if path exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}

	// Get mount points
	mountPoints, err := mounter.List()
	if err != nil {
		return false, err
	}

	for _, mp := range mountPoints {
		if mp.Path == path {
			return true, nil
		}
	}

	return false, nil
}

func isFormatted(devicePath string) (bool, error) {
	cmd := exec.Command("blkid", "-p", "-s", "TYPE", "-o", "value", devicePath)
	output, err := cmd.Output()
	if err != nil {
		// Exit code 2 means no filesystem found
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			return false, nil
		}
		return false, err
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

func formatDevice(devicePath, fsType string) error {
	var cmd *exec.Cmd
	switch fsType {
	case "ext4":
		cmd = exec.Command("mkfs.ext4", "-F", devicePath)
	case "ext3":
		cmd = exec.Command("mkfs.ext3", "-F", devicePath)
	case "xfs":
		cmd = exec.Command("mkfs.xfs", "-f", devicePath)
	default:
		return fmt.Errorf("unsupported filesystem type: %s", fsType)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("format failed: %v, output: %s", err, string(output))
	}
	return nil
}

func getDeviceFromMountPoint(mountPoint string) (string, error) {
	mounter := kmount.New("")
	mountPoints, err := mounter.List()
	if err != nil {
		return "", err
	}

	for _, mp := range mountPoints {
		if mp.Path == mountPoint {
			return mp.Device, nil
		}
	}

	return "", fmt.Errorf("mount point %s not found", mountPoint)
}

func resizeFilesystem(devicePath, mountPoint string) error {
	// Detect filesystem type
	cmd := exec.Command("blkid", "-p", "-s", "TYPE", "-o", "value", devicePath)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to detect filesystem: %v", err)
	}

	fsType := strings.TrimSpace(string(output))

	switch fsType {
	case "ext4", "ext3", "ext2":
		cmd = exec.Command("resize2fs", devicePath)
	case "xfs":
		cmd = exec.Command("xfs_growfs", mountPoint)
	default:
		return fmt.Errorf("unsupported filesystem for resize: %s", fsType)
	}

	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resize failed: %v, output: %s", err, string(output))
	}

	return nil
}
