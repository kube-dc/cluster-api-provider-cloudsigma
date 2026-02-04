# CloudSigma Integration - Quick Start Guide

This guide helps you get started with CloudSigma worker nodes in KubeDC.

## Overview

```
┌─────────────────────────────────────────────────────────┐
│  Management Cluster (kube-dc)                           │
│  ├── KubeDC Controller                                  │
│  ├── CAPI Controllers                                   │
│  └── CloudSigma Provider (CAPCS)                        │
└─────────────────────┬───────────────────────────────────┘
                      │
         ┌────────────┴────────────┐
         ▼                         ▼
  ┌─────────────┐          ┌─────────────┐
  │ CloudSigma  │          │   Kamaji    │
  │   Servers   │ ───────> │  Control    │
  │  (Workers)  │  join    │   Plane     │
  └─────────────┘          └─────────────┘
```

## Prerequisites

- [ ] Kubernetes management cluster v1.26+
- [ ] CloudSigma account with API access  
- [ ] kubectl v1.26+
- [ ] Worker node image prepared (see `images/ubuntu-k8s/`)

## Step-by-Step Guide

### Step 1: Install CAPCS Provider

```bash
# Install CloudSigma CAPI provider
kubectl apply -f https://raw.githubusercontent.com/kube-dc/cluster-api-provider-cloudsigma/main/config/install.yaml

# Or from local repository
kubectl apply -f config/install.yaml

# Verify installation
kubectl get pods -n capcs-system
```

### Step 2: Install CRDs

```bash
# Install CloudSigma Custom Resource Definitions
kubectl apply -f https://raw.githubusercontent.com/kube-dc/cluster-api-provider-cloudsigma/main/config/crd/bases/

# Or from local repository
kubectl apply -f config/crd/bases/

# Verify CRDs
kubectl get crd | grep cloudsigma
```

### Step 3: Configure CloudSigma Credentials

```bash
# Create secret with CloudSigma API credentials
kubectl create secret generic cloudsigma-credentials \
  --namespace=capcs-system \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-api-password' \
  --from-literal=region='zrh'

# Verify secret
kubectl get secret cloudsigma-credentials -n capcs-system

# Check provider logs
kubectl logs -n capcs-system -l control-plane=controller-manager
```

### Step 4: Prepare Worker Image

Use the working Kubernetes-ready image:

```bash
# Current tested image UUID (CloudSigma ZRH region)
CLOUDSIGMA_IMAGE_UUID="4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79"

# This image includes:
# - Ubuntu 24.04
# - Kubernetes tools (kubeadm, kubelet, kubectl)
# - Containerd runtime
# - CloudSigma bootstrap service
# - SSH access: user cloudsigma / password Cloud2025
```

To build your own image, see `images/ubuntu-k8s/README.md`.

### Step 5: Deploy CloudSigma Workers

Use one of the example configurations:

**Option A: Simple 2-worker cluster** (recommended for testing)
```bash
kubectl apply -f examples/cloudsigma-test-cluster.yaml
```

**Option B: Cluster with VLAN networking**
```bash
# First, update the VLAN UUID in the example
kubectl apply -f examples/cloudsigma-cluster-with-vlan.yaml
```

**Option C: Manual deployment**
```yaml
# my-workers.yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaMachineTemplate
metadata:
  name: worker-template
  namespace: default
spec:
  template:
    spec:
      cpu: 2000          # 2 GHz
      memory: 4096       # 4 GB
      diskUUID: "4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79"
      nics:
        - ipv4Conf: dhcp  # Public NAT IP
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: worker-pool
  namespace: default
spec:
  replicas: 2
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: my-cluster
    spec:
      clusterName: my-cluster
      version: v1.30.0
      bootstrap:
        dataSecretName: worker-bootstrap-data
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: CloudSigmaMachineTemplate
        name: worker-template
```

```bash
kubectl apply -f my-workers.yaml
```

### Step 6: Monitor Worker Provisioning

```bash
# Watch CloudSigma machines
kubectl get cloudsigmamachines -A -w

# Check machine status
kubectl describe cloudsigmamachine <machine-name>

# View provider logs
kubectl logs -n capcs-system -l control-plane=controller-manager -f

# Check events
kubectl get events -A --sort-by='.lastTimestamp' | grep CloudSigma
```

### Step 7: Verify Workers Joined

```bash
# Check nodes in your cluster
kubectl get nodes

# Expected output:
# NAME                              STATUS   ROLES    AGE   VERSION
# cloudsigma-worker-1               Ready    <none>   5m    v1.30.0
# cloudsigma-worker-2               Ready    <none>   5m    v1.30.0

# View machine details
kubectl get cloudsigmamachines -o wide

# Check CloudSigma VMs via API or web console
```

