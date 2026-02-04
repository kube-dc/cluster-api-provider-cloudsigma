# CloudSigma CAPI Deployment Hierarchy

## Overview

This document explains the resource hierarchy when deploying CloudSigma workers through kube-dc-k8-manager.

## Resource Hierarchy

```
KdcCluster (kube-dc-k8-manager)
└── Cluster (Cluster API)
    ├── MachineDeployment (Cluster API)
    │   └── MachineSet (Cluster API)
    │       └── Machine (Cluster API)
    │           └── CloudSigmaMachine (CAPCS)
    │               └── CloudSigma VM (actual infrastructure)
    └── CloudSigmaMachineTemplate (CAPCS)
        └── (cloned by Machine controller)
```

## Actual Deployment Example

Based on current working deployment:

### 1. KdcCluster (User Creates)

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: multi-pool-test
  namespace: shalb-envoy
spec:
  version: v1.34.0
  controlPlane:
    replicas: 2
  enableClusterAPI: true
  workers:
    - name: cloudsigma-test
      replicas: 2
      infrastructureProvider: cloudsigma
      cloudsigma:
        cpu: 2000
        memory: 4096
        diskSize: 10  # GB
        imageUUID: "4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79"
        region: "next"
      labels:
        node-type: cloudsigma
```

### 2. CloudSigmaMachineTemplate (Created by KdcCluster Controller)

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaMachineTemplate
metadata:
  name: cloudsigma-test-workers
  namespace: shalb-envoy
spec:
  template:
    spec:
      cpu: 2000
      memory: 4096
      disks:
        - boot_order: 1
          device: virtio
          size: 10737418240  # 10GB in bytes
          uuid: "4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79"
      meta:
        cluster: multi-pool-test
        provider: cloudsigma
      tags:
        - kubernetes
        - cloudsigma
        - auto-bootstrap-v4
```

### 3. MachineDeployment (Created by KdcCluster Controller)

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: cloudsigma-test-workers
  namespace: shalb-envoy
spec:
  clusterName: multi-pool-test
  replicas: 2
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: multi-pool-test
      cluster.x-k8s.io/deployment-name: cloudsigma-test-workers
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: multi-pool-test
        node-type: cloudsigma
    spec:
      clusterName: multi-pool-test
      version: v1.34.0
      bootstrap:
        dataSecretName: cloudsigma-test-workers-bootstrap
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: CloudSigmaMachineTemplate
        name: cloudsigma-test-workers
        namespace: shalb-envoy
```

### 4. Machine (Created by MachineDeployment Controller)

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Machine
metadata:
  name: cloudsigma-test-workers-98wtn-bpr69
  namespace: shalb-envoy
  labels:
    cluster.x-k8s.io/cluster-name: multi-pool-test
    cluster.x-k8s.io/deployment-name: cloudsigma-test-workers
spec:
  clusterName: multi-pool-test
  version: v1.34.0
  bootstrap:
    dataSecretName: multi-pool-test-cloudsigma-test-workers-bootstrap-data
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: CloudSigmaMachine
    name: cloudsigma-test-workers-98wtn-bpr69
    namespace: shalb-envoy
  providerID: cloudsigma://24d94086-f52a-4e2b-b163-6f0bb67ad14b
```

### 5. CloudSigmaMachine (Cloned from Template)

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaMachine
metadata:
  name: cloudsigma-test-workers-98wtn-bpr69
  namespace: shalb-envoy
  annotations:
    cluster.x-k8s.io/cloned-from-groupkind: CloudSigmaMachineTemplate.infrastructure.cluster.x-k8s.io
    cluster.x-k8s.io/cloned-from-name: cloudsigma-test-workers
  labels:
    cluster.x-k8s.io/cluster-name: multi-pool-test
    node-type: cloudsigma
  ownerReferences:
    - apiVersion: cluster.x-k8s.io/v1beta1
      kind: Machine
      name: cloudsigma-test-workers-98wtn-bpr69
spec:
  cpu: 2000
  memory: 4096
  disks:
    - boot_order: 1
      device: virtio
      size: 10737418240
      uuid: "4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79"
  meta:
    cluster: multi-pool-test
    provider: cloudsigma
  tags:
    - kubernetes
    - cloudsigma
    - auto-bootstrap-v4
  providerID: cloudsigma://24d94086-f52a-4e2b-b163-6f0bb67ad14b
