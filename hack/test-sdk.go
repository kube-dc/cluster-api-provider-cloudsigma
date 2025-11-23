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
		fmt.Println("❌ Error: CLOUDSIGMA_USERNAME and CLOUDSIGMA_PASSWORD must be set")
		fmt.Println("Run: source .env.local")
		os.Exit(1)
	}

	fmt.Printf("🔐 Testing CloudSigma SDK connection...\n")
	fmt.Printf("   Username: %s\n", username)
	fmt.Printf("   Region: %s\n", region)
	fmt.Println()

	// Create client
	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(username, password)
	client := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))

	ctx := context.Background()

	// Test 1: List servers
	fmt.Println("📋 Test 1: Listing servers...")
	servers, resp, err := client.Servers.List(ctx)
	if err != nil {
		fmt.Printf("❌ Failed to list servers: %v\n", err)
		if resp != nil {
			fmt.Printf("   HTTP Status: %d\n", resp.StatusCode)
		}
		os.Exit(1)
	}
	fmt.Printf("✅ Successfully listed servers: %d found\n", len(servers))
	for i, srv := range servers {
		fmt.Printf("   [%d] %s (UUID: %s, Status: %s)\n", i+1, srv.Name, srv.UUID, srv.Status)
	}
	fmt.Println()

	// Test 2: Get profile
	fmt.Println("👤 Test 2: Getting profile...")
	profile, resp, err := client.Profile.Get(ctx)
	if err != nil {
		fmt.Printf("❌ Failed to get profile: %v\n", err)
		if resp != nil {
			fmt.Printf("   HTTP Status: %d\n", resp.StatusCode)
		}
		os.Exit(1)
	}
	fmt.Printf("✅ Profile retrieved successfully\n")
	fmt.Printf("   Email: %s\n", profile.Email)
	fmt.Printf("   UUID: %s\n", profile.UUID)
	fmt.Println()

	// Test 3: List drives (images)
	fmt.Println("💾 Test 3: Listing drives...")
	drives, resp, err := client.Drives.List(ctx, nil)
	if err != nil {
		fmt.Printf("❌ Failed to list drives: %v\n", err)
		if resp != nil {
			fmt.Printf("   HTTP Status: %d\n", resp.StatusCode)
		}
		os.Exit(1)
	}
	fmt.Printf("✅ Successfully listed drives: %d found\n", len(drives))
	fmt.Println()

	// Test 4: List VLANs
	fmt.Println("🌐 Test 4: Listing VLANs...")
	vlans, resp, err := client.VLANs.List(ctx)
	if err != nil {
		fmt.Printf("❌ Failed to list VLANs: %v\n", err)
		if resp != nil {
			fmt.Printf("   HTTP Status: %d\n", resp.StatusCode)
		}
		os.Exit(1)
	}
	fmt.Printf("✅ Successfully listed VLANs: %d found\n", len(vlans))
	fmt.Println()

	fmt.Println("🎉 All SDK tests passed successfully!")
	fmt.Println("✅ CloudSigma API credentials are working")
	fmt.Println("✅ Ready to implement CAPCS provider")
}