### Step 8: Deploy CloudSigma CCM (in Tenant Cluster)

```bash
# Create credentials in tenant cluster
kubectl create secret generic cloudsigma-credentials \
  --namespace=kube-system \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-api-password' \
  --from-literal=region='zrh'

# Deploy CCM
kubectl apply -f https://github.com/shalb/cloudsigma-cloud-controller-manager/releases/latest/download/cloudsigma-ccm.yaml

# Verify CCM
kubectl get pods -n kube-system -l app=cloudsigma-cloud-controller-manager

# Check nodes have providerID
kubectl get nodes -o custom-columns=NAME:.metadata.name,PROVIDER-ID:.spec.providerID
```

## Common Operations

### Scale Workers Up

```bash
kubectl patch kdccluster my-cluster --type=json -p='[
  {"op": "replace", "path": "/spec/workers/0/replicas", "value": 5}
]'
```

### Scale Workers Down

```bash
kubectl patch kdccluster my-cluster --type=json -p='[
  {"op": "replace", "path": "/spec/workers/0/replicas", "value": 1}
]'
```

### Add New Worker Pool

```bash
kubectl patch kdccluster my-cluster --type=json -p='[
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
        "region": "zrh",
        "tags": ["kubernetes", "high-memory"]
      },
      "labels": {
        "workload-type": "memory-intensive"
      }
    }
  }
]'
```

### Remove Worker Pool

```bash
# Delete pool at index 1 (second pool)
kubectl patch kdccluster my-cluster --type=json -p='[
  {"op": "remove", "path": "/spec/workers/1"}
]'
```

### Check Worker Status

```bash
# In management cluster
kubectl get machinedeployment
kubectl get machines
kubectl get cloudsigmamachines

# In tenant cluster
kubectl get nodes
kubectl top nodes
```

## Troubleshooting

### Provider not starting

```bash
# Check pod status
kubectl get pods -n capcs-system

# View logs
kubectl logs -n capcs-system deployment/cloudsigma-controller-manager

# Common issues:
# - Missing credentials secret
# - RBAC permissions not applied
# - Image pull errors
```

### Workers not provisioning

```bash
# Check CloudSigmaMachine status
kubectl describe cloudsigmamachine <name>

# Check CAPCS provider logs
kubectl logs -n capcs-system deployment/cloudsigma-controller-manager -f

# Verify credentials
kubectl get secret cloudsigma-credentials -n capcs-system -o yaml

# Check CloudSigma API connectivity
kubectl logs -n capcs-system -l control-plane=controller-manager | grep -i error
```

### Workers not joining cluster

```bash
# Check bootstrap data
kubectl get secret <cluster-name>-<pool-name>-bootstrap-<id> -o yaml

# Check CloudSigma server console (via web UI)
# Look for cloud-init logs: /var/log/cloud-init-output.log
```

### Nodes showing NotReady

```bash
# Check node status
kubectl describe node <node-name>

# Common causes:
# - CCM not deployed
# - Network plugin not installed
# - Kubelet not started

# Check kubelet logs on node:
# journalctl -u kubelet -f
```

## Cost Optimization

### Use development clusters efficiently

```bash
# Scale to zero when not in use
kubectl patch kdccluster dev-cluster --type=json -p='[
  {"op": "replace", "path": "/spec/workers/0/replicas", "value": 0}
]'

# Scale back up when needed
kubectl patch kdccluster dev-cluster --type=json -p='[
  {"op": "replace", "path": "/spec/workers/0/replicas", "value": 3}
]'
```

### Right-size workers

Monitor resource usage:
```bash
kubectl top nodes
kubectl top pods -A
```

Adjust worker specs based on actual usage.

## Next Steps

- [ ] Set up monitoring (Prometheus/Grafana)
- [ ] Configure cluster-autoscaler
- [ ] Implement backup strategy
- [ ] Set up GitOps (Flux/ArgoCD)
- [ ] Configure network policies
- [ ] Set up logging (ELK/Loki)

## Resources

- [Full Integration Plan](CLOUDSIGMA_INTEGRATION_PLAN.md)
- [CRD Design](CAPCS_CRD_DESIGN.md)
- [CCM Deployment](CCM_DEPLOYMENT.md)
- [CloudSigma API Docs](https://docs.cloudsigma.com/en/latest/)
- [CAPI Documentation](https://cluster-api.sigs.k8s.io/)

## Support

- GitHub Issues: https://github.com/shalb/kube-dc-k8-manager/issues
- Discussions: https://github.com/shalb/kube-dc-k8-manager/discussions
