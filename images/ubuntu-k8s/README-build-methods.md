# Ubuntu Kubernetes Image Build Methods

## Current Method: CloudSigma Native Build ✅

**Script:** `build-on-cloudsigma.sh`

**How it works:**
1. Clones Ubuntu 24.04 base drive on CloudSigma
2. Creates a VM from the cloned drive
3. SSH into VM and runs provisioning scripts
4. Installs Kubernetes and dependencies
5. Runs cleanup and installs bootstrap service
6. Stops VM and creates final image

**Bootstrap Method:**
- Custom systemd service: `cloudsigma-bootstrap.service`
- Script: `/usr/local/bin/cloudsigma-bootstrap.sh`
- Uses Cepko library to read CloudSigma metadata
- Bypasses cloud-init datasource issues

**Provisioning Scripts (executed in order):**
- `01-base-packages.sh` - Base system packages
- `02-install-containerd.sh` - Container runtime
- `03-install-kubernetes.sh` - Kubernetes components
- `04-configure-system.sh` - System configuration
- `05-cleanup.sh` - Cleanup + install bootstrap service

---

## Unused/Legacy Build Methods

### cloud-init/ directory
- **Purpose:** Local QEMU/KVM builds
- **Status:** NOT USED in CloudSigma native builds
- **Files:** `user-data`, `meta-data`
- **Note:** Kept for reference/local testing only

### http/ directory
- **Purpose:** ISO-based autoinstall builds
- **Status:** NOT USED in CloudSigma native builds
- **Files:** `user-data`, `meta-data`
- **Note:** Kept for reference only

### upload-to-cloudsigma.sh
- **Purpose:** Upload pre-built image to CloudSigma
- **Status:** NOT USED (we build directly on CloudSigma)
- **Note:** May be useful for uploading externally built images

---

## Final Image Details

**Current Image UUID:** `569991f2-c96f-443c-88dd-72d1e53bf090`

**Included:**
- Ubuntu 24.04.2 LTS
- Kubernetes 1.34.1
- Containerd 1.7.28
- Cloud-init 25.1.4
- Custom bootstrap service

**Bootstrap Features:**
- ✅ Automatic kubeadm join on boot
- ✅ Reads CloudSigma metadata via Cepko
- ✅ Renders Jinja templates
- ✅ Disables swap before kubelet
- ✅ Creates bootstrap success marker
- ✅ Logs to `/var/log/cloudsigma-bootstrap.log`

**Login:**
- User: `cloudsigma`
- Password: `Cloud2025`
- SSH password auth: enabled
