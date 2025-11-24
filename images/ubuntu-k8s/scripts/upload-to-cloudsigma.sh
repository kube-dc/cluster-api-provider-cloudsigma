#!/bin/bash
set -euo pipefail

echo "==> Uploading image to CloudSigma"

# Check required environment variables
if [ -z "${CLOUDSIGMA_USERNAME:-}" ] || [ -z "${CLOUDSIGMA_PASSWORD:-}" ] || [ -z "${CLOUDSIGMA_REGION:-}" ]; then
  echo "Error: CLOUDSIGMA_USERNAME, CLOUDSIGMA_PASSWORD, and CLOUDSIGMA_REGION must be set"
  exit 1
fi

if [ -z "${IMAGE_NAME:-}" ] || [ -z "${IMAGE_FILE:-}" ]; then
  echo "Error: IMAGE_NAME and IMAGE_FILE must be set"
  exit 1
fi

# Check if image file exists
if [ ! -f "${IMAGE_FILE}" ]; then
  echo "Error: Image file not found: ${IMAGE_FILE}"
  exit 1
fi

# Get file size
FILE_SIZE=$(stat -c%s "${IMAGE_FILE}")
echo "Image file: ${IMAGE_FILE}"
echo "Image size: ${FILE_SIZE} bytes ($(numfmt --to=iec-i --suffix=B ${FILE_SIZE}))"

# CloudSigma upload endpoint (use direct.* domain for uploads)
UPLOAD_ENDPOINT="https://direct.${CLOUDSIGMA_REGION}.cloudsigma.com/api/2.0/drives/upload/"
API_BASE="https://${CLOUDSIGMA_REGION}.cloudsigma.com/api/2.0"

# Upload the image data (CloudSigma creates drive automatically)
echo "Uploading image to CloudSigma (this will create the drive automatically)..."
echo "This may take several minutes depending on file size and network speed..."
echo "Upload started at: $(date '+%H:%M:%S')"
START_TIME=$(date +%s)

DRIVE_UUID=$(curl --http1.1 --max-time 1800 \
  -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
  --upload-file "${IMAGE_FILE}" \
  --header "Content-Type: application/octet-stream" \
  --progress-bar \
  "${UPLOAD_ENDPOINT}")

UPLOAD_STATUS=$?
END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo ""
echo "Upload finished at: $(date '+%H:%M:%S')"
echo "Total time: ${ELAPSED} seconds"

if [ ${UPLOAD_STATUS} -ne 0 ]; then
  echo "❌ Upload failed with exit code: ${UPLOAD_STATUS}"
  exit 1
fi

if [ -z "${DRIVE_UUID}" ]; then
  echo "❌ Upload failed: No drive UUID returned"
  exit 1
fi

echo "✅ Drive created with UUID: ${DRIVE_UUID}"

# Rename the drive to our desired name
echo "Setting drive name to: ${IMAGE_NAME}"
curl -s --http1.1 --max-time 30 \
  -u "${CLOUDSIGMA_USERNAME}:${CLOUDSIGMA_PASSWORD}" \
  -X PUT \
  -H "Content-Type: application/json" \
  -d "{\"name\": \"${IMAGE_NAME}\"}" \
  "${API_BASE}/drives/${DRIVE_UUID}/" > /dev/null

echo ""
echo "✅ Image uploaded successfully!"
echo "Drive UUID: ${DRIVE_UUID}"
echo "Drive Name: ${IMAGE_NAME}"
echo ""
echo "You can now use this drive UUID in your CloudSigmaMachineTemplate."

exit 0

# OLD GO CODE BELOW (keeping for reference, not executed)
cat > /dev/null << 'GOEOF'
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: upload-drive <image-file> <image-name>")
		os.Exit(1)
	}

	imageFile := os.Args[1]
	imageName := os.Args[2]

	username := os.Getenv("CLOUDSIGMA_USERNAME")
	password := os.Getenv("CLOUDSIGMA_PASSWORD")
	region := os.Getenv("CLOUDSIGMA_REGION")

	if username == "" || password == "" || region == "" {
		fmt.Println("Error: CLOUDSIGMA credentials not set")
		os.Exit(1)
	}

	fmt.Printf("Uploading %s as %s to CloudSigma %s...\n", imageFile, imageName, region)

	// Get file info
	fileInfo, err := os.Stat(imageFile)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fileSizeBytes := fileInfo.Size()
	fileSizeGB := float64(fileSizeBytes) / (1024 * 1024 * 1024)
	fmt.Printf("File size: %.2f GB (%d bytes)\n", fileSizeGB, fileSizeBytes)

	ctx := context.Background()

	// Create CloudSigma client
	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(username, password)
	client := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))

	// Create drive
	fmt.Println("Creating drive in CloudSigma...")
	driveRequest := &cloudsigma.DriveCreateRequest{
		Drives: []cloudsigma.Drive{
			{
				Name:   imageName,
				Size:   int(fileSizeBytes),
				Media:  "disk",
			},
		},
	}

	createdDrives, _, err := client.Drives.Create(ctx, driveRequest)
	if err != nil {
		fmt.Printf("Error creating drive: %v\n", err)
		os.Exit(1)
	}
	if len(createdDrives) == 0 {
		fmt.Println("Error: No drive was created")
		os.Exit(1)
	}
	createdDrive := createdDrives[0]
	fmt.Printf("Drive created with UUID: %s\n", createdDrive.UUID)

	// Upload image data using streaming
	fmt.Println("Uploading image data...")
	
	// Open file
	file, err := os.Open(imageFile)
	if err != nil {
		fmt.Printf("Error opening file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// Use CloudSigma SDK's upload method
	fmt.Println("Uploading to CloudSigma (this may take several minutes)...")
	err = client.Drives.Upload(ctx, createdDrive.UUID, file)
	if err != nil {
		fmt.Printf("Error uploading: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Image uploaded successfully!")
	fmt.Printf("Drive UUID: %s\n", createdDrive.UUID)
	fmt.Printf("Drive Name: %s\n", imageName)
}
GOEOF

# Build and run the upload tool
WORK_DIR=$(mktemp -d)
cd "$WORK_DIR"

# Create go.mod with dependencies
cat > go.mod << 'MODEOF'
module upload-drive

go 1.21

require github.com/cloudsigma/cloudsigma-sdk-go v0.15.1
MODEOF

# Copy the upload program
cp /tmp/upload-drive.go .

go mod tidy
go run upload-drive.go "${IMAGE_FILE}" "${IMAGE_NAME}"

# Cleanup
cd /
rm -rf "$WORK_DIR"

echo "==> Upload complete"
