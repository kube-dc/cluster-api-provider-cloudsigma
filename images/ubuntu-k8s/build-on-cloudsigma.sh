#!/bin/bash
set -euo pipefail

echo "==> Building Kubernetes image directly on CloudSigma"

# Check required environment variables
if [ -z "${CLOUDSIGMA_USERNAME:-}" ] || [ -z "${CLOUDSIGMA_PASSWORD:-}" ] || [ -z "${CLOUDSIGMA_REGION:-}" ]; then
  echo "Error: CLOUDSIGMA_USERNAME, CLOUDSIGMA_PASSWORD, and CLOUDSIGMA_REGION must be set"
  exit 1
fi

K8S_VERSION="${K8S_VERSION:-1.34.1}"
UBUNTU_VERSION="${UBUNTU_VERSION:-24.04}"
API_BASE="https://${CLOUDSIGMA_REGION}.cloudsigma.com/api/2.0"

echo "Kubernetes version: ${K8S_VERSION}"
echo "Ubuntu version: ${UBUNTU_VERSION}"

# Step 1: Find Ubuntu base drive
echo ""
if [ -n "${UBUNTU_DRIVE_UUID:-}" ]; then
  echo "==> Using specified base drive: ${UBUNTU_DRIVE_UUID}"
else
  echo "==> Finding Ubuntu ${UBUNTU_VERSION} base drive..."
  UBUNTU_DRIVE_UUID=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
    "${API_BASE}/libdrives/?limit=100" | \
    python3 -c "import sys, json; drives = json.load(sys.stdin)['objects']; ubuntu = [d for d in drives if '${UBUNTU_VERSION}' in d.get('name', '') and 'lts' in d.get('name', '').lower() and 'x86_64' in d.get('arch', '') and 'install' not in d.get('name', '').lower()]; print(ubuntu[0]['uuid'] if ubuntu else '')" || echo "")

  if [ -z "${UBUNTU_DRIVE_UUID}" ]; then
    echo "Error: Could not find Ubuntu ${UBUNTU_VERSION} base drive in library"
    echo "Please check CloudSigma library drives or set UBUNTU_DRIVE_UUID environment variable"
    echo ""
    echo "Available Ubuntu drives:"
    curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" "${API_BASE}/libdrives/?limit=20&os=linux" | \
      python3 -c "import sys, json; drives = json.load(sys.stdin)['objects']; [print(f\"  {d['name']} - UUID: {d['uuid']}\") for d in drives if 'ubuntu' in d.get('name', '').lower()]" 2>/dev/null || true
    exit 1
  fi
fi

echo "✅ Using base drive: ${UBUNTU_DRIVE_UUID}"

# Step 2: Clone the base drive
echo ""
echo "==> Cloning Ubuntu base drive..."
BUILD_DRIVE_NAME="k8s-build-${K8S_VERSION}-$(date +%s)"
CLONE_RESPONSE=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
  -X POST \
  -H "Content-Type: application/json" \
  "${API_BASE}/drives/${UBUNTU_DRIVE_UUID}/action/?do=clone" \
  -d "{\"name\": \"${BUILD_DRIVE_NAME}\"}")

BUILD_DRIVE_UUID=$(echo "${CLONE_RESPONSE}" | python3 -c "import sys, json; print(json.load(sys.stdin)['objects'][0]['uuid'])" 2>/dev/null || echo "")

if [ -z "${BUILD_DRIVE_UUID}" ]; then
  echo "Error: Failed to clone drive"
  echo "Response: ${CLONE_RESPONSE}"
  exit 1
fi

echo "✅ Build drive created: ${BUILD_DRIVE_UUID}"

# Wait for clone to complete
echo "Waiting for drive clone to complete..."
for i in {1..60}; do
  DRIVE_STATUS=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
    "${API_BASE}/drives/${BUILD_DRIVE_UUID}/" | \
    python3 -c "import sys, json; print(json.load(sys.stdin)['status'])" 2>/dev/null || echo "unknown")
  
  if [ "${DRIVE_STATUS}" = "unmounted" ]; then
    echo "✅ Drive clone complete"
    break
  fi
  
  echo "  Drive status: ${DRIVE_STATUS} (waiting...)"
  sleep 5
