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

- [ ] KubeDC management cluster installed
- [ ] CloudSigma account with API access
- [ ] CAPCS provider installed (upcoming)
- [ ] Worker node images prepared

## Step-by-Step Guide

### Step 1: Install CAPCS Provider

```bash
# Install CloudSigma CAPI provider
kubectl apply -f https://github.com/shalb/cluster-api-provider-cloudsigma/releases/latest/download/infrastructure-components.yaml
```

### Step 2: Configure CloudSigma Credentials

```bash
# Create secret with CloudSigma API credentials
kubectl create secret generic cloudsigma-credentials \
  --namespace=kube-system \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-api-password' \
  --from-literal=region='zrh'
```

### Step 3: Prepare Worker Image

Option A: Use pre-built image
```bash
# Get image UUID from CloudSigma library
CLOUDSIGMA_IMAGE_UUID="<your-image-uuid>"
```

Option B: Build custom image
```bash
# Clone image builder
git clone https://github.com/shalb/cloudsigma-k8s-images
cd cloudsigma-k8s-images

# Build image
packer build -var 'cloudsigma_username=user' \
             -var 'cloudsigma_password=pass' \
             -var 'k8s_version=1.34.0' \
             cloudsigma-k8s-worker.pkr.hcl
```

### Step 4: Create ConfigMap with Image UUID

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cloudsigma-images
  namespace: kube-system
data:
  ubuntu-22.04-k8s-1.34.0: "your-image-uuid-here"
```

```bash
kubectl apply -f cloudsigma-images-configmap.yaml
```

### Step 5: Create KdcCluster with CloudSigma Workers

```yaml
# cluster-with-cloudsigma.yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  version: v1.34.0
  
  controlPlane:
    replicas: 2
  
  dataStore:
    dedicated: true
    eipName: default-gw
    port: 32380
  
  network:
    serviceCIDR: 10.96.0.0/12
    podCIDR: 10.220.0.0/16
  
  eip:
    create: true
    externalNetworkType: public
  
  enableClusterAPI: true
  
  workers:
    - name: cloudsigma-workers
      replicas: 3
      infrastructureProvider: cloudsigma
      cloudsigma:
        cpu: 2000
        memory: 4096
        diskSize: 50
        imageUUID: "your-image-uuid-here"
        region: "zrh"
        tags:
          - kubernetes
          - production
      labels:
        provider: cloudsigma
```

```bash
kubectl apply -f cluster-with-cloudsigma.yaml
```

### Step 6: Monitor Cluster Creation

```bash
# Watch cluster status
kubectl get kdccluster my-cluster -w

# Check CAPI resources
kubectl get cluster,machinedeployment,machines

# Check CloudSigma machines
kubectl get cloudsigmamachines

# View events
kubectl get events --sort-by='.lastTimestamp'
```

### Step 7: Access Tenant Cluster

```bash
# Get kubeconfig
kubectl get secret my-cluster-cp-admin-kubeconfig \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > my-cluster-kubeconfig.yaml

# Set kubeconfig
export KUBECONFIG=my-cluster-kubeconfig.yaml

# Verify nodes
kubectl get nodes

# Expected output:
# NAME                              STATUS   ROLES    AGE   VERSION
# my-cluster-cloudsigma-workers-0   Ready    <none>   5m    v1.34.0
# my-cluster-cloudsigma-workers-1   Ready    <none>   5m    v1.34.0
# my-cluster-cloudsigma-workers-2   Ready    <none>   5m    v1.34.0
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

### Workers not provisioning

```bash
# Check CloudSigmaMachine status
kubectl describe cloudsigmamachine <name>

# Check CAPCS provider logs
kubectl logs -n capcs-system deployment/capcs-controller-manager

# Verify credentials
kubectl get secret cloudsigma-credentials -n kube-system -o yaml
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
