#!/bin/bash
set -euxo pipefail

echo "==> Installing base packages"

# Wait for apt locks
while sudo fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do
  echo "Waiting for apt lock..."
  sleep 2
done

# Update package list
sudo apt-get update

# Install required packages
sudo apt-get install -y \
  apt-transport-https \
  ca-certificates \
  curl \
  gnupg \
  lsb-release \
  software-properties-common \
  socat \
  conntrack \
  ipset \
  ebtables \
  ethtool \
  nfs-common \
  open-iscsi \
  util-linux

# Disable swap
sudo swapoff -a
sudo sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab

echo "==> Base packages installed"
