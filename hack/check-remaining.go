// check-remaining.go - Check and optionally delete test VMs and drives in CloudSigma
//
// Usage:
//   go run hack/check-remaining.go                    # List all test resources
//   go run hack/check-remaining.go --delete           # Delete all test resources
//   go run hack/check-remaining.go --pattern=user-a   # Filter by pattern
//
// Environment variables:
//   CLOUDSIGMA_USERNAME - CloudSigma username
//   CLOUDSIGMA_PASSWORD - CloudSigma password
//   CLOUDSIGMA_REGION   - CloudSigma region (e.g., "next", "zrh")

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
)

// Default patterns to match test resources
var defaultPatterns = []string{
	"multi-pool-test",
	"user-a",
	"cloudsigma-cluster",
	"cloudsigma-test",
	"test-cluster",
}

func main() {
	var deleteResources bool
	var pattern string
	var listAll bool

	flag.BoolVar(&deleteResources, "delete", false, "Delete matching resources")
	flag.StringVar(&pattern, "pattern", "", "Custom pattern to match (overrides defaults)")
	flag.BoolVar(&listAll, "all", false, "List all servers and drives")
	flag.Parse()

	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(
		os.Getenv("CLOUDSIGMA_USERNAME"),
		os.Getenv("CLOUDSIGMA_PASSWORD"),
	)
	region := os.Getenv("CLOUDSIGMA_REGION")
	if region == "" {
		region = "next"
	}
	client := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
	ctx := context.Background()

	fmt.Printf("CloudSigma Region: %s\n", region)
	fmt.Println(strings.Repeat("=", 60))

	// Determine patterns to use
	patterns := defaultPatterns
	if pattern != "" {
		patterns = []string{pattern}
	}

	// List servers
	servers, _, err := client.Servers.List(ctx)
	if err != nil {
		fmt.Printf("Error listing servers: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nTotal servers: %d\n", len(servers))

	var matchedServers []cloudsigma.Server
	for _, s := range servers {
		if listAll {
			fmt.Printf("  • %s (UUID: %s, Status: %s)\n", s.Name, s.UUID, s.Status)
			continue
		}
		if matchesAny(s.Name, patterns) {
			matchedServers = append(matchedServers, s)
			fmt.Printf("  ⚠️  %s (UUID: %s, Status: %s)\n", s.Name, s.UUID, s.Status)
		}
	}

	// List drives
	drives, _, err := client.Drives.List(ctx, nil)
	if err != nil {
		fmt.Printf("Error listing drives: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nTotal drives: %d\n", len(drives))

	var matchedDrives []cloudsigma.Drive
	for _, d := range drives {
		if listAll {
			fmt.Printf("  • %s (UUID: %s, Size: %dGB)\n", d.Name, d.UUID, d.Size/1024/1024/1024)
			continue
		}
		if matchesAny(d.Name, patterns) {
			matchedDrives = append(matchedDrives, d)
			fmt.Printf("  ⚠️  %s (UUID: %s, Size: %dGB)\n", d.Name, d.UUID, d.Size/1024/1024/1024)
		}
	}

	if listAll {
		return
	}

	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("Matched servers: %d\n", len(matchedServers))
	fmt.Printf("Matched drives:  %d\n", len(matchedDrives))

	if !deleteResources {
		if len(matchedServers) > 0 || len(matchedDrives) > 0 {
			fmt.Println("\nRun with --delete to remove these resources")
		}
		return
	}

	// Delete resources
	fmt.Println("\n🗑️  Deleting resources...")

	// Stop and delete servers
	for _, s := range matchedServers {
		fmt.Printf("  Stopping server %s...", s.Name)
		if s.Status == "running" {
			_, _, err := client.Servers.Stop(ctx, s.UUID)
			if err != nil {
				fmt.Printf(" error: %v\n", err)
				continue
			}
			fmt.Printf(" stopped, waiting...")
			time.Sleep(5 * time.Second)
		}
		fmt.Printf(" deleting...")
		_, err := client.Servers.Delete(ctx, s.UUID)
		if err != nil {
			fmt.Printf(" error: %v\n", err)
			continue
		}
		fmt.Println(" ✓ deleted")
	}

	// Wait for servers to be fully deleted
	if len(matchedServers) > 0 {
		fmt.Println("  Waiting for servers to be fully deleted...")
		time.Sleep(10 * time.Second)
	}

	// Delete drives
	for _, d := range matchedDrives {
		fmt.Printf("  Deleting drive %s...", d.Name)
		_, err := client.Drives.Delete(ctx, d.UUID)
		if err != nil {
			fmt.Printf(" error: %v\n", err)
			continue
		}
		fmt.Println(" ✓ deleted")
	}

	fmt.Println("\n✅ Cleanup complete")
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}
