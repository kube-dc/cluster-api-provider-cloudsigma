# Ubuntu Kubernetes Image Builder for CloudSigma

This directory contains Packer configuration to build Ubuntu 24.04 images with Kubernetes pre-installed for use with CloudSigma Cluster API Provider.

## Prerequisites

1. **Packer** - Install from https://www.packer.io/downloads
   ```bash
   # Ubuntu/Debian
   wget -O- https://apt.releases.hashicorp.com/gpg | sudo gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
   echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" | sudo tee /etc/apt/sources.list.d/hashicorp.list
   sudo apt update && sudo apt install packer
   ```

2. **QEMU/KVM** - For building the image locally
   ```bash
   sudo apt install qemu-kvm libvirt-daemon-system
   ```

3. **CloudSigma Credentials** - Set environment variables
   ```bash
   export CLOUDSIGMA_USERNAME="your-email@example.com"
   export CLOUDSIGMA_PASSWORD="your-password"
   export CLOUDSIGMA_REGION="next"  # or your region
   ```

## Quick Start

### Option 1: Build Directly on CloudSigma (Recommended ✅)

This method builds the image on a CloudSigma VM - no upload needed!

```bash
# Navigate to the image directory
cd images/ubuntu-k8s

# Build on CloudSigma infrastructure
make build-on-cloudsigma K8S_VERSION=1.34.1
```

**Benefits:**
- ✅ No file upload required (image is already in CloudSigma)
- ✅ Faster - no download/upload overhead
- ✅ Works around CloudSigma API upload size limits
- ✅ Uses your CloudSigma resources efficiently

### Option 2: Build Locally with QEMU

This method builds locally and requires manual upload via CloudSigma UI.

```bash
# Navigate to the image directory
cd images/ubuntu-k8s

# Initialize Packer
make init

# Validate the configuration
make validate

# Build the image locally
make build

# Upload manually via CloudSigma Web UI:
# 1. Go to https://next.cloudsigma.com
# 2. Navigate to Drives → Upload
# 3. Upload output-ubuntu-k8s/packer-ubuntu-k8s
```

**Note:** Local build creates ~10GB image which must be manually uploaded via CloudSigma Web UI due to API size limits.

## Configuration

### Kubernetes Version

Specify the Kubernetes version to install:

```bash
make build K8S_VERSION=1.34.1
```

### Ubuntu Version

Specify the Ubuntu version:

```bash
make build UBUNTU_VERSION=24.04
```

## What's Included

The image includes:

- ✅ Ubuntu 24.04 LTS (or specified version)
- ✅ Containerd (container runtime)
- ✅ Kubernetes components (kubelet, kubeadm, kubectl)
- ✅ CNI plugins
- ✅ Required kernel modules and sysctl configuration
- ✅ Cloud-init configured for CAPI
- ✅ Common Kubernetes images pre-pulled

## Image Build Process

1. **Base Installation**: Ubuntu 24.04 server with minimal packages
2. **Base Packages**: Install required system packages
3. **Containerd**: Install and configure container runtime
4. **Kubernetes**: Install kubelet, kubeadm, kubectl
5. **System Configuration**: Configure kernel modules, sysctl, etc.
6. **Cleanup**: Remove unnecessary files and prepare for cloning
7. **Upload**: Upload to CloudSigma as a drive

## Using the Image

After building, the image will be available in CloudSigma as a drive. You can use its UUID in your `CloudSigmaMachineTemplate`:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaMachineTemplate
metadata:
  name: worker-template
spec:
  template:
    spec:
      disks:
        - uuid: "<your-image-uuid>"  # Use the UUID from build output
          device: "virtio"
          boot_order: 1
          size: 10737418240  # 10 GB
```

## Troubleshooting

### Build fails during provisioning

Check the packer logs for details:
```bash
packer build -debug ubuntu-k8s.pkr.hcl
```

### Upload to CloudSigma fails

Verify your credentials:
```bash
echo $CLOUDSIGMA_USERNAME
echo $CLOUDSIGMA_REGION
```

### QEMU/KVM acceleration not available

Ensure KVM is installed and your user has access:
```bash
sudo usermod -aG kvm $USER
newgrp kvm
```

## Directory Structure

```
images/ubuntu-k8s/
├── ubuntu-k8s.pkr.hcl            # Packer configuration (local QEMU build)
├── ubuntu-k8s-remote.pkr.hcl     # Packer configuration (remote CloudSigma build)
├── build-on-cloudsigma.sh        # Script to orchestrate CloudSigma build
├── Makefile                      # Build automation
├── README.md                     # This file
├── cloud-init/                   # Cloud-init files for cloud image
│   ├── user-data                 # Cloud-init user configuration
│   └── meta-data                 # Cloud-init metadata
└── scripts/                      # Provisioning scripts (shared by both methods)
    ├── 01-base-packages.sh       # Install base packages
    ├── 02-install-containerd.sh  # Install containerd
    ├── 03-install-kubernetes.sh  # Install Kubernetes
    ├── 04-configure-system.sh    # System configuration
    ├── 05-cleanup.sh             # Cleanup before upload
    └── upload-to-cloudsigma.sh   # Upload to CloudSigma API (not used by CloudSigma build)
```

## Customization

### Add Custom Packages

Edit `scripts/01-base-packages.sh` to add additional packages:

```bash
sudo apt-get install -y \
  your-package-here
```

### Pre-pull Additional Images

Edit `scripts/03-install-kubernetes.sh` to pre-pull specific images:

```bash
sudo crictl pull your-registry/your-image:tag
```

### Modify Kubernetes Configuration

Edit `scripts/04-configure-system.sh` to adjust kubelet or system settings.

## Clean Up

Remove build artifacts:

```bash
make clean
```

## Support

For issues related to:
- **Packer**: https://github.com/hashicorp/packer/issues
- **CloudSigma API**: https://cloudsigma.com/support/
- **CAPCS Provider**: https://github.com/kube-dc/cluster-api-provider-cloudsigma
