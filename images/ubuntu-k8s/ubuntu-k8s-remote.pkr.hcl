# Packer template for provisioning a remote CloudSigma VM
# This is used by build-on-cloudsigma.sh script

variable "vm_ip" {
  type = string
  description = "IP address of the CloudSigma VM to provision"
}

variable "k8s_version" {
  type    = string
  default = "1.34.1"
}

source "null" "cloudsigma-vm" {
  communicator = "ssh"
  ssh_host     = var.vm_ip
  ssh_username = "cloudsigma"  # CloudSigma library drives use 'cloudsigma' user
  ssh_timeout  = "10m"
  # Use SSH key file
  ssh_private_key_file = pathexpand("~/.ssh/id_ed25519")
}

build {
  sources = ["source.null.cloudsigma-vm"]

  # Wait for cloud-init to finish
  provisioner "shell" {
    inline = [
      "echo 'Waiting for cloud-init...'",
      "sudo cloud-init status --wait || true",
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
}
