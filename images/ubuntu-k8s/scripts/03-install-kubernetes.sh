#!/bin/bash
set -euxo pipefail

echo "==> Installing Kubernetes ${K8S_VERSION}"

# Add Kubernetes apt repository
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.34/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg

echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.34/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list

# Update package list
sudo apt-get update

# Install Kubernetes components
sudo apt-get install -y \
  kubelet=${K8S_VERSION}-1.1 \
  kubeadm=${K8S_VERSION}-1.1 \
  kubectl=${K8S_VERSION}-1.1

# Hold Kubernetes packages at current version
sudo apt-mark hold kubelet kubeadm kubectl

# Enable kubelet (but don't start it yet - will start on first boot with kubeadm join)
sudo systemctl enable kubelet

# Pre-pull common images to speed up node join
sudo kubeadm config images pull --kubernetes-version=${K8S_VERSION}

echo "==> Kubernetes ${K8S_VERSION} installed"
