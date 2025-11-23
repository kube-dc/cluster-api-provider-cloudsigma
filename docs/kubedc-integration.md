# KubeDC Integration Guide

## Overview

Cluster API Provider CloudSigma (CAPCS) integrates with [KubeDC](https://github.com/kube-dc/kube-dc-k8-manager) to provide worker node infrastructure for Kamaji-based tenant control planes.

**Architecture:**
```
KubeDC Controller (kube-dc-k8-manager)
  ├── Manages KdcCluster CRD
  ├── Creates Kamaji TenantControlPlane
  └── Delegates worker provisioning to CAPI
          │
          ▼
Cluster API (Management Cluster)
  ├── MachineDeployment Controller
  └── CAPCS Provider (this project)
          │
          ▼
CloudSigma Infrastructure
  └── Worker VMs (join Kamaji control plane)
```

## KdcCluster Integration

### Worker Pool Configuration

KdcCluster can specify CloudSigma as the infrastructure provider for worker pools:

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  version: v1.34.0
  
  # Kamaji control plane
  controlPlane:
    replicas: 3
  
  dataStore:
    dedicated: true
    eipName: default-gw
  
  # Worker pools using CloudSigma
  enableClusterAPI: true
  workers:
    - name: cloudsigma-workers
      replicas: 3
      infrastructureProvider: cloudsigma  # ← Uses CAPCS
      cloudsigma:
        cpu: 2000        # MHz
        memory: 4096     # MB
        diskSize: 50     # GB
        imageUUID: "k8s-worker-ubuntu-22.04-uuid"
        region: "zrh"
        tags:
          - kubernetes
          - production
```

### How It Works

1. **KubeDC Controller** creates:
   - `KdcCluster` resource
   - Kamaji `TenantControlPlane` (manages API server, controller-manager, scheduler)
   - `KdcClusterDatastore` (etcd for control plane)

2. **KubeDC Controller** detects `infrastructureProvider: cloudsigma` and creates:
   - `MachineDeployment` (CAPI resource)
   - `KubeadmConfigTemplate` (bootstrap configuration)
   - `CloudSigmaMachineTemplate` (infrastructure spec)

3. **CAPI Machine Controller** reconciles `MachineDeployment`:
   - Creates `Machine` resources (one per replica)
   - References `CloudSigmaMachine` for infrastructure

4. **CAPCS Controller** (this project) reconciles `CloudSigmaMachine`:
   - Creates CloudSigma server via SDK
   - Injects bootstrap data (cloud-init with kubeadm join)
   - Sets providerID and addresses
   - Updates Machine status

5. **Worker VM** boots:
   - Runs cloud-init
   - Executes `kubeadm join` to Kamaji control plane
   - Becomes ready node in tenant cluster

## Testing with KubeDC

### Prerequisites

1. KubeDC management cluster with:
   - Kamaji installed
   - CAPI core controllers
   - CAPCS provider (this project)

2. CloudSigma credentials configured

3. Worker node image prepared

### Test Scenario

**Create test cluster:**

```bash
# 1. Create KdcCluster with CloudSigma workers
kubectl apply -f - <<EOF
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: test-cluster
  namespace: default
spec:
  version: v1.34.0
  controlPlane:
    replicas: 2
  dataStore:
    dedicated: true
    eipName: default-gw
  network:
    serviceCIDR: 10.96.0.0/12
    podCIDR: 10.220.0.0/16
  eip:
    create: true
    externalNetworkType: public
  enableClusterAPI: true
  workers:
    - name: test-workers
      replicas: 2
      infrastructureProvider: cloudsigma
      cloudsigma:
        cpu: 2000
        memory: 4096
        diskSize: 30
        imageUUID: "your-image-uuid"
        region: "zrh"
EOF
```

**Monitor creation:**

```bash
# Watch KdcCluster
kubectl get kdccluster test-cluster -w

# Watch CAPI resources
kubectl get machinedeployment,machines,cloudsigmamachines

# Watch CloudSigma servers (via CAPCS logs)
kubectl logs -n capcs-system deployment/capcs-controller-manager -f

# Watch Kamaji control plane
kubectl get tenantcontrolplane test-cluster-cp
```

**Verify worker nodes:**

```bash
# Get kubeconfig for tenant cluster
kubectl get secret test-cluster-cp-admin-kubeconfig \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > test-cluster.kubeconfig

# Check nodes joined control plane
kubectl --kubeconfig=test-cluster.kubeconfig get nodes

# Expected output:
# NAME                       STATUS   ROLES    AGE   VERSION
# test-cluster-test-workers-0   Ready    <none>   5m    v1.34.0
# test-cluster-test-workers-1   Ready    <none>   5m    v1.34.0
```

**Test CRUD operations:**

```bash
# Scale up
kubectl patch kdccluster test-cluster --type=json -p='[
  {"op": "replace", "path": "/spec/workers/0/replicas", "value": 4}
]'

