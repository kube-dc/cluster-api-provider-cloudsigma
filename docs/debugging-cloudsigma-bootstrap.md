# Debugging CloudSigma Bootstrap Issues

This document describes how to investigate and debug issues with CloudSigma VM bootstrap failures, including Cepko metadata retrieval and cloud-init problems.

## Overview

CloudSigma VMs use a custom bootstrap mechanism via:
1. **Cepko** - CloudSigma's metadata service accessed via serial port (`/dev/ttyS1`)
2. **cloudsigma-bootstrap.service** - A systemd service that reads metadata and executes cloud-init style bootstrap
3. **cloud-init** - Standard cloud-init with CloudSigma datasource

## Common Issues

### 1. Cepko Returns Wrong Data Format

**Symptom:** Bootstrap fails with `AttributeError: 'str' object has no attribute 'get'`

**Cause:** Cepko returns a string (error message or raw data) instead of a dict with server context.

**Diagnosis:**
```bash
# SSH into the VM
ssh cloudsigma@<VM_IP>

# Check bootstrap log
sudo cat /var/log/cloudsigma-bootstrap.log

# Test Cepko manually (requires root)
sudo python3 << 'EOF'
import sys
sys.path.insert(0, '/usr/lib/python3/dist-packages')
from cloudinit.sources.helpers.cloudsigma import Cepko

cepko = Cepko()
result = cepko.all()
print('Result type:', type(result.result))

if isinstance(result.result, dict):
    print('Keys:', list(result.result.keys()))
    meta = result.result.get('meta', {})
    print('Meta keys:', list(meta.keys()) if meta else 'None')
    if 'cloudinit-user-data' in meta:
        print('Bootstrap data present!')
else:
    print('ERROR: Got string instead of dict')
    print('First 200 chars:', str(result.result)[:200])
EOF
```

**Expected output (working):**
```
Result type: <class 'dict'>
Keys: ['cpus_instead_of_cores', 'enable_numa', 'meta', 'nics', ...]
Meta keys: ['cluster', 'base64_fields', 'pool', 'cloudinit-user-data']
Bootstrap data present!
```

**Error output (broken):**
```
Result type: <class 'str'>
ERROR: Got string instead of dict
First 200 chars: .  \r\nGo to the 'Properties' tab of the server...
```

**Fix:** Delete and recreate the Machine resource to get a fresh VM with properly attached metadata.

### 2. cloud-init DataSource Failure

**Symptom:** cloud-init falls back to `DataSourceNone`

**Diagnosis:**
```bash
# Check cloud-init status
cloud-init status --long

# Check datasource
cat /run/cloud-init/ds-identify.log

# Check cloud-init logs for CloudSigma datasource
sudo grep -i cloudsigma /var/log/cloud-init.log
```

**Note:** On CloudSigma K8s images, the CloudSigma datasource may fail initially but `cloudsigma-bootstrap.service` handles the bootstrap via Cepko directly.

### 3. Bootstrap Service Failed

**Symptom:** Node doesn't join cluster, kubelet fails to start

**Diagnosis:**
```bash
# Check bootstrap service status
systemctl status cloudsigma-bootstrap.service

# Check full logs
sudo journalctl -u cloudsigma-bootstrap.service --no-pager

# Check bootstrap log file
sudo cat /var/log/cloudsigma-bootstrap.log

# Check if kubeadm config was created
sudo cat /run/kubeadm/kubeadm-join-config.yaml

# Check kubelet status
systemctl status kubelet
sudo journalctl -u kubelet --tail=50
```

### 4. Serial Port Permission Issues

**Symptom:** `SerialException: [Errno 13] could not open port /dev/ttyS1: Permission denied`

**Cause:** Running Cepko as non-root user

**Fix:** Always run Cepko commands with `sudo`

### 5. Missing Metadata on VM

**Symptom:** Cepko returns empty or no `cloudinit-user-data`

**Diagnosis:**
```bash
# Check all metadata
sudo python3 << 'EOF'
import sys, json
sys.path.insert(0, '/usr/lib/python3/dist-packages')
from cloudinit.sources.helpers.cloudsigma import Cepko

cepko = Cepko()
result = cepko.all()
if isinstance(result.result, dict):
    print(json.dumps(result.result.get('meta', {}), indent=2))
EOF
```

**Cause:** CAPCS controller didn't attach bootstrap data when creating the VM.

**Check CAPCS logs:**
```bash
kubectl logs -n capcs-system deployment/cloudsigma-controller-manager --tail=500 | grep -i "bootstrap\|meta\|creating"
```

## CCM (Cloud Controller Manager) Issues

### CCM Not Running

**Symptom:** Node has `node.cloudprovider.kubernetes.io/uninitialized` taint, no InternalIP

**Diagnosis:**
```bash
# Check CCM pod in management cluster
kubectl get pod -A | grep csccm

# Check CCM logs
kubectl logs -n <namespace> deployment/csccm-<cluster-name>

# Common issues:
# 1. Missing cloudsigma-credentials secret
kubectl get secret cloudsigma-credentials -n <namespace>

# 2. Missing kubeconfig secret
kubectl get secret <cluster>-cp-admin-kubeconfig -n <namespace>
```

**Fix for missing credentials:**
```bash
# Copy credentials from capcs-system namespace
kubectl get secret cloudsigma-credentials -n capcs-system -o yaml | \
  sed 's/namespace: capcs-system/namespace: <target-namespace>/' | \
  kubectl apply -f -
```

## Quick Troubleshooting Checklist

1. **Check Machine status:**
   ```bash
   kubectl get machine -n <namespace> -l cluster.x-k8s.io/cluster-name=<cluster>
   ```

2. **Check CloudSigmaMachine status:**
   ```bash
   kubectl get cloudsigmamachine -n <namespace>
   ```

3. **SSH to VM and check bootstrap:**
   ```bash
   ssh cloudsigma@<VM_IP>
   sudo cat /var/log/cloudsigma-bootstrap.log
   ```

4. **Check if kubeadm join completed:**
   ```bash
   ls -la /run/cluster-api/bootstrap-success.complete
   ```

5. **Check kubelet config exists:**
   ```bash
   ls -la /var/lib/kubelet/config.yaml
   ```

6. **Verify Cepko metadata access:**
   ```bash
   sudo python3 -c "
   import sys; sys.path.insert(0, '/usr/lib/python3/dist-packages')
   from cloudinit.sources.helpers.cloudsigma import Cepko
   c = Cepko(); r = c.all()
   print('OK' if isinstance(r.result, dict) and 'meta' in r.result else 'FAIL')
   "
   ```

## Recovery Steps

If a VM bootstrap fails:

1. **Delete the Machine resource** - MachineDeployment will create a new one:
   ```bash
   kubectl delete machine <machine-name> -n <namespace>
   ```

2. **Wait for new Machine to provision:**
   ```bash
   kubectl get machine -n <namespace> -w
   ```

3. **Verify new VM bootstrap succeeds:**
   ```bash
   ssh cloudsigma@<new-vm-ip>
   sudo cat /var/log/cloudsigma-bootstrap.log
   ```
