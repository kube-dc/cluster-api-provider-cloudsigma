package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
)

func main() {
	username := os.Getenv("CLOUDSIGMA_USERNAME")
	password := os.Getenv("CLOUDSIGMA_PASSWORD")
	region := os.Getenv("CLOUDSIGMA_REGION")

	if username == "" || password == "" || region == "" {
		fmt.Println("Error: CLOUDSIGMA_USERNAME, CLOUDSIGMA_PASSWORD, and CLOUDSIGMA_REGION must be set")
		os.Exit(1)
	}

	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(username, password)
	client := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
	ctx := context.Background()

	// List all servers
	fmt.Println("🔍 Listing all servers...")
	servers, _, err := client.Servers.List(ctx)
	if err != nil {
		fmt.Printf("Error listing servers: %v\n", err)
		os.Exit(1)
	}

	// Find test servers
	var testServers []cloudsigma.Server
	for _, server := range servers {
		if strings.Contains(server.Name, "multi-pool-test-cloudsigma") {
			testServers = append(testServers, server)
		}
	}

	fmt.Printf("Found %d test servers to delete\n\n", len(testServers))

	// Delete servers
	for i, server := range testServers {
		fmt.Printf("[%d/%d] Processing server: %s (UUID: %s, Status: %s)\n", 
			i+1, len(testServers), server.Name, server.UUID, server.Status)

		// Stop if running
		if server.Status == "running" || server.Status == "starting" {
			fmt.Printf("  ⏹️  Stopping server...\n")
			_, _, err := client.Servers.Stop(ctx, server.UUID)
			if err != nil {
				fmt.Printf("  ⚠️  Warning: failed to stop: %v\n", err)
			} else {
				fmt.Printf("  ✅ Stopped\n")
				time.Sleep(2 * time.Second)
			}
		}

		// Collect drive UUIDs before deletion
		var driveUUIDs []string
		for _, drive := range server.Drives {
			if drive.Drive != nil {
				driveUUIDs = append(driveUUIDs, drive.Drive.UUID)
			}
		}

		// Delete server
		fmt.Printf("  🗑️  Deleting server...\n")
		_, err := client.Servers.Delete(ctx, server.UUID)
		if err != nil {
			fmt.Printf("  ⚠️  Warning: failed to delete server: %v\n", err)
		} else {
			fmt.Printf("  ✅ Server deleted\n")
		}

		// Delete associated drives
		for _, driveUUID := range driveUUIDs {
			fmt.Printf("  🗑️  Deleting drive: %s\n", driveUUID)
			_, err := client.Drives.Delete(ctx, driveUUID)
			if err != nil {
				fmt.Printf("  ⚠️  Warning: failed to delete drive: %v\n", err)
			} else {
				fmt.Printf("  ✅ Drive deleted\n")
			}
		}

		fmt.Println()
	}

	// Clean up any orphaned drives
	fmt.Println("🔍 Checking for orphaned test drives...")
	drives, _, err := client.Drives.List(ctx, nil)
	if err != nil {
		fmt.Printf("Error listing drives: %v\n", err)
		os.Exit(1)
	}

	var orphanedDrives []cloudsigma.Drive
	for _, drive := range drives {
		if strings.Contains(drive.Name, "multi-pool-test-cloudsigma") && 
		   len(drive.MountedOn) == 0 {
			orphanedDrives = append(orphanedDrives, drive)
		}
	}

	if len(orphanedDrives) > 0 {
		fmt.Printf("Found %d orphaned drives to delete\n\n", len(orphanedDrives))
		for i, drive := range orphanedDrives {
			fmt.Printf("[%d/%d] Deleting orphaned drive: %s (UUID: %s)\n", 
				i+1, len(orphanedDrives), drive.Name, drive.UUID)
			_, err := client.Drives.Delete(ctx, drive.UUID)
			if err != nil {
				fmt.Printf("  ⚠️  Warning: failed to delete: %v\n", err)
			} else {
				fmt.Printf("  ✅ Deleted\n")
			}
		}
	} else {
		fmt.Println("No orphaned drives found")
	}

	fmt.Println("\n✅ Cleanup complete!")
}
