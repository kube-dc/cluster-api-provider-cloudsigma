package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
)

func main() {
	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(
		os.Getenv("CLOUDSIGMA_USERNAME"),
		os.Getenv("CLOUDSIGMA_PASSWORD"),
	)
	client := cloudsigma.NewClient(cred, cloudsigma.WithLocation(os.Getenv("CLOUDSIGMA_REGION")))

	servers, _, _ := client.Servers.List(context.Background())
	fmt.Printf("Total servers: %d\n\n", len(servers))

	testCount := 0
	for _, s := range servers {
		if strings.Contains(s.Name, "multi-pool-test-cloudsigma") {
			testCount++
			fmt.Printf("• %s (UUID: %s, Status: %s)\n", s.Name, s.UUID, s.Status)
		}
	}

	fmt.Printf("\n⚠️  Test servers still remaining: %d\n", testCount)

	// Check drives
	drives, _, _ := client.Drives.List(context.Background(), nil)
	testDrives := 0
	for _, d := range drives {
		if strings.Contains(d.Name, "multi-pool-test-cloudsigma") {
			testDrives++
		}
	}
	fmt.Printf("⚠️  Test drives still remaining: %d\n", testDrives)
}
