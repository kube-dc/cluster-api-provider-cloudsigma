package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
)

func main() {
	username := os.Getenv("CLOUDSIGMA_USERNAME")
	password := os.Getenv("CLOUDSIGMA_PASSWORD")
	region := os.Getenv("CLOUDSIGMA_REGION")

	if username == "" || password == "" {
		fmt.Println("❌ Error: Set CLOUDSIGMA_USERNAME and CLOUDSIGMA_PASSWORD")
		os.Exit(1)
	}

	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(username, password)
	client := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
	ctx := context.Background()

	// List VLANs
	fmt.Println("🌐 Available VLANs:")
	vlans, _, err := client.VLANs.List(ctx)
	if err != nil {
		fmt.Printf("❌ Error listing VLANs: %v\n", err)
		os.Exit(1)
	}
	for _, vlan := range vlans {
		fmt.Printf("  • VLAN UUID: %s\n", vlan.UUID)
	}

	// List drives (images)
	fmt.Println("\n💾 Available Drives/Images:")
	drives, _, err := client.Drives.List(ctx, nil)
	if err != nil {
		fmt.Printf("❌ Error listing drives: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n  Ubuntu/Debian images:")
	for _, drive := range drives {
		if drive.Name != "" {
			// Show potentially useful images
			name := drive.Name
			if len(name) > 60 {
				name = name[:60] + "..."
			}
			fmt.Printf("  • %s\n    UUID: %s, Size: %.2f GB\n", name, drive.UUID, float64(drive.Size)/(1024*1024*1024))
		}
	}
}