status:
  ready: true
  instanceID: 24d94086-f52a-4e2b-b163-6f0bb67ad14b
  instanceState: running
  conditions:
    - type: ServerReady
      status: "True"
      lastTransitionTime: "2025-11-24T07:38:01Z"
```

### 6. CloudSigma VM (Created by CAPCS Controller)

CloudSigma API shows:
- UUID: `24d94086-f52a-4e2b-b163-6f0bb67ad14b`
- State: `running`
- CPU: 2000 MHz
- Memory: 4096 MB
- Disk: Cloned from `4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79`

## Key Field Mappings

### KdcCluster Worker Spec → CloudSigmaMachineTemplate

| KdcCluster Field | CloudSigmaMachineTemplate Field | Notes |
|------------------|--------------------------------|-------|
| `cloudsigma.cpu` | `spec.template.spec.cpu` | CPU in MHz |
| `cloudsigma.memory` | `spec.template.spec.memory` | Memory in MB |
| `cloudsigma.diskSize` | `spec.template.spec.disks[0].size` | Converted to bytes |
| `cloudsigma.imageUUID` | `spec.template.spec.disks[0].uuid` | Boot disk UUID |
| `cloudsigma.region` | N/A | Used for credentials |
| `cloudsigma.tags` | `spec.template.spec.tags` | CloudSigma tags |
| `cloudsigma.meta` | `spec.template.spec.meta` | CloudSigma metadata |
| `labels` | Machine labels | Kubernetes node labels |

## Controller Responsibilities

### KdcCluster Controller (kube-dc-k8-manager)
- Creates Cluster resource
- Creates CloudSigmaMachineTemplate from worker spec
- Creates MachineDeployment referencing the template
- Manages worker pool lifecycle

### CAPI MachineDeployment Controller
- Creates MachineSet
- Manages replica count
- Rolling updates

### CAPI Machine Controller
- Clones CloudSigmaMachine from CloudSigmaMachineTemplate
- Manages bootstrap data
- Sets providerID from CloudSigmaMachine status

### CAPCS CloudSigmaMachine Controller
- Creates CloudSigma VM
- Attaches bootstrap data (cloud-init)
- Waits for VM to be running
- Sets status with instanceID and providerID
- Handles deletion with finalizers

## Bootstrap Process

1. **Bootstrap Data Creation**: KdcCluster controller creates bootstrap secret
2. **CloudSigma VM Creation**: CAPCS controller creates VM with bootstrap data in metadata
3. **Cloud-Init Execution**: VM boots, cloud-init reads metadata, executes kubeadm join
4. **Node Registration**: Node joins cluster with providerID `cloudsigma://<uuid>`
5. **CCM Integration**: CloudSigma CCM sets node IP addresses

## Monitoring the Deployment

```bash
# Watch KdcCluster
kubectl get kdccluster multi-pool-test -n shalb-envoy -w

# Watch CAPI resources
kubectl get cluster,machinedeployment,machine -n shalb-envoy

# Watch CloudSigma resources
kubectl get cloudsigmamachines,cloudsigmamachinetemplates -n shalb-envoy

# Check Machine status
kubectl describe machine <machine-name> -n shalb-envoy

# Check CloudSigmaMachine status
kubectl describe cloudsigmamachine <machine-name> -n shalb-envoy

# View controller logs
kubectl logs -n capcs-system -l control-plane=controller-manager -f
```

## Troubleshooting

### Machine not creating
1. Check MachineDeployment: `kubectl describe md <name> -n shalb-envoy`
2. Check CloudSigmaMachineTemplate exists
3. Check CAPCS controller logs

### CloudSigmaMachine not ready
1. Check CloudSigmaMachine status: `kubectl describe cloudsigmamachine <name> -n shalb-envoy`
2. Check CAPCS controller logs for errors
3. Verify CloudSigma credentials secret
4. Check CloudSigma API access

### VM created but not joining
1. Check bootstrap data secret exists
2. SSH to VM: `ssh cloudsigma@<vm-ip>` (password: Cloud2025)
3. Check cloud-init logs: `sudo tail -f /var/log/cloud-init-output.log`
4. Check bootstrap service: `sudo systemctl status cloudsigma-bootstrap`

## References

- [KdcCluster Examples](https://github.com/kube-dc/kube-dc-k8-manager/tree/main/examples)
- [CloudSigmaMachine CRD](./api-reference.md)
- [Quick Start Guide](./quickstart.md)
