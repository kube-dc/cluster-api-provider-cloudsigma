#!/bin/bash
set -e

# Release script for cluster-api-provider-cloudsigma
# Usage: ./hack/release.sh v0.1.0

REGISTRY_REPO="shalb"
IMAGE_NAME="cluster-api-provider-cloudsigma"

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
echo "Registry: ${REGISTRY_REPO}/${IMAGE_NAME}:${VERSION}"
echo ""

read -p "Proceed with release? (y/n): " confirm
if [[ ! $confirm =~ ^[Yy] ]]; then
  echo "Aborted."
  exit 0
fi

path=$(dirname -- "$( readlink -f -- "$0"; )")
rootPath=$(cd -- "${path}/../" &> /dev/null && pwd)
cd "${rootPath}"

# Clean
echo ""
echo "=== Step 1/4: Cleaning ==="
make clean

# Build Docker image
echo ""
echo "=== Step 2/4: Building Docker image ==="
make docker-build IMG=${REGISTRY_REPO}/${IMAGE_NAME}:${VERSION}
make docker-build-latest IMG=${REGISTRY_REPO}/${IMAGE_NAME}:${VERSION} IMG_LATEST=${REGISTRY_REPO}/${IMAGE_NAME}:latest

# Push to Docker Hub
echo ""
echo "=== Step 3/4: Pushing to Docker Hub ==="
docker push ${REGISTRY_REPO}/${IMAGE_NAME}:${VERSION}
docker push ${REGISTRY_REPO}/${IMAGE_NAME}:latest

# Create git tag
echo ""
echo "=== Step 4/4: Creating git tag ==="
if git rev-parse "${VERSION}" >/dev/null 2>&1; then
  echo "Tag ${VERSION} already exists, skipping..."
else
  git tag -a "${VERSION}" -m "Release ${VERSION}"
  echo "Created tag ${VERSION}"
  echo "Don't forget to push: git push origin ${VERSION}"
fi

echo ""
echo "=== Release ${VERSION} Complete ==="
echo ""