# Scale down
kubectl patch kdccluster test-cluster --type=json -p='[
  {"op": "replace", "path": "/spec/workers/0/replicas", "value": 1}
]'

# Add new pool
kubectl patch kdccluster test-cluster --type=json -p='[
  {
    "op": "add",
    "path": "/spec/workers/-",
    "value": {
      "name": "high-memory",
      "replicas": 2,
      "infrastructureProvider": "cloudsigma",
      "cloudsigma": {
        "cpu": 4000,
        "memory": 16384,
        "diskSize": 100,
        "imageUUID": "your-image-uuid",
        "region": "zrh"
      }
    }
  }
]'

# Delete pool
kubectl patch kdccluster test-cluster --type=json -p='[
  {"op": "remove", "path": "/spec/workers/1"}
]'
```

## KubeDC Controller Changes

The KubeDC controller (`kube-dc-k8-manager`) handles CloudSigma integration:

### Worker Reconciliation

```go
// internal/controller/kdccluster_workers.go

func (r *KdcClusterReconciler) reconcileInfrastructureTemplate(
    ctx context.Context,
    cluster *k8sv1alpha1.KdcCluster,
    pool k8sv1alpha1.WorkerPoolSpec,
) error {
    switch pool.InfrastructureProvider {
    case "cloudsigma":
        return r.reconcileCloudSigmaMachineTemplate(ctx, cluster, pool)
    case "kubevirt":
        return r.reconcileKubevirtMachineTemplate(ctx, cluster, pool)
    // ... other providers
    default:
        return fmt.Errorf("unsupported provider: %s", pool.InfrastructureProvider)
    }
}

func (r *KdcClusterReconciler) reconcileCloudSigmaMachineTemplate(
    ctx context.Context,
    cluster *k8sv1alpha1.KdcCluster,
    pool k8sv1alpha1.WorkerPoolSpec,
) error {
    template := &unstructured.Unstructured{}
    template.SetAPIVersion("infrastructure.cluster.x-k8s.io/v1beta1")
    template.SetKind("CloudSigmaMachineTemplate")
    template.SetName(fmt.Sprintf("%s-cloudsigma-%s", cluster.Name, pool.Name))
    template.SetNamespace(cluster.Namespace)
    
    spec := map[string]interface{}{
        "template": map[string]interface{}{
            "spec": map[string]interface{}{
                "cpu":       pool.CloudSigma.CPU,
                "memory":    pool.CloudSigma.Memory,
                "imageUUID": pool.CloudSigma.ImageUUID,
                "region":    pool.CloudSigma.Region,
                "tags":      pool.CloudSigma.Tags,
                "disks": []interface{}{
                    map[string]interface{}{
                        "uuid":       pool.CloudSigma.ImageUUID,
                        "device":     "virtio",
                        "boot_order": 1,
                        "size":       pool.CloudSigma.DiskSize * 1024 * 1024 * 1024,
                    },
                },
                "nics": []interface{}{
                    map[string]interface{}{
                        "vlan": pool.CloudSigma.VLAN.UUID,
                        "ipv4_conf": map[string]interface{}{
                            "conf": "dhcp",
                        },
                    },
                },
            },
        },
    }
    
    template.Object["spec"] = spec
    return r.Client.Create(ctx, template)
}
```

## Control Plane Integration

### Kamaji TenantControlPlane

CAPCS workers connect to Kamaji-managed control planes:

```yaml
# Created by KubeDC Controller
apiVersion: kamaji.clastix.io/v1alpha1
kind: TenantControlPlane
metadata:
  name: test-cluster-cp
  namespace: default
