/*
Copyright 2025 Kube-DC Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cloud

import (
	"context"
	"fmt"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"k8s.io/klog/v2"
)

// Client wraps the CloudSigma SDK client with CAPI-specific functionality
type Client struct {
	sdk         *cloudsigma.Client
	region      string
	username    string
	password    string
	apiEndpoint string
}

// NewClient creates a new CloudSigma client wrapper
func NewClient(username, password, region string) (*Client, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password are required")
	}

	if region == "" {
		region = "zrh" // Default to Zurich
	}

	klog.V(4).Infof("Creating CloudSigma client for region: %s", region)

	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(username, password)
	sdk := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))

	// Determine API endpoint based on region
	apiEndpoint := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0", region)

	return &Client{
		sdk:         sdk,
		region:      region,
		username:    username,
		password:    password,
		apiEndpoint: apiEndpoint,
	}, nil
}

// Region returns the configured region
func (c *Client) Region() string {
	return c.region
}

// Username returns the configured username
func (c *Client) Username() string {
	return c.username
}

// VerifyConnection tests the connection to CloudSigma API
func (c *Client) VerifyConnection(ctx context.Context) error {
	klog.V(4).Info("Verifying CloudSigma API connection")

	_, _, err := c.sdk.Profile.Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to verify CloudSigma connection: %w", err)
	}

	klog.V(4).Info("CloudSigma API connection verified")
	return nil
}
