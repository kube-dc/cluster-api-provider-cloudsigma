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

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	// Default token expiry buffer - refresh tokens 5 minutes before expiry
	defaultTokenExpiryBuffer = 5 * time.Minute

	// Default HTTP timeout for OAuth requests
	defaultHTTPTimeout = 30 * time.Second

	// UMA grant type for RPT token exchange
	umaGrantType = "urn:ietf:params:oauth:grant-type:uma-ticket"

	// Service provider API audience
	serviceProviderAudience = "service_provider_api"
)

// ImpersonationConfig holds configuration for the impersonation client
type ImpersonationConfig struct {
	// OAuthURL is the CloudSigma OAuth/Keycloak URL (e.g., https://oauth.cloudsigma.com)
	OAuthURL string

	// ClientID is the service account client ID
	ClientID string

	// ClientSecret is the service account client secret
	ClientSecret string

	// TokenExpiryBuffer is the time before expiry to refresh tokens
	TokenExpiryBuffer time.Duration

	// HTTPTimeout is the timeout for HTTP requests
	HTTPTimeout time.Duration
}

// CachedToken holds an impersonated token with expiry information
type CachedToken struct {
	Token     string
	ExpiresAt time.Time
	UserEmail string
	Region    string
}

// IsExpired checks if the token is expired (including buffer)
func (t *CachedToken) IsExpired(buffer time.Duration) bool {
	return time.Now().Add(buffer).After(t.ExpiresAt)
}

// ImpersonationClient handles CloudSigma OAuth impersonation flow
type ImpersonationClient struct {
	config     ImpersonationConfig
	httpClient *http.Client

	// Service account token cache
	saToken          string
	saTokenExpiresAt time.Time
	saTokenMutex     sync.RWMutex

	// RPT token cache
	rptToken          string
	rptTokenExpiresAt time.Time
	rptTokenMutex     sync.RWMutex

	// Impersonated token cache (per user+region)
	tokenCache map[string]*CachedToken
	cacheMutex sync.RWMutex
}

// NewImpersonationClient creates a new impersonation client
func NewImpersonationClient(config ImpersonationConfig) (*ImpersonationClient, error) {
	if config.OAuthURL == "" {
		return nil, fmt.Errorf("OAuthURL is required")
	}
	if config.ClientID == "" {
		return nil, fmt.Errorf("ClientID is required")
	}
	if config.ClientSecret == "" {
		return nil, fmt.Errorf("ClientSecret is required")
	}

	if config.TokenExpiryBuffer == 0 {
		config.TokenExpiryBuffer = defaultTokenExpiryBuffer
	}
	if config.HTTPTimeout == 0 {
		config.HTTPTimeout = defaultHTTPTimeout
	}

	return &ImpersonationClient{
		config: config,
		httpClient: &http.Client{
			Timeout: config.HTTPTimeout,
		},
		tokenCache: make(map[string]*CachedToken),
	}, nil
}

// GetImpersonatedToken returns a valid impersonated token for the specified user and region.
// It uses caching to avoid unnecessary OAuth calls.
func (c *ImpersonationClient) GetImpersonatedToken(ctx context.Context, userEmail, region string) (string, error) {
	if userEmail == "" {
		return "", fmt.Errorf("userEmail is required for impersonation")
	}
	if region == "" {
		return "", fmt.Errorf("region is required for impersonation")
	}

	cacheKey := fmt.Sprintf("%s:%s", userEmail, region)

	// Check cache first
	c.cacheMutex.RLock()
	cached, exists := c.tokenCache[cacheKey]
	c.cacheMutex.RUnlock()

	if exists && !cached.IsExpired(c.config.TokenExpiryBuffer) {
		klog.V(4).Infof("Using cached impersonated token for user %s in region %s", userEmail, region)
		return cached.Token, nil
	}

	// Token not cached or expired, get a new one
	klog.V(2).Infof("Getting new impersonated token for user %s in region %s", userEmail, region)

	token, expiresAt, err := c.fetchImpersonatedToken(ctx, userEmail, region)
	if err != nil {
		return "", err
	}

	// Cache the token
	c.cacheMutex.Lock()
	c.tokenCache[cacheKey] = &CachedToken{
		Token:     token,
		ExpiresAt: expiresAt,
		UserEmail: userEmail,
		Region:    region,
	}
	c.cacheMutex.Unlock()

	return token, nil
}

// fetchImpersonatedToken performs the full OAuth impersonation flow
func (c *ImpersonationClient) fetchImpersonatedToken(ctx context.Context, userEmail, region string) (string, time.Time, error) {
	// Step 1: Get service account access token
	saToken, err := c.getServiceAccountToken(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get service account token: %w", err)
	}

	// Step 2: Exchange for RPT token
	rptToken, err := c.getRPTToken(ctx, saToken)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get RPT token: %w", err)
	}

	// Step 3: Impersonate user
	impersonatedToken, expiresAt, err := c.impersonateUser(ctx, rptToken, saToken, userEmail, region)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to impersonate user %s: %w", userEmail, err)
	}

	return impersonatedToken, expiresAt, nil
}