done

# Step 3: Find SSH key in CloudSigma
echo ""
echo "==> Finding SSH key..."
if [ -n "${SSH_KEY_UUID:-}" ]; then
  echo "Using specified SSH key UUID: ${SSH_KEY_UUID}"
else
  # Try to find voa@kube-dc-bastion key
  SSH_KEY_UUID=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
    "${API_BASE}/keypairs/" | \
    python3 -c "import sys, json; keys = json.load(sys.stdin)['objects']; matching = [k for k in keys if 'voa@kube-dc-bastion' in k.get('name', '') or 'voa' in k.get('name', '')]; print(matching[0]['uuid'] if matching else '')" 2>/dev/null || echo "")
  
  if [ -z "${SSH_KEY_UUID}" ]; then
    echo "Warning: No SSH key found in CloudSigma account"
    echo "You can specify SSH_KEY_UUID environment variable"
  else
    echo "Found SSH key: ${SSH_KEY_UUID}"
  fi
fi

# Step 4: Create build VM with SSH key attached
echo ""
echo "==> Creating build VM..."
VM_NAME="packer-k8s-builder-$(date +%s)"

# Build the server JSON with SSH key if available
if [ -n "${SSH_KEY_UUID}" ]; then
  SERVER_JSON="{
    \"objects\": [{
      \"name\": \"${VM_NAME}\",
      \"cpu\": 2000,
      \"mem\": 4294967296,
      \"vnc_password\": \"packer123\",
      \"pubkeys\": [
        {\"uuid\": \"${SSH_KEY_UUID}\"}
      ],
      \"drives\": [{
        \"boot_order\": 1,
        \"dev_channel\": \"0:0\",
        \"device\": \"virtio\",
        \"drive\": \"${BUILD_DRIVE_UUID}\"
      }],
      \"nics\": [{
        \"ip_v4_conf\": {
          \"conf\": \"dhcp\"
        }
      }]
    }]
  }"
else
  # No SSH key - will need password auth
  SERVER_JSON="{
    \"objects\": [{
      \"name\": \"${VM_NAME}\",
      \"cpu\": 2000,
      \"mem\": 4294967296,
      \"vnc_password\": \"packer123\",
      \"drives\": [{
        \"boot_order\": 1,
        \"dev_channel\": \"0:0\",
        \"device\": \"virtio\",
        \"drive\": \"${BUILD_DRIVE_UUID}\"
      }],
      \"nics\": [{
        \"ip_v4_conf\": {
          \"conf\": \"dhcp\"
        }
      }]
    }]
  }"
fi

VM_CREATE_RESPONSE=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
  -H "Content-Type: application/json" \
  -d "${SERVER_JSON}" \
  "${API_BASE}/servers/")

VM_UUID=$(echo "${VM_CREATE_RESPONSE}" | python3 -c "import sys, json; print(json.load(sys.stdin)['objects'][0]['uuid'])" 2>/dev/null || echo "")

if [ -z "${VM_UUID}" ]; then
  echo "Error: Failed to create VM"
  echo "Response: ${VM_CREATE_RESPONSE}"
  curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" -X DELETE "${API_BASE}/drives/${BUILD_DRIVE_UUID}/"
  exit 1
fi

echo "✅ VM created: ${VM_UUID}"

# Step 4: Start the VM
echo ""
echo "==> Starting VM..."
curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
  -X POST \
  "${API_BASE}/servers/${VM_UUID}/action/?do=start" > /dev/null

echo "Waiting for VM to boot and get IP..."
VM_IP=""
for i in {1..60}; do
  VM_INFO=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
    "${API_BASE}/servers/${VM_UUID}/")
  
  VM_IP=$(echo "${VM_INFO}" | python3 -c "import sys, json; vm=json.load(sys.stdin); nics=vm.get('runtime', {}).get('nics', []); print(nics[0]['ip_v4']['uuid'] if nics and 'ip_v4' in nics[0] else '')" 2>/dev/null || echo "")
  
  if [ -n "${VM_IP}" ]; then
    # The uuid field IS the IP address for CloudSigma public IPs
    echo "✅ VM IP: ${VM_IP}"
    break
  fi
  
  echo "  Waiting for IP... (${i}/60)"
  sleep 5
