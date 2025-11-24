# Packer template for building Kubernetes image directly on CloudSigma
# This creates the image on CloudSigma infrastructure (no local build, no upload needed)

variable "k8s_version" {
  type    = string
  default = "1.34.1"
}

variable "ubuntu_version" {
  type    = string
  default = "24.04"
}

variable "cloudsigma_username" {
  type      = string
  default   = env("CLOUDSIGMA_USERNAME")
  sensitive = true
}

variable "cloudsigma_password" {
  type      = string
  default   = env("CLOUDSIGMA_PASSWORD")
  sensitive = true
}

variable "cloudsigma_region" {
  type    = string
  default = env("CLOUDSIGMA_REGION")
}

# Ubuntu 24.04 base drive UUID in CloudSigma
# You need to find this in your CloudSigma account or use a public Ubuntu drive
variable "ubuntu_base_drive" {
  type    = string
  default = "" # Set this to your Ubuntu 24.04 base drive UUID
}

source "cloudsigma" "ubuntu-k8s" {
  username = var.cloudsigma_username
  password = var.cloudsigma_password
  location = var.cloudsigma_region
  
  # VM configuration
  server_name = "packer-k8s-builder-${var.k8s_version}"
  cpu         = 2000  # 2 GHz
  memory      = 4096  # 4 GB
  vnc_password = "packer"
  
  # Use Ubuntu base drive
  # Option 1: Clone from existing drive
  clone_drive_uuid = var.ubuntu_base_drive
  
  # Option 2: Or specify a library drive
  # drive_template = "ubuntu-24.04"
  
  # Network - public IP with DHCP
  network_interfaces = [{
    ip_v4_conf = {
      conf = "dhcp"
    }
  }]
  
  # SSH configuration
  ssh_username = "ubuntu"
  ssh_timeout  = "10m"
  
  # Output drive name
  drive_name = "ubuntu-${var.ubuntu_version}-k8s-${var.k8s_version}"
}

build {
  sources = ["source.cloudsigma.ubuntu-k8s"]

  # Wait for cloud-init to finish
  provisioner "shell" {
    inline = [
      "echo 'Waiting for cloud-init...'",
      "cloud-init status --wait || true",
      "echo 'Cloud-init finished'"
    ]
  }

  # Install base packages
  provisioner "shell" {
    script = "scripts/01-base-packages.sh"
  }

  # Install containerd
  provisioner "shell" {
    script = "scripts/02-install-containerd.sh"
  }

  # Install Kubernetes
  provisioner "shell" {
    environment_vars = [
      "K8S_VERSION=${var.k8s_version}"
    ]
    script = "scripts/03-install-kubernetes.sh"
  }

  # Configure kernel modules and sysctl
  provisioner "shell" {
    script = "scripts/04-configure-system.sh"
  }

  # Cleanup
  provisioner "shell" {
    script = "scripts/05-cleanup.sh"
  }

  # Post-processor to display the resulting drive UUID
  post-processor "manifest" {
    output     = "manifest-cloudsigma.json"
    strip_path = true
  }
}