// getServiceAccountToken gets the service account access token using client_credentials grant (Step 1)
func (c *ImpersonationClient) getServiceAccountToken(ctx context.Context) (string, error) {
	// Check cache
	c.saTokenMutex.RLock()
	if c.saToken != "" && time.Now().Add(c.config.TokenExpiryBuffer).Before(c.saTokenExpiresAt) {
		token := c.saToken
		c.saTokenMutex.RUnlock()
		klog.V(4).Info("Using cached service account token")
		return token, nil
	}
	c.saTokenMutex.RUnlock()

	klog.V(2).Info("Fetching new service account token")

	tokenURL := fmt.Sprintf("%s/realms/cloudsigma/protocol/openid-connect/token", c.config.OAuthURL)

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", c.config.ClientID)
	data.Set("client_secret", c.config.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	// Cache the token
	c.saTokenMutex.Lock()
	c.saToken = tokenResp.AccessToken
	c.saTokenExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	c.saTokenMutex.Unlock()

	klog.V(2).Info("Successfully obtained service account token")
	return tokenResp.AccessToken, nil
}

// getRPTToken exchanges access token for RPT token using UMA ticket grant (Step 2)
func (c *ImpersonationClient) getRPTToken(ctx context.Context, accessToken string) (string, error) {
	// Check cache
	c.rptTokenMutex.RLock()
	if c.rptToken != "" && time.Now().Add(c.config.TokenExpiryBuffer).Before(c.rptTokenExpiresAt) {
		token := c.rptToken
		c.rptTokenMutex.RUnlock()
		klog.V(4).Info("Using cached RPT token")
		return token, nil
	}
	c.rptTokenMutex.RUnlock()

	klog.V(2).Info("Fetching new RPT token")

	tokenURL := fmt.Sprintf("%s/realms/cloudsigma/protocol/openid-connect/token", c.config.OAuthURL)

	data := url.Values{}
	data.Set("grant_type", umaGrantType)
	data.Set("audience", serviceProviderAudience)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("RPT token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	// Cache the token
	c.rptTokenMutex.Lock()
	c.rptToken = tokenResp.AccessToken
	c.rptTokenExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	c.rptTokenMutex.Unlock()

	klog.V(2).Info("Successfully obtained RPT token")
	return tokenResp.AccessToken, nil
}

// impersonateUser gets an impersonated token for the specified user (Step 3)
func (c *ImpersonationClient) impersonateUser(ctx context.Context, rptToken, subjectToken, userEmail, region string) (string, time.Time, error) {
	klog.V(2).Infof("Impersonating user %s in region %s", userEmail, region)

	// Build impersonation URL for the specific region
	impersonateURL := fmt.Sprintf("https://direct.%s.cloudsigma.com/service_provider/api/v1/user/impersonate", region)

	payload := impersonateRequest{
		UserEmail:    userEmail,
		SubjectToken: subjectToken,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, impersonateURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+rptToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("impersonation request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var impersonateResp impersonateResponse
	if err := json.Unmarshal(body, &impersonateResp); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse impersonation response: %w", err)
	}

	if impersonateResp.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("impersonation response missing access_token")
	}

	// Calculate expiry time
	expiresAt := time.Now().Add(time.Duration(impersonateResp.ExpiresIn) * time.Second)
	if impersonateResp.ExpiresIn == 0 {
		// Default to 15 minutes if not specified
		expiresAt = time.Now().Add(15 * time.Minute)
	}

	klog.V(2).Infof("Successfully impersonated user %s", userEmail)
	return impersonateResp.AccessToken, expiresAt, nil
}

// ClearCache clears all cached tokens
func (c *ImpersonationClient) ClearCache() {
	c.saTokenMutex.Lock()
	c.saToken = ""
	c.saTokenExpiresAt = time.Time{}
	c.saTokenMutex.Unlock()

	c.rptTokenMutex.Lock()
	c.rptToken = ""
	c.rptTokenExpiresAt = time.Time{}
	c.rptTokenMutex.Unlock()

	c.cacheMutex.Lock()
	c.tokenCache = make(map[string]*CachedToken)
	c.cacheMutex.Unlock()

	klog.V(2).Info("Cleared all token caches")
}

// ClearUserToken removes a specific user's cached token
func (c *ImpersonationClient) ClearUserToken(userEmail, region string) {
	cacheKey := fmt.Sprintf("%s:%s", userEmail, region)

	c.cacheMutex.Lock()
	delete(c.tokenCache, cacheKey)
	c.cacheMutex.Unlock()

	klog.V(2).Infof("Cleared cached token for user %s in region %s", userEmail, region)
}

// tokenResponse represents OAuth token endpoint response
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// impersonateRequest represents the impersonation API request body
type impersonateRequest struct {
	UserEmail    string `json:"user_email"`
	SubjectToken string `json:"subject_token"`
}

// impersonateResponse represents the impersonation API response
type impersonateResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
}
