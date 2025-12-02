# CloudSigma CSI Driver

The CloudSigma CSI (Container Storage Interface) driver enables dynamic provisioning, attachment, and management of persistent volumes on CloudSigma infrastructure for Kubernetes clusters.

## Features

- **Dynamic Volume Provisioning**: Automatically create CloudSigma drives for PersistentVolumeClaims
- **Volume Attachment**: Hot-plug volumes to running nodes
- **Volume Expansion**: Resize volumes (offline only)
- **Volume Snapshots**: Create and restore volume snapshots
- **Storage Classes**: Support for different CloudSigma storage types (DSSD)
- **Topology Awareness**: Zone-aware volume placement

## Architecture

The CSI driver consists of two components:

### Controller Plugin
- Runs as a Deployment in the `cloudsigma-csi` namespace
- Handles volume lifecycle operations (create, delete, attach, detach, expand, snapshot)
- Includes sidecars: csi-provisioner, csi-attacher, csi-resizer, csi-snapshotter

### Node Plugin
- Runs as a DaemonSet on each node
- Handles volume staging and publishing (mount operations)
- Performs filesystem expansion
- Reports node capacity and topology

## Deployment

### Prerequisites

1. CloudSigma credentials with API access
2. Kubernetes cluster v1.20+
3. CSI feature gates enabled (default in modern Kubernetes)

### Installation

1. Create namespace and secret:
```bash
kubectl create namespace cloudsigma-csi

kubectl create secret generic cloudsigma-credentials \
  -n cloudsigma-csi \
  --from-literal=username='your-username' \
  --from-literal=password='your-password' \
  --from-literal=region='zrh'  # or 'next'
```

2. Deploy CSI driver (managed by cluster-api-provider-cloudsigma)

### Verify Installation

```bash
# Check CSI driver pods
kubectl get pods -n cloudsigma-csi

# Check CSI driver registration
kubectl get csidriver csi.cloudsigma.com

# Check storage class
kubectl get storageclass cloudsigma-dssd
```

## Usage

### Creating a PersistentVolumeClaim

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: cloudsigma-dssd
  resources:
    requests:
      storage: 10Gi
```

### Using in a Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-pod
spec:
  containers:
  - name: app
    image: nginx
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: my-pvc
```

### Storage Classes

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: cloudsigma-dssd
provisioner: csi.cloudsigma.com
parameters:
  storageType: dssd  # CloudSigma DSSD storage
allowVolumeExpansion: true
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
```

## Volume Lifecycle

### 1. Volume Creation (CreateVolume)
- CSI provisioner watches for new PVCs
- Controller creates CloudSigma drive via API
- Drive is created with requested size and storage type
- Volume ID is the CloudSigma drive UUID

### 2. Volume Attachment (ControllerPublishVolume)
- CSI attacher watches for volume attachment requests
- Controller hot-plugs drive to node (running VM)
- Returns channel information (e.g., `1:1`) for device discovery
- Implements detachment verification to prevent stuck drives

### 3. Volume Staging (NodeStageVolume)
- Node plugin discovers device using `/dev/disk/by-path/virtio-pci-*`
- **Battle-proof device discovery**:
  - Snapshots existing devices before attachment
  - Polls for new device appearance (max 10 seconds)
  - Validates device is a block device and not boot disk
  - Handles pre-existing unmounted disks (uses newest)
  - Mutex serialization prevents race conditions
- Formats device if unformatted (ext4)
- Mounts to staging path

### 4. Volume Publishing (NodePublishVolume)
- Bind-mounts from staging path to pod's volume path

### 5. Volume Unpublishing (NodeUnpublishVolume)
- Unmounts volume from pod's path

### 6. Volume Unstaging (NodeUnstageVolume)
- Unmounts volume from staging path

### 7. Volume Detachment (ControllerUnpublishVolume)
- Controller hot-unplugs drive from node
- **Detachment verification** (added in v1.2.5):
  - Polls drive status for up to 30 seconds
  - Verifies `status == "unmounted"` and `mounted_on == []`
  - Handles CloudSigma's asynchronous detachment
  - Prevents "volume still mounted" errors during deletion

### 8. Volume Deletion (DeleteVolume)
- Controller deletes CloudSigma drive via API
- Only succeeds if volume is fully detached

## Device Discovery

The CSI driver uses **stable device paths** for reliable volume identification:

### Implementation Details

```
/dev/disk/by-path/virtio-pci-0000:00:06.0 -> ../../vdb
/dev/disk/by-path/virtio-pci-0000:00:07.0 -> ../../vdc
```

**Discovery Algorithm:**
1. Acquire mutex lock (serializes concurrent operations)
2. Snapshot existing `/dev/disk/by-path/virtio-pci-*` devices
3. Filter out boot disk (`/dev/vda`) and partitions (`-part*`)
4. Check for unmounted data disks:
   - If exactly 1 found → use it (pre-attached volume)
   - If multiple → use newest (most recently attached)
5. If none found, wait for NEW device to appear
6. Validate: block device, not boot disk, unique match
7. Hold mutex through mounting to prevent race conditions

### Why Not /dev/vdX?

Direct device names (`/dev/vdb`, `/dev/vdc`) are **unreliable**:
- Order depends on attachment sequence
- Changes across reboots
- Race conditions with concurrent attachments
- Led to incorrect device assignments

## Volume Expansion

### Supported ✅
CloudSigma CSI driver supports volume expansion with **limitations**.

### CloudSigma Limitation: Offline Expansion Only ⚠️

CloudSigma API **does not support online (hot) expansion**. Attempting to resize a mounted volume returns:
```
403 Cannot resize drive mounted on a running guest
```

### Expansion Workflow

#### 1. Request Expansion
```bash
kubectl patch pvc my-pvc -p '{"spec":{"resources":{"requests":{"storage":"20Gi"}}}}'
```

#### 2. Offline Expansion Process
```bash
# Delete pod to detach volume
kubectl delete pod my-pod