done

if [ -z "${VM_IP}" ]; then
  echo "Error: VM did not get an IP address"
  # Cleanup
  curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" -X POST "${API_BASE}/servers/${VM_UUID}/action/?do=stop" > /dev/null
  sleep 10
  curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" -X DELETE "${API_BASE}/servers/${VM_UUID}/" > /dev/null
  curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" -X DELETE "${API_BASE}/drives/${BUILD_DRIVE_UUID}/" > /dev/null
  exit 1
fi

# Step 5: Wait for SSH
echo ""
echo "==> Waiting for SSH..."
for i in {1..60}; do
  if timeout 5 ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 cloudsigma@${VM_IP} "echo 'SSH ready'" 2>/dev/null; then
    echo "✅ SSH is ready"
    break
  fi
  echo "  Waiting for SSH... (${i}/60)"
  sleep 5
done

# Step 6: Run Packer with null builder
echo ""
echo "==> Running Packer provisioning..."
echo "VM_IP=${VM_IP}" > /tmp/packer-vars.env
packer build \
  -var "vm_ip=${VM_IP}" \
  -var "k8s_version=${K8S_VERSION}" \
  ubuntu-k8s-remote.pkr.hcl

# Step 7: Stop VM and clone the drive
echo ""
echo "==> Stopping VM..."
curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
  -X POST \
  "${API_BASE}/servers/${VM_UUID}/action/?do=stop" > /dev/null

echo "Waiting for VM to stop..."
for i in {1..30}; do
  VM_STATUS=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
    "${API_BASE}/servers/${VM_UUID}/" | \
    python3 -c "import sys, json; print(json.load(sys.stdin)['status'])" 2>/dev/null || echo "unknown")
  
  if [ "${VM_STATUS}" = "stopped" ]; then
    echo "✅ VM stopped"
    break
  fi
  
  echo "  VM status: ${VM_STATUS} (waiting...)"
  sleep 5
done

# Step 8: Clone the provisioned drive as final image
echo ""
echo "==> Creating final image..."
FINAL_IMAGE_NAME="ubuntu-${UBUNTU_VERSION}-k8s-${K8S_VERSION}"
FINAL_CLONE_RESPONSE=$(curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
  -X POST \
  -H "Content-Type: application/json" \
  "${API_BASE}/drives/${BUILD_DRIVE_UUID}/action/?do=clone" \
  -d "{\"name\": \"${FINAL_IMAGE_NAME}\"}")

FINAL_DRIVE_UUID=$(echo "${FINAL_CLONE_RESPONSE}" | python3 -c "import sys, json; print(json.load(sys.stdin)['objects'][0]['uuid'])" 2>/dev/null || echo "")

if [ -z "${FINAL_DRIVE_UUID}" ]; then
  echo "Error: Failed to create final image"
  echo "Response: ${FINAL_CLONE_RESPONSE}"
else
  echo "✅ Final image created: ${FINAL_DRIVE_UUID}"
  echo ""
  echo "======================================"
  echo "Image Name: ${FINAL_IMAGE_NAME}"
  echo "Drive UUID: ${FINAL_DRIVE_UUID}"
  echo "======================================"
  echo ""
  echo "Use this UUID in your CloudSigmaMachineTemplate:"
  echo "  disks:"
  echo "    - uuid: \"${FINAL_DRIVE_UUID}\""
fi

# Step 9: Cleanup
echo ""
echo "==> Cleaning up..."
curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" -X DELETE "${API_BASE}/servers/${VM_UUID}/" > /dev/null
curl -s -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" -X DELETE "${API_BASE}/drives/${BUILD_DRIVE_UUID}/" > /dev/null

echo "✅ Cleanup complete"
echo ""
echo "🎉 Build complete! Your Kubernetes image is ready to use."
