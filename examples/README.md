# CloudSigma Cluster API Provider - Examples

This directory contains example configurations for deploying Kubernetes clusters with CloudSigma workers using Cluster API.

## Prerequisites

1. **Management Cluster** with Cluster API installed
2. **cluster-api-provider-cloudsigma** deployed to management cluster
3. **CloudSigma credentials** configured
4. **Working image** with Kubernetes pre-installed

## Current Working Image

**UUID:** `4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79`

**Specifications:**
- OS: Ubuntu 24.04.2 LTS
- Kubernetes: 1.34.1
- Container Runtime: containerd 1.7.28
- Cloud-init: 25.1.4
- Bootstrap: Custom Cepko-based service (automatic node join)
- Login: `cloudsigma` / `Cloud2025`

**Features:**
- ✅ Automatic kubeadm join on boot (~7 seconds)
- ✅ Swap disabled
- ✅ SSH password authentication enabled
- ✅ Proper hostname and machine-id cleanup
- ✅ Bootstrap logs: `/var/log/cloudsigma-bootstrap.log`

## Examples

### 1. Simple CloudSigma Cluster
**File:** `cloudsigma-cluster-simple.yaml`

Basic cluster with 2 CloudSigma workers using NAT public IPs.

```bash
kubectl apply -f cloudsigma-cluster-simple.yaml
```

**Configuration:**
- 2 replicas
- 2 GHz CPU, 4 GB RAM
- 10 GB disk
- Public NAT IP (no VLAN)
- Kubernetes 1.34.0

### 2. CloudSigma Cluster with Private VLAN
**File:** `cloudsigma-cluster-with-vlan.yaml`

Production-ready cluster with 3 workers on a private VLAN.

```bash
# Edit the file and replace YOUR-VLAN-UUID-HERE
kubectl apply -f cloudsigma-cluster-with-vlan.yaml
```

**Configuration:**
- 3 replicas
- 4 GHz CPU, 8 GB RAM
- 20 GB disk
- Private VLAN with DHCP
- Production labels and taints

### 3. Multi-Pool Cluster (CloudSigma + Others)
**File:** `multi-pool-cloudsigma-workers.yaml`

Add CloudSigma workers to an existing multi-provider cluster.

```bash
kubectl apply -f multi-pool-cloudsigma-workers.yaml
```

**Configuration:**
- 1 replica (scale as needed)
- 2 GHz CPU, 4 GB RAM
- Public NAT IP
- Compatible with mixed infrastructure deployments

### 4. Multi-Pool with VLAN
**File:** `multi-pool-cloudsigma-workers-vlan.yaml`

CloudSigma workers with VLAN for existing cluster.

```bash
# Edit VLAN UUID before applying
kubectl apply -f multi-pool-cloudsigma-workers-vlan.yaml
```

## Customization Guide

### Adjusting Resources

```yaml
spec:
  template:
    spec:
      cpu: 4000          # CPU in MHz (4000 = 4 GHz)
      memory: 8192       # Memory in MB (8192 = 8 GB)
      disks:
        - size: 21474836480  # Disk size in bytes (21474836480 = 20 GB)
```

### Changing Replica Count

```yaml
spec:
  replicas: 5  # Number of worker nodes
```

### Adding Custom Labels

```yaml
template:
  metadata:
    labels:
      workload-type: gpu
      cost-center: engineering
      environment: staging
```

### Adding Taints

```yaml
spec:
  template:
    spec:
      joinConfiguration:
        nodeRegistration:
          taints:
            - key: workload
              value: gpu
              effect: NoSchedule
```

### Using Different Image

```yaml
disks:
  - uuid: "YOUR-IMAGE-UUID"  # Replace with your custom image
```

## Networking Options

### Option 1: Public NAT IP (Default)

```yaml
spec:
  template:
    spec:
      # NICs omitted - CloudSigma auto-assigns public NAT IP
```

**Use when:**
- Testing or development
- Workers need direct internet access
- Simplicity is preferred

### Option 2: Private VLAN

```yaml
spec:
  template:
    spec:
      nics:
        - vlan: "013ee207-5234-4f7b-8dd1-d06050b9fd38"  # Your VLAN UUID
          ipv4_conf:
            conf: "dhcp"
```

**Use when:**
- Production deployments
- Network isolation required
- Integration with existing infrastructure

## Verification

### Check MachineDeployment

```bash
kubectl get machinedeployment -n <namespace>
```

### Check Machines

```bash
kubectl get machine -n <namespace>
```

### Check CloudSigma VMs

```bash
kubectl get cloudsigmamachine -n <namespace>
```

### Check Nodes in Tenant Cluster

```bash
export KUBECONFIG=/path/to/tenant/kubeconfig
kubectl get nodes
```

### Debug Bootstrap Issues

SSH into worker VM:
```bash
ssh cloudsigma@<VM-IP>
# Password: Cloud2025

# Check bootstrap service
sudo systemctl status cloudsigma-bootstrap.service

# View bootstrap logs
sudo cat /var/log/cloudsigma-bootstrap.log

# Check if bootstrap completed
ls -la /run/cluster-api/bootstrap-success.complete

# Check kubelet
sudo systemctl status kubelet
sudo journalctl -u kubelet -n 50
```

## Common Issues

### Nodes NotReady

**Cause:** Missing CNI (Container Network Interface)

**Solution:** Install CNI plugin (Cilium, Calico, Flannel) in the tenant cluster.

**Note:** This is **NOT** an image or bootstrap issue if nodes join the cluster successfully.

### MachineDeployment Stuck "ScalingUp"

**Cause:** Waiting for nodes to become Ready (usually due to missing CNI or CCM)

**Solution:** 
1. Install CNI plugin
2. Deploy CloudSigma Cloud Controller Manager (CCM)

See: `../docs/ccm-deployment.md`

### Bootstrap Failed

**Symptoms:** Node doesn't appear in cluster after 2-3 minutes

**Debug:**
1. SSH into VM and check bootstrap log
2. Verify metadata is set correctly in CloudSigmaMachine
3. Check cloud-init logs: `sudo cat /var/log/cloud-init.log`

## Image Building

To build a new image with custom Kubernetes version:

```bash
cd images/ubuntu-k8s
./build-on-cloudsigma.sh
```

See: `images/ubuntu-k8s/README-build-methods.md`

## Next Steps

1. **Deploy CCM** - For node IP addresses and proper initialization
   - See: `docs/ccm-deployment.md`

2. **Install CNI** - For pod networking
   - Cilium, Calico, or Flannel

3. **Scale Workers** - Adjust replicas in MachineDeployment

4. **Monitor** - Watch node joins and cluster health

## References

- [Cluster API Documentation](https://cluster-api.sigs.k8s.io/)
- [CloudSigma API Docs](https://docs.cloudsigma.com/)
- [Provider Documentation](../README.md)
