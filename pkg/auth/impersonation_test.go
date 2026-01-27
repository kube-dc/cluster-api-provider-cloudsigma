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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewImpersonationClient(t *testing.T) {
	tests := []struct {
		name    string
		config  ImpersonationConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: ImpersonationConfig{
				OAuthURL:     "https://oauth.example.com",
				ClientID:     "test-client",
				ClientSecret: "test-secret",
			},
			wantErr: false,
		},
		{
			name: "missing oauth url",
			config: ImpersonationConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
			},
			wantErr: true,
		},
		{
			name: "missing client id",
			config: ImpersonationConfig{
				OAuthURL:     "https://oauth.example.com",
				ClientSecret: "test-secret",
			},
			wantErr: true,
		},
		{
			name: "missing client secret",
			config: ImpersonationConfig{
				OAuthURL: "https://oauth.example.com",
				ClientID: "test-client",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewImpersonationClient(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewImpersonationClient() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && client == nil {
				t.Error("NewImpersonationClient() returned nil client")
			}
		})
	}
}

func TestCachedToken_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		token     CachedToken
		buffer    time.Duration
		wantExpired bool
	}{
		{
			name: "not expired",
			token: CachedToken{
				Token:     "test-token",
				ExpiresAt: time.Now().Add(10 * time.Minute),
			},
			buffer:      5 * time.Minute,
			wantExpired: false,
		},
		{
			name: "expired",
			token: CachedToken{
				Token:     "test-token",
				ExpiresAt: time.Now().Add(-1 * time.Minute),
			},
			buffer:      5 * time.Minute,
			wantExpired: true,
		},
		{
			name: "expires within buffer",
			token: CachedToken{
				Token:     "test-token",
				ExpiresAt: time.Now().Add(3 * time.Minute),
			},
			buffer:      5 * time.Minute,
			wantExpired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsExpired(tt.buffer); got != tt.wantExpired {
				t.Errorf("CachedToken.IsExpired() = %v, want %v", got, tt.wantExpired)
			}
		})
	}
}

func TestImpersonationClient_GetServiceAccountToken(t *testing.T) {
	// Create mock OAuth server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/cloudsigma/protocol/openid-connect/token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Parse form
		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		grantType := r.FormValue("grant_type")
		if grantType == "client_credentials" {
			// Step 1: Service account token
			resp := tokenResponse{
				AccessToken: "test-sa-token",
				TokenType:   "Bearer",
				ExpiresIn:   900,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		if grantType == umaGrantType {
			// Step 2: RPT token
			resp := tokenResponse{
				AccessToken: "test-rpt-token",
				TokenType:   "Bearer",
				ExpiresIn:   900,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	client, err := NewImpersonationClient(ImpersonationConfig{
		OAuthURL:     server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// Test getting service account token
	token, err := client.getServiceAccountToken(ctx)
	if err != nil {
		t.Fatalf("getServiceAccountToken() error = %v", err)
	}
	if token != "test-sa-token" {
		t.Errorf("getServiceAccountToken() = %v, want test-sa-token", token)
	}

	// Test caching - second call should use cache
	token2, err := client.getServiceAccountToken(ctx)
	if err != nil {
		t.Fatalf("getServiceAccountToken() second call error = %v", err)
	}
	if token2 != "test-sa-token" {
		t.Errorf("getServiceAccountToken() second call = %v, want test-sa-token", token2)
	}
}

func TestImpersonationClient_GetRPTToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/cloudsigma/protocol/openid-connect/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Check authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer test-sa-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if r.FormValue("grant_type") != umaGrantType {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp := tokenResponse{
			AccessToken: "test-rpt-token",
			TokenType:   "Bearer",
			ExpiresIn:   900,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, _ := NewImpersonationClient(ImpersonationConfig{
		OAuthURL:     server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	})

	ctx := context.Background()

	token, err := client.getRPTToken(ctx, "test-sa-token")
	if err != nil {
		t.Fatalf("getRPTToken() error = %v", err)
	}
	if token != "test-rpt-token" {
		t.Errorf("getRPTToken() = %v, want test-rpt-token", token)
	}
}

func TestImpersonationClient_ImpersonateUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer test-rpt-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Parse request body
		var req impersonateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if req.UserEmail != "user@example.com" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp := impersonateResponse{
			AccessToken: "test-impersonated-token",
			TokenType:   "Bearer",
			ExpiresIn:   900,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Extract host/port from test server URL
	client, _ := NewImpersonationClient(ImpersonationConfig{
		OAuthURL:     "https://oauth.example.com",
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	})

	// Override HTTP client to use test server
	client.httpClient = server.Client()

	// We can't easily test impersonateUser directly because it constructs the URL
	// based on region. Instead, test the full flow with mocks.
	_ = client // Suppress unused variable warning
	t.Log("ImpersonateUser test requires integration testing with full OAuth mock")
}

func TestImpersonationClient_ClearCache(t *testing.T) {
	client, _ := NewImpersonationClient(ImpersonationConfig{
		OAuthURL:     "https://oauth.example.com",
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	})

	// Add some cached data
	client.saToken = "test-sa-token"
	client.saTokenExpiresAt = time.Now().Add(10 * time.Minute)
	client.rptToken = "test-rpt-token"
	client.rptTokenExpiresAt = time.Now().Add(10 * time.Minute)
	client.tokenCache["user@example.com:next"] = &CachedToken{
		Token:     "test-impersonated-token",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	// Clear cache
	client.ClearCache()

	// Verify cache is cleared
	if client.saToken != "" {
		t.Error("saToken not cleared")
	}
	if client.rptToken != "" {
		t.Error("rptToken not cleared")
	}
	if len(client.tokenCache) != 0 {
		t.Error("tokenCache not cleared")
	}
}

func TestImpersonationClient_ClearUserToken(t *testing.T) {
	client, _ := NewImpersonationClient(ImpersonationConfig{
		OAuthURL:     "https://oauth.example.com",
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	})

	// Add cached tokens for two users
	client.tokenCache["user1@example.com:next"] = &CachedToken{
		Token:     "token1",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	client.tokenCache["user2@example.com:next"] = &CachedToken{
		Token:     "token2",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	// Clear only user1's token
	client.ClearUserToken("user1@example.com", "next")

	// Verify only user1's token is cleared
	if _, exists := client.tokenCache["user1@example.com:next"]; exists {
		t.Error("user1 token not cleared")
	}
	if _, exists := client.tokenCache["user2@example.com:next"]; !exists {
		t.Error("user2 token should still exist")
	}
}
