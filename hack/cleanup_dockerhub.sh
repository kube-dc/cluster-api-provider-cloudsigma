#!/bin/bash
set -e

# Docker Hub Cleanup Script
# Deletes all tags EXCEPT the specified ones.

REGISTRY="shalb"
REPOS=(
  "kube-dc-k8-manager"
  "cluster-api-provider-cloudsigma"
  "cloudsigma-ccm"
  "cloudsigma-csi-controller"
  "cloudsigma-csi-node"
)

# Define what to KEEP for each repo
# Format: "repo_name:tag1,tag2"
declare -A KEEP_TAGS
KEEP_TAGS["kube-dc-k8-manager"]="v0.4.0,latest"
KEEP_TAGS["cluster-api-provider-cloudsigma"]="v0.1.0,latest"
KEEP_TAGS["cloudsigma-ccm"]="v0.1.0,latest"
KEEP_TAGS["cloudsigma-csi-controller"]="v0.1.0,latest"
KEEP_TAGS["cloudsigma-csi-node"]="v0.1.0,latest"

echo "=== Docker Hub Cleanup Tool ==="
echo "This script will DELETE all tags for the configured repositories EXCEPT the ones specified to keep."
echo ""

# 1. Authentication
echo "Authenticating using existing Docker login..."

# Try to read auth from ~/.docker/config.json
if [ -f ~/.docker/config.json ]; then
  # Extract auth string for index.docker.io
  AUTH_STRING=$(jq -r '.auths["https://index.docker.io/v1/"].auth' ~/.docker/config.json)
  
  if [ "$AUTH_STRING" != "null" ] && [ -n "$AUTH_STRING" ]; then
    # Decode base64 (user:pass)
    DECODED=$(echo "$AUTH_STRING" | base64 -d)
    USERNAME=$(echo "$DECODED" | cut -d: -f1)
    PASSWORD=$(echo "$DECODED" | cut -d: -f2)
  else
    echo "No auth found in config.json. Falling back to manual entry."
  fi
fi

if [ -z "$USERNAME" ] || [ -z "$PASSWORD" ]; then
  echo "Please enter your Docker Hub credentials to obtain an API token."
  read -p "Username: " USERNAME
  read -s -p "Password (or Access Token): " PASSWORD
  echo ""
fi

echo "Authenticating as $USERNAME..."
TOKEN=$(curl -s -H "Content-Type: application/json" -X POST -d '{"username": "'${USERNAME}'", "password": "'${PASSWORD}'"}' https://hub.docker.com/v2/users/login/ | jq -r .token)

if [ "$TOKEN" == "null" ] || [ -z "$TOKEN" ]; then
  echo "Authentication failed. Please check your credentials."
  exit 1
fi
echo "Authentication successful."
echo ""

# 2. Process Repositories
for REPO in "${REPOS[@]}"; do
  FULL_REPO="${REGISTRY}/${REPO}"
  echo "------------------------------------------------"
  echo "Checking repository: ${FULL_REPO}"
  
  # Get allowlist for this repo
  IFS=',' read -r -a ALLOWED <<< "${KEEP_TAGS[$REPO]}"
  
  # Fetch all tags (pagination handling is basic here, assuming < 100 tags for now or using page_size=100)
  # For robust production use, this needs a while loop for pagination.
  TAGS_JSON=$(curl -s -H "Authorization: JWT ${TOKEN}" "https://hub.docker.com/v2/repositories/${REGISTRY}/${REPO}/tags/?page_size=100")
  
  # Extract tag names
  TAGS=$(echo "$TAGS_JSON" | jq -r '.results[].name')
  
  if [ -z "$TAGS" ]; then
    echo "No tags found or error fetching tags."
    continue
  fi

  TO_DELETE=()
  
  for TAG in $TAGS; do
    KEEP=false
    for A in "${ALLOWED[@]}"; do
      if [ "$TAG" == "$A" ]; then
        KEEP=true
        break
      fi
    done
    
    if [ "$KEEP" = false ]; then
      TO_DELETE+=("$TAG")
    fi
  done

  if [ ${#TO_DELETE[@]} -eq 0 ]; then
    echo "✅ No outdated tags to delete."
    continue
  fi

  echo "⚠️  Found ${#TO_DELETE[@]} tags to DELETE:"
  for T in "${TO_DELETE[@]}"; do
    echo "  - $T"
  done
  
  read -p "Delete these tags? (y/n): " CONFIRM
  if [[ $CONFIRM =~ ^[Yy] ]]; then
    for TAG in "${TO_DELETE[@]}"; do
      echo -n "Deleting ${TAG}... "
      RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE -H "Authorization: JWT ${TOKEN}" "https://hub.docker.com/v2/repositories/${REGISTRY}/${REPO}/tags/${TAG}/")
      if [ "$RESPONSE" == "204" ]; then
        echo "✅"
      else
        echo "❌ (HTTP $RESPONSE)"
      fi
    done
  else
    echo "Skipping..."
  fi
done

echo ""
echo "=== Cleanup Complete ==="
