#!/bin/bash
set -e

# Release script for cluster-api-provider-cloudsigma + CCM + CSI
# Usage: ./hack/release.sh v0.1.0

REGISTRY_REPO="shalb"
PROVIDER_IMAGE="cluster-api-provider-cloudsigma"
CCM_IMAGE="cloudsigma-ccm"
CSI_CONTROLLER_IMAGE="cloudsigma-csi-controller"
CSI_NODE_IMAGE="cloudsigma-csi-node"

if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  echo "Example: $0 v0.1.0"
  exit 1
fi

VERSION=$1

# Validate version format
if [[ ! $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Error: Version must be in format vX.Y.Z (e.g., v0.1.0)"
  exit 1
fi

echo "=== Release: ${VERSION} ==="
echo "Registry: ${REGISTRY_REPO}"
echo "Images to build:"
echo "  - ${PROVIDER_IMAGE}:${VERSION}"
echo "  - ${CCM_IMAGE}:${VERSION}"
echo "  - ${CSI_CONTROLLER_IMAGE}:${VERSION}"
echo "  - ${CSI_NODE_IMAGE}:${VERSION}"
echo ""

read -p "Proceed with release? (y/n): " confirm
if [[ ! $confirm =~ ^[Yy] ]]; then
  echo "Aborted."
  exit 0
fi

path=$(dirname -- "$( readlink -f -- "$0"; )")
rootPath=$(cd -- "${path}/../" &> /dev/null && pwd)
cd "${rootPath}"

# Step 1: Clean
echo ""
echo "=== Step 1/5: Cleaning ==="
make clean

# Step 2: Build and Push Provider
echo ""
echo "=== Step 2/5: Building & Pushing Provider ==="
make docker-build IMG=${REGISTRY_REPO}/${PROVIDER_IMAGE}:${VERSION}
make docker-build-latest IMG=${REGISTRY_REPO}/${PROVIDER_IMAGE}:${VERSION} IMG_LATEST=${REGISTRY_REPO}/${PROVIDER_IMAGE}:latest
docker push ${REGISTRY_REPO}/${PROVIDER_IMAGE}:${VERSION}
docker push ${REGISTRY_REPO}/${PROVIDER_IMAGE}:latest

# Step 3: Build and Push CCM
echo ""
echo "=== Step 3/5: Building & Pushing CCM ==="
echo "Building CCM..."
docker build -t ${REGISTRY_REPO}/${CCM_IMAGE}:${VERSION} -f ccm/Dockerfile .
docker tag ${REGISTRY_REPO}/${CCM_IMAGE}:${VERSION} ${REGISTRY_REPO}/${CCM_IMAGE}:latest
echo "Pushing CCM..."
docker push ${REGISTRY_REPO}/${CCM_IMAGE}:${VERSION}
docker push ${REGISTRY_REPO}/${CCM_IMAGE}:latest

# Step 4: Build and Push CSI
echo ""
echo "=== Step 4/5: Building & Pushing CSI ==="
echo "Building CSI Controller & Node..."
# Multi-stage build requires targeting stages or manual separation if we want separate images
# The CSI Dockerfile has two stages: 'controller' and 'node'

# Build Controller
docker build --target controller -t ${REGISTRY_REPO}/${CSI_CONTROLLER_IMAGE}:${VERSION} -f csi/Dockerfile .
docker tag ${REGISTRY_REPO}/${CSI_CONTROLLER_IMAGE}:${VERSION} ${REGISTRY_REPO}/${CSI_CONTROLLER_IMAGE}:latest
echo "Pushing CSI Controller..."
docker push ${REGISTRY_REPO}/${CSI_CONTROLLER_IMAGE}:${VERSION}
docker push ${REGISTRY_REPO}/${CSI_CONTROLLER_IMAGE}:latest

# Build Node
docker build --target node -t ${REGISTRY_REPO}/${CSI_NODE_IMAGE}:${VERSION} -f csi/Dockerfile .
docker tag ${REGISTRY_REPO}/${CSI_NODE_IMAGE}:${VERSION} ${REGISTRY_REPO}/${CSI_NODE_IMAGE}:latest
echo "Pushing CSI Node..."
docker push ${REGISTRY_REPO}/${CSI_NODE_IMAGE}:${VERSION}
docker push ${REGISTRY_REPO}/${CSI_NODE_IMAGE}:latest

# Step 5: Git Tag
echo ""
echo "=== Step 5/5: Checking Git Tag ==="
if git rev-parse "${VERSION}" >/dev/null 2>&1; then
  echo "Tag ${VERSION} already exists, skipping creation..."
else
  git tag -a "${VERSION}" -m "Release ${VERSION}"
  echo "Created tag ${VERSION}"
  echo "Don't forget to push: git push origin ${VERSION}"
fi

echo ""
echo "=== Release ${VERSION} Complete ==="
echo "Images:"
echo "  - ${REGISTRY_REPO}/${PROVIDER_IMAGE}:${VERSION}"
echo "  - ${REGISTRY_REPO}/${CCM_IMAGE}:${VERSION}"
echo "  - ${REGISTRY_REPO}/${CSI_CONTROLLER_IMAGE}:${VERSION}"
echo "  - ${REGISTRY_REPO}/${CSI_NODE_IMAGE}:${VERSION}"
echo ""
