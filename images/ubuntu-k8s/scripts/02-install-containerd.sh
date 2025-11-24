#!/bin/bash
set -euxo pipefail

echo "==> Installing containerd"

# Install containerd
sudo apt-get update
sudo apt-get install -y containerd

# Create containerd configuration directory
sudo mkdir -p /etc/containerd

# Generate default containerd configuration
containerd config default | sudo tee /etc/containerd/config.toml

# Configure containerd to use systemd cgroup driver
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

# Enable and start containerd
sudo systemctl enable containerd
sudo systemctl restart containerd

# Verify containerd is running
sudo systemctl status containerd --no-pager

echo "==> Containerd installed and configured"
