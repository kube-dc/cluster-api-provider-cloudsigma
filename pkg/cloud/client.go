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
	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/auth"
	"k8s.io/klog/v2"
)

// Client wraps the CloudSigma SDK client with CAPI-specific functionality
type Client struct {
	sdk         *cloudsigma.Client
	region      string
	username    string
	password    string
	apiEndpoint string

	// Impersonation support
	impersonationClient *auth.ImpersonationClient
	impersonatedUser    string
	useImpersonation    bool
}

// NewClient creates a new CloudSigma client wrapper using username/password credentials.
// This is the legacy authentication mode.
func NewClient(username, password, region string) (*Client, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password are required")
	}

	if region == "" {
		region = "zrh" // Default to Zurich
	}

	klog.V(4).Infof("Creating CloudSigma client for region: %s (credential mode)", region)

	cred := cloudsigma.NewUsernamePasswordCredentialsProvider(username, password)
	sdk := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))

	// Determine API endpoint based on region
	apiEndpoint := fmt.Sprintf("https://%s.cloudsigma.com/api/2.0", region)

	return &Client{
		sdk:              sdk,
		region:           region,
		username:         username,
		password:         password,
		apiEndpoint:      apiEndpoint,
		useImpersonation: false,
	}, nil
}

// NewClientWithImpersonation creates a new CloudSigma client that uses OAuth impersonation.
// This allows the controller to create resources in the specified user's CloudSigma account.
func NewClientWithImpersonation(ctx context.Context, impersonationClient *auth.ImpersonationClient, userEmail, region string) (*Client, error) {
	if impersonationClient == nil {
		return nil, fmt.Errorf("impersonationClient is required")
	}
	if userEmail == "" {
		return nil, fmt.Errorf("userEmail is required for impersonation")
	}
	if region == "" {
		region = "zrh" // Default to Zurich
	}

	klog.V(4).Infof("Creating CloudSigma client for region: %s (impersonation mode, user: %s)", region, userEmail)

	// Get impersonated token
	token, err := impersonationClient.GetImpersonatedToken(ctx, userEmail, region)
	if err != nil {
		return nil, fmt.Errorf("failed to get impersonated token for user %s: %w", userEmail, err)
	}

	// Create SDK client with token-based authentication
	cred := cloudsigma.NewTokenCredentialsProvider(token)
	sdk := cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))

	// Determine API endpoint based on region
	apiEndpoint := fmt.Sprintf("https://direct.%s.cloudsigma.com/api/2.0", region)

	return &Client{
		sdk:                 sdk,
		region:              region,
		apiEndpoint:         apiEndpoint,
		impersonationClient: impersonationClient,
		impersonatedUser:    userEmail,
		useImpersonation:    true,
	}, nil
}

// RefreshImpersonatedToken refreshes the impersonated token if using impersonation mode.
// This should be called before long-running operations to ensure the token is still valid.
func (c *Client) RefreshImpersonatedToken(ctx context.Context) error {
	if !c.useImpersonation {
		return nil // No refresh needed for credential-based auth
	}

	klog.V(4).Infof("Refreshing impersonated token for user: %s", c.impersonatedUser)

	token, err := c.impersonationClient.GetImpersonatedToken(ctx, c.impersonatedUser, c.region)
	if err != nil {
		return fmt.Errorf("failed to refresh impersonated token: %w", err)
	}

	// Recreate SDK client with new token
	cred := cloudsigma.NewTokenCredentialsProvider(token)
	c.sdk = cloudsigma.NewClient(cred, cloudsigma.WithLocation(c.region))

	return nil
}

// IsImpersonationMode returns true if the client is using impersonation
func (c *Client) IsImpersonationMode() bool {
	return c.useImpersonation
}

// ImpersonatedUser returns the impersonated user email, or empty string if not using impersonation
func (c *Client) ImpersonatedUser() string {
	return c.impersonatedUser
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
