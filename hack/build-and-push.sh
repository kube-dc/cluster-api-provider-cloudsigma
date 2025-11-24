#!/bin/bash
# Build and push CloudSigma CAPI provider image
set -e

# Configuration
REGISTRY_REPO="${REGISTRY_REPO:-shalb}"
IMAGE_NAME="cluster-api-provider-cloudsigma"
VERSION="${VERSION:-$(git describe --tags --always --dirty)}"
LATEST="${LATEST:-true}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}==> Building CloudSigma CAPI Provider${NC}"
echo "Repository: ${REGISTRY_REPO}"
echo "Image: ${IMAGE_NAME}"
echo "Version: ${VERSION}"
echo ""

# Check if logged in to registry
echo -e "${YELLOW}==> Checking Docker Hub authentication...${NC}"
if ! docker info 2>/dev/null | grep -q "docker.io"; then
    echo -e "${RED}Warning: Not logged in to Docker Hub${NC}"
    echo "Login with: docker login -u USERNAME"
    echo ""
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Build image
IMG="${REGISTRY_REPO}/${IMAGE_NAME}:${VERSION}"
echo -e "${GREEN}==> Building image: ${IMG}${NC}"
docker build -t "${IMG}" .

if [ $? -ne 0 ]; then
    echo -e "${RED}✗ Build failed${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Build successful${NC}"
echo ""

# Tag as latest if requested
if [ "${LATEST}" = "true" ]; then
    LATEST_IMG="${REGISTRY_REPO}/${IMAGE_NAME}:latest"
    echo -e "${GREEN}==> Tagging as latest: ${LATEST_IMG}${NC}"
    docker tag "${IMG}" "${LATEST_IMG}"
fi

# Push image
echo -e "${GREEN}==> Pushing image: ${IMG}${NC}"
docker push "${IMG}"

if [ $? -ne 0 ]; then
    echo -e "${RED}✗ Push failed${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Push successful${NC}"

# Push latest tag if applicable
if [ "${LATEST}" = "true" ]; then
    echo -e "${GREEN}==> Pushing latest tag${NC}"
    docker push "${LATEST_IMG}"
    
    if [ $? -ne 0 ]; then
        echo -e "${YELLOW}Warning: Failed to push latest tag${NC}"
    else
        echo -e "${GREEN}✓ Latest tag pushed${NC}"
    fi
fi

echo ""
echo -e "${GREEN}==> Image built and pushed successfully!${NC}"
echo ""
echo "Images:"
echo "  - ${IMG}"
if [ "${LATEST}" = "true" ]; then
    echo "  - ${LATEST_IMG}"
fi
echo ""
echo "Update deployment with:"
echo "  kubectl set image deployment/cloudsigma-controller-manager \\"
echo "    manager=${IMG} \\"
echo "    -n capcs-system"
echo ""
echo "Or update config/manager/deployment.yaml and apply:"
echo "  sed -i 's|image:.*cluster-api-provider-cloudsigma:.*|image: ${IMG}|' config/manager/deployment.yaml"
echo "  kubectl apply -f config/install.yaml"