# Wait for volume to detach (CSI handles automatically)
# CSI resizer expands volume at CloudSigma
# Volume capacity updated

# Recreate pod to reattach volume
kubectl apply -f my-pod.yaml

# CSI node expands filesystem automatically
```

#### 3. Verify Expansion
```bash
kubectl exec my-pod -- df -h /data
```

### Implementation Details

**Controller Expansion** (`ControllerExpandVolume`):
- Gets drive details (name, media required by API)
- Calls CloudSigma `Drives.Resize()` API
- Returns `NodeExpansionRequired: true`

**Node Expansion** (`NodeExpandVolume`):
- Gets device path from mount point
- Calls `resize2fs` (ext4) or `xfs_growfs` (xfs)
- Expands filesystem to use new capacity

### Automatic vs Manual Expansion

- **Automatic**: CSI handles resize when pod is deleted/recreated
- **Manual**: You must delete pod first, CSI cannot resize while mounted

## Snapshots

### Creating a Snapshot

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: my-snapshot
spec:
  volumeSnapshotClassName: cloudsigma-snapshot
  source:
    persistentVolumeClaimName: my-pvc
```

### Restoring from Snapshot

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-pvc
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: cloudsigma-dssd
  dataSource:
    name: my-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  resources:
    requests:
      storage: 10Gi
```

## RBAC Requirements

### Controller ServiceAccount Permissions

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cloudsigma-csi-controller
rules:
- apiGroups: [""]
  resources: ["persistentvolumes"]
  verbs: ["get", "list", "watch", "create", "delete", "patch"]
- apiGroups: [""]
  resources: ["persistentvolumeclaims"]
  verbs: ["get", "list", "watch", "update", "patch"]
- apiGroups: [""]
  resources: ["persistentvolumeclaims/status"]
  verbs: ["patch"]
- apiGroups: ["storage.k8s.io"]
  resources: ["storageclasses"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["events"]
  verbs: ["list", "watch", "create", "update", "patch"]
- apiGroups: ["storage.k8s.io"]
  resources: ["csinodes"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["storage.k8s.io"]
  resources: ["volumeattachments"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: ["storage.k8s.io"]
  resources: ["volumeattachments/status"]
  verbs: ["patch"]
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list"]
- apiGroups: [""]
  resources: ["pods"]  # Required for csi-resizer
  verbs: ["get", "list", "watch"]
- apiGroups: ["snapshot.storage.k8s.io"]
  resources: ["volumesnapshots", "volumesnapshotcontents", "volumesnapshotclasses"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

## Version History

### v1.2.7 (Latest)
- **Fixed**: Volume resize API validation (name and media fields)
- **Documented**: Offline-only expansion limitation

### v1.2.5
- **Added**: Detachment verification with polling (30s timeout)
- **Fixed**: Stuck drives preventing deletion
- **Added**: RBAC for pod listing (csi-resizer requirement)

### v1.2.4
- **Implemented**: Battle-proof device discovery via `/dev/disk/by-path/`
- **Added**: Mutex serialization for concurrent operations
- **Fixed**: Multiple volume handling on same node
- **Removed**: Unreliable `/dev/vdX` fallback logic

### v1.2.0
- Initial CSI driver implementation
- Basic volume provisioning and attachment
- Snapshot support

## Troubleshooting

### Volume Not Attaching

**Symptoms**: Pod stuck in `ContainerCreating`, events show `FailedMount`

**Check**:
```bash
# Check CSI driver logs
kubectl logs -n cloudsigma-csi deployment/csi-controller -c csi-controller

