package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
)

func main() {
	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(
		os.Getenv("CLOUDSIGMA_USERNAME"),
		os.Getenv("CLOUDSIGMA_PASSWORD"),
	)
	client := cloudsigma.NewClient(cred, cloudsigma.WithLocation(os.Getenv("CLOUDSIGMA_REGION")))

	servers, _, err := client.Servers.List(context.Background())
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	if len(servers) > 0 && len(servers[0].Drives) > 0 {
		fmt.Printf("Server: %s\n", servers[0].Name)
		fmt.Printf("Drive count: %d\n", len(servers[0].Drives))

		driveJSON, _ := json.MarshalIndent(servers[0].Drives[0], "", "  ")
		fmt.Printf("Drive reference:\n%s\n", string(driveJSON))
	} else {
		fmt.Println("No servers with drives found")
	}
}
