// +build ignore

/*
Test script to verify CloudSigma API for IP failover
Run with: go run hack/test-ip-failover.go

This tests:
1. Get current server NIC configuration
2. Attach a static IP to a server
3. Detach static IP and switch back to DHCP
*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const (
	// CloudSigma API endpoint
	apiEndpoint = "https://next.cloudsigma.com/api/2.0"
)

type CustomIPv4Conf struct {
	Conf string       `json:"conf"`
	IP   *CustomIPRef `json:"ip,omitempty"`
}

type CustomIPRef struct {
	UUID string `json:"uuid"`
}

type CustomServerNIC struct {
	MAC      string          `json:"mac,omitempty"`
	VLAN     string          `json:"vlan,omitempty"`
	IPv4Conf *CustomIPv4Conf `json:"ip_v4_conf,omitempty"`
}

type NICUpdateRequest struct {
	NICs []CustomServerNIC `json:"nics"`
}

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: go run hack/test-ip-failover.go <action> <server-uuid> <ip-address>")
		fmt.Println("")
		fmt.Println("Actions:")
		fmt.Println("  get     - Get current server NIC configuration")
		fmt.Println("  attach  - Attach static IP to server")
		fmt.Println("  detach  - Detach static IP, switch to DHCP")
		fmt.Println("  list    - List all owned IPs")
		fmt.Println("")
		fmt.Println("Environment variables:")
		fmt.Println("  CS_USERNAME - CloudSigma username")
		fmt.Println("  CS_PASSWORD - CloudSigma password")
		fmt.Println("")
		fmt.Println("Example:")
		fmt.Println("  CS_USERNAME=user CS_PASSWORD=pass go run hack/test-ip-failover.go get 4192fc6e-0372-4961-99fa-6dc9d4038dbf 31.171.254.211")
		os.Exit(1)
	}

	action := os.Args[1]
	serverUUID := os.Args[2]
	ipAddress := os.Args[3]

	username := os.Getenv("CS_USERNAME")
	password := os.Getenv("CS_PASSWORD")

	if username == "" || password == "" {
		fmt.Println("Error: CS_USERNAME and CS_PASSWORD environment variables must be set")
		os.Exit(1)
	}

	ctx := context.Background()

	switch action {
	case "get":
		err := getServerNICs(ctx, username, password, serverUUID)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	case "attach":
		err := attachStaticIP(ctx, username, password, serverUUID, ipAddress)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	case "detach":
		err := detachStaticIP(ctx, username, password, serverUUID)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	case "list":
		err := listIPs(ctx, username, password)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown action: %s\n", action)
		os.Exit(1)
	}
}

func getServerNICs(ctx context.Context, username, password, serverUUID string) error {
	url := fmt.Sprintf("%s/servers/%s/", apiEndpoint, serverUUID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Pretty print the response
	var prettyJSON bytes.Buffer
	json.Indent(&prettyJSON, body, "", "  ")

	// Extract just the NICs section
	var server map[string]interface{}
	json.Unmarshal(body, &server)

	fmt.Println("=== Server Info ===")
	fmt.Printf("Name: %v\n", server["name"])
	fmt.Printf("UUID: %v\n", server["uuid"])
	fmt.Printf("Status: %v\n", server["status"])
	fmt.Println("")
	fmt.Println("=== NICs ===")

	nics, _ := json.MarshalIndent(server["nics"], "", "  ")
	fmt.Println(string(nics))

	return nil
}

func attachStaticIP(ctx context.Context, username, password, serverUUID, ipAddress string) error {
	fmt.Printf("Attaching static IP %s to server %s\n", ipAddress, serverUUID)

	// First, get current NICs to preserve MAC address
	url := fmt.Sprintf("%s/servers/%s/", apiEndpoint, serverUUID)

	getReq, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	getReq.Header.Set("Content-Type", "application/json")
	getReq.SetBasicAuth(username, password)

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()

	getBody, _ := io.ReadAll(getResp.Body)
	var server map[string]interface{}
	json.Unmarshal(getBody, &server)

	// Get existing MAC address
	var existingMAC string
	if nics, ok := server["nics"].([]interface{}); ok && len(nics) > 0 {
		if nic, ok := nics[0].(map[string]interface{}); ok {
			if mac, ok := nic["mac"].(string); ok {
				existingMAC = mac
				fmt.Printf("Preserving existing MAC: %s\n", existingMAC)
			}
		}
	}

	// Build update request
	updateReq := map[string]interface{}{
		"nics": []map[string]interface{}{
			{
				"mac": existingMAC,
				"ip_v4_conf": map[string]interface{}{
					"conf": "static",
					"ip": map[string]interface{}{
						"uuid": ipAddress,
					},
				},
			},
		},
	}

	body, _ := json.Marshal(updateReq)
	fmt.Printf("Request: %s\n", string(body))

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Printf("Success! Status: %d\n", resp.StatusCode)

	// Show updated NICs
	var updatedServer map[string]interface{}
	json.Unmarshal(respBody, &updatedServer)
	nics, _ := json.MarshalIndent(updatedServer["nics"], "", "  ")
	fmt.Println("Updated NICs:")
	fmt.Println(string(nics))

	return nil
}

func detachStaticIP(ctx context.Context, username, password, serverUUID string) error {
	fmt.Printf("Detaching static IP from server %s, switching to DHCP\n", serverUUID)

	url := fmt.Sprintf("%s/servers/%s/", apiEndpoint, serverUUID)

	// First get existing MAC
	getReq, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	getReq.Header.Set("Content-Type", "application/json")
	getReq.SetBasicAuth(username, password)

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()

	getBody, _ := io.ReadAll(getResp.Body)
	var server map[string]interface{}
	json.Unmarshal(getBody, &server)

	var existingMAC string
	if nics, ok := server["nics"].([]interface{}); ok && len(nics) > 0 {
		if nic, ok := nics[0].(map[string]interface{}); ok {
			if mac, ok := nic["mac"].(string); ok {
				existingMAC = mac
			}
		}
	}

	updateReq := map[string]interface{}{
		"nics": []map[string]interface{}{
			{
				"mac": existingMAC,
				"ip_v4_conf": map[string]interface{}{
					"conf": "dhcp",
				},
			},
		},
	}

	body, _ := json.Marshal(updateReq)
	fmt.Printf("Request: %s\n", string(body))

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Printf("Success! Status: %d\n", resp.StatusCode)
	return nil
}

func listIPs(ctx context.Context, username, password string) error {
	url := fmt.Sprintf("%s/ips/detail/", apiEndpoint)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	fmt.Println("=== Owned IPs ===")
	if objects, ok := result["objects"].([]interface{}); ok {
		for _, obj := range objects {
			ip := obj.(map[string]interface{})
			serverInfo := "not attached"
			if server, ok := ip["server"].(map[string]interface{}); ok && server != nil {
				serverInfo = fmt.Sprintf("attached to %s", server["uuid"])
			}
			subscription := "no subscription"
			if sub, ok := ip["subscription"].(map[string]interface{}); ok && sub != nil {
				subscription = fmt.Sprintf("subscription %v", sub["id"])
			}
			fmt.Printf("  %s - %s (%s)\n", ip["uuid"], serverInfo, subscription)
		}
	}

	return nil
}