# Check node plugin logs
kubectl logs -n cloudsigma-csi daemonset/csi-node -c csi-node
```

**Common causes**:
- CloudSigma API credentials incorrect
- Network connectivity to CloudSigma API
- Node not found in CloudSigma

### Device Not Found

**Symptoms**: `device /dev/vdX not found` or timeout waiting for device

**Solution**: Verify device discovery logs:
```bash
kubectl logs -n cloudsigma-csi daemonset/csi-node -c csi-node | grep "findDeviceByPath"
```

**Fixed in v1.2.4** with stable `/dev/disk/by-path/` discovery

### Volume Resize Fails

**Error**: `Cannot resize drive mounted on a running guest`

**Solution**: This is expected. CloudSigma requires offline expansion:
1. Delete pod using the volume
2. Wait for PVC resize to complete
3. Recreate pod
4. Verify new size with `df -h`

### Volume Stuck in Deleting

**Symptoms**: PVC/PV stuck in `Terminating` state

**Check**:
```bash
# Check if volume is still attached
kubectl get volumeattachment

# Check controller logs
kubectl logs -n cloudsigma-csi deployment/csi-controller -c csi-controller | grep -i detach
```

**Fixed in v1.2.5** with detachment verification

### Multiple Volumes Race Condition

**Symptoms**: Wrong device mounted, data corruption

**Solution**: Fixed in v1.2.4 with mutex serialization. Ensure using latest image:
```bash
kubectl set image daemonset/csi-node -n cloudsigma-csi \
  csi-node=shalb/cloudsigma-csi:v1.2.4
```

## Limitations

1. **No Online Volume Expansion**: Volumes must be detached for resize (CloudSigma platform limitation)
2. **ReadWriteOnce Only**: Multi-attach not supported (CloudSigma limitation)
3. **No Volume Cloning**: Direct PVC-to-PVC cloning not implemented (use snapshots instead)
4. **Single Region**: Volumes cannot be moved between CloudSigma regions

## Performance

### Storage Types

- **DSSD**: CloudSigma Distributed SSD
  - High performance
  - Suitable for databases, general workloads
  - Default storage class

### Benchmarks

Typical performance (depends on VM size and CloudSigma infrastructure):
- **Sequential Read**: ~200-500 MB/s
- **Sequential Write**: ~150-300 MB/s
- **Random IOPS**: 5k-15k IOPS

## Best Practices

1. **Use WaitForFirstConsumer**: Ensures volume created in same zone as pod
   ```yaml
   volumeBindingMode: WaitForFirstConsumer
   ```

2. **Enable Volume Expansion**: Allow resize without recreation
   ```yaml
   allowVolumeExpansion: true
   ```

3. **Set Reclaim Policy**: Choose Delete (default) or Retain
   ```yaml
   reclaimPolicy: Delete  # or Retain
   ```

4. **Monitor Detachment**: Check logs if volumes fail to delete
   ```bash
   kubectl logs -n cloudsigma-csi deployment/csi-controller -c csi-controller
   ```

5. **Plan for Offline Resize**: Design apps to tolerate volume expansion downtime

6. **Use Snapshots**: Regular backups via VolumeSnapshot for data protection

## API Reference

### StorageClass Parameters

| Parameter | Description | Required | Default |
|-----------|-------------|----------|---------|
| `storageType` | CloudSigma storage type | No | `dssd` |

### Volume Context (PublishContext)

| Key | Description | Example |
|-----|-------------|---------|
| `channel` | Device channel on VM | `1:1` |
| `volumeId` | CloudSigma drive UUID | `debe0474-...` |

## CloudSigma API Integration

The CSI driver uses the [cloudsigma-sdk-go](https://github.com/cloudsigma/cloudsigma-sdk-go) client library.

### Endpoints

- **ZRH**: `https://zrh.cloudsigma.com/api/2.0/`
- **NEXT**: `https://next.cloudsigma.com/api/2.0/`

### API Operations

| CSI Operation | CloudSigma API Call |
|---------------|---------------------|
| CreateVolume | `POST /drives/` |
| DeleteVolume | `DELETE /drives/{uuid}/` |
| ControllerPublishVolume | `PUT /servers/{uuid}/` (add drive) |
| ControllerUnpublishVolume | `PUT /servers/{uuid}/` (remove drive) |
| ControllerExpandVolume | `POST /drives/{uuid}/action/?do=resize` |
| CreateSnapshot | `POST /snapshots/` |
| DeleteSnapshot | `DELETE /snapshots/{uuid}/` |

## Development

### Building Images

```bash
cd csi

# Build controller
docker build --target controller -t shalb/cloudsigma-csi:v1.2.7-controller -f Dockerfile ..

# Build node
docker build --target node -t shalb/cloudsigma-csi:v1.2.7-node -f Dockerfile ..

# Push
docker push shalb/cloudsigma-csi:v1.2.7-controller
docker push shalb/cloudsigma-csi:v1.2.7-node
```

### Testing

See [Testing Guide](testing.md) for CSI driver testing procedures.

## Support

For issues or questions:
- GitHub Issues: https://github.com/kube-dc/cluster-api-provider-cloudsigma/issues
- CloudSigma Support: https://www.cloudsigma.com/support/