spec:
  dataStore: test-cluster-datastore
  controlPlane:
    deployment:
      replicas: 2
  kubernetes:
    version: v1.34.0
    kubelet:
      cgroupfs: systemd
  networkProfile:
    port: 6443
  addons:
    coreDNS: {}
    kubeProxy: {}
```

### Bootstrap Configuration

KubeDC creates `KubeadmConfigTemplate` with Kamaji control plane endpoint:

```yaml
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: KubeadmConfigTemplate
metadata:
  name: test-cluster-test-workers-bootstrap
  namespace: default
spec:
  template:
    spec:
      joinConfiguration:
        nodeRegistration:
          name: "{{ ds.meta_data.local_hostname }}"
          kubeletExtraArgs:
            cloud-provider: external
            provider-id: "cloudsigma://{{ ds.meta_data.instance_id }}"
        discovery:
          bootstrapToken:
            apiServerEndpoint: "<kamaji-control-plane-ip>:6443"
            token: "<bootstrap-token>"
            caCertHashes:
              - "sha256:<hash>"
```

## Mixed Infrastructure Deployments

KubeDC supports multiple infrastructure providers in a single cluster:

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: mixed-cluster
spec:
  version: v1.34.0
  controlPlane:
    replicas: 3
  enableClusterAPI: true
  workers:
    # CloudSigma workers for production
    - name: cloudsigma-prod
      replicas: 3
      infrastructureProvider: cloudsigma
      cloudsigma:
        cpu: 4000
        memory: 8192
        diskSize: 100
        imageUUID: "prod-image-uuid"
        region: "zrh"
      labels:
        environment: production
        provider: cloudsigma
    
    # KubeVirt workers for development
    - name: kubevirt-dev
      replicas: 2
      infrastructureProvider: kubevirt
      cpuCores: 2
      memory: 4Gi
      labels:
        environment: development
        provider: kubevirt
```

## Troubleshooting

### Workers not provisioning

**Check KubeDC controller:**
```bash
kubectl logs -n kube-dc deployment/kube-dc-k8-manager-controller-manager
```

**Check CAPCS controller:**
```bash
kubectl logs -n capcs-system deployment/capcs-controller-manager
```

**Check CloudSigmaMachine status:**
```bash
kubectl describe cloudsigmamachine <machine-name>
```

### Workers not joining control plane

**Check bootstrap data:**
```bash
kubectl get secret <cluster-name>-<pool-name>-bootstrap-<id> -o yaml
```

**Check Kamaji control plane:**
```bash
kubectl get tenantcontrolplane <cluster-name>-cp
kubectl describe tenantcontrolplane <cluster-name>-cp
```

**Check control plane endpoint:**
```bash
kubectl get svc <cluster-name>-cp
```

## Development Workflow

### Testing CAPCS with KubeDC

1. **Setup development environment:**
```bash
# Run KubeDC controller locally
cd /home/voa/projects/kube-dc-k8-manager
make run

# Run CAPCS controller locally (separate terminal)
cd /home/voa/projects/cluster-api-provider-cloudsigma
make run
```

2. **Create test cluster:**
```bash
kubectl apply -f examples/kubedc-test-cluster.yaml
```

3. **Monitor reconciliation:**
```bash
# Watch both controllers' logs
# KubeDC creates: TenantControlPlane, MachineDeployment, CloudSigmaMachineTemplate
# CAPCS creates: CloudSigma servers
```

4. **Verify integration:**
```bash
# Check all resources
kubectl get kdccluster,tenantcontrolplane,machinedeployment,machines,cloudsigmamachines
```

## References

- **KubeDC Project:** https://github.com/kube-dc/kube-dc-k8-manager
- **Kamaji:** https://github.com/clastix/kamaji
- **Cluster API:** https://cluster-api.sigs.k8s.io/
- **CloudSigma API:** https://docs.cloudsigma.com/

## Support

For integration issues:
- CAPCS Issues: https://github.com/kube-dc/cluster-api-provider-cloudsigma/issues
- KubeDC Issues: https://github.com/kube-dc/kube-dc-k8-manager/issues
