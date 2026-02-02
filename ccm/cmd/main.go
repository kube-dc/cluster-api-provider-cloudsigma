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

package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/kube-dc/cluster-api-provider-cloudsigma/ccm/controllers"
	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/auth"
)

func main() {
	var metricsAddr string
	var probeAddr string
	var clusterName string
	var kubeconfig string
	var cloudsigmaRegion string
	// Impersonation config (default)
	var oauthURL string
	var clientID string
	var clientSecret string
	var userEmail string
	// Legacy credentials (must be explicitly enabled)
	var legacyCredentialsEnabled bool
	var cloudsigmaUsername string
	var cloudsigmaPassword string
	// CSI token provisioning
	var csiTokenEnabled bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&clusterName, "cluster-name", "", "Name of the cluster being managed.")
	flag.StringVar(&kubeconfig, "tenant-kubeconfig", "", "Path to kubeconfig file for connecting to the tenant cluster.")
	flag.StringVar(&cloudsigmaRegion, "cloudsigma-region", os.Getenv("CLOUDSIGMA_REGION"), "CloudSigma region")
	// Impersonation config (default mode)
	flag.StringVar(&oauthURL, "oauth-url", os.Getenv("CLOUDSIGMA_OAUTH_URL"), "CloudSigma OAuth URL")
	flag.StringVar(&clientID, "client-id", os.Getenv("CLOUDSIGMA_CLIENT_ID"), "OAuth client ID")
	flag.StringVar(&clientSecret, "client-secret", os.Getenv("CLOUDSIGMA_CLIENT_SECRET"), "OAuth client secret")
	flag.StringVar(&userEmail, "user-email", os.Getenv("CLOUDSIGMA_USER_EMAIL"), "User email for impersonation")
	// Legacy credentials (must be explicitly enabled)
	flag.BoolVar(&legacyCredentialsEnabled, "enable-legacy-credentials", os.Getenv("CLOUDSIGMA_ENABLE_LEGACY_CREDENTIALS") == "true", "Enable legacy username/password authentication")
	flag.StringVar(&cloudsigmaUsername, "cloudsigma-username", os.Getenv("CLOUDSIGMA_USERNAME"), "CloudSigma API username (only used with --enable-legacy-credentials)")
	flag.StringVar(&cloudsigmaPassword, "cloudsigma-password", os.Getenv("CLOUDSIGMA_PASSWORD"), "CloudSigma API password (only used with --enable-legacy-credentials)")
	// CSI token provisioning
	flag.BoolVar(&csiTokenEnabled, "enable-csi-token", os.Getenv("CLOUDSIGMA_ENABLE_CSI_TOKEN") == "true", "Enable CSI token provisioning - CCM will create and refresh CloudSigma API token for CSI driver")

	flag.Parse()

	if kubeconfig == "" {
		klog.Fatal("--tenant-kubeconfig is required")
	}

	klog.Infof("Starting CloudSigma CCM for cluster: %s", clusterName)
	klog.Infof("Using tenant kubeconfig: %s", kubeconfig)

	// Create context that cancels on SIGTERM/SIGINT
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		klog.Info("Received shutdown signal")
		cancel()
	}()

	// Start health/ready probes
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		klog.Infof("Starting health probe server on %s", probeAddr)
		if err := http.ListenAndServe(probeAddr, mux); err != nil && err != http.ErrServerClosed {
			klog.Errorf("Health probe server error: %v", err)
		}
	}()

	// Start metrics server (simple placeholder)
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		klog.Infof("Starting metrics server on %s", metricsAddr)
		if err := http.ListenAndServe(metricsAddr, mux); err != nil && err != http.ErrServerClosed {
			klog.Errorf("Metrics server error: %v", err)
		}
	}()

	// Setup impersonation (default mode)
	var impersonationClient *auth.ImpersonationClient
	if oauthURL != "" && clientID != "" && clientSecret != "" {
		var err error
		impersonationClient, err = auth.NewImpersonationClient(auth.ImpersonationConfig{
			OAuthURL:     oauthURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		if err != nil {
			klog.Fatalf("Failed to create impersonation client: %v", err)
		}
		klog.Infof("Impersonation mode configured (default), userEmail: %s", userEmail)
	} else {
		klog.Info("Impersonation not configured - CLOUDSIGMA_OAUTH_URL, CLOUDSIGMA_CLIENT_ID, CLOUDSIGMA_CLIENT_SECRET required")
	}

	// Legacy credentials - only used when explicitly enabled
	if legacyCredentialsEnabled {
		if cloudsigmaUsername == "" || cloudsigmaPassword == "" {
			klog.Fatal("Legacy credentials enabled but CLOUDSIGMA_USERNAME and CLOUDSIGMA_PASSWORD not set")
		}
		klog.Infof("Legacy credential fallback ENABLED (explicit), username: %s", cloudsigmaUsername)
	} else {
		// Clear legacy credentials when not explicitly enabled
		cloudsigmaUsername = ""
		cloudsigmaPassword = ""
		klog.Info("Legacy credential fallback DISABLED (default)")
	}

	// Validate we have at least one auth method
	if impersonationClient == nil && !legacyCredentialsEnabled {
		klog.Fatal("No authentication configured. Set impersonation (CLOUDSIGMA_OAUTH_URL, CLOUDSIGMA_CLIENT_ID, CLOUDSIGMA_CLIENT_SECRET) or enable legacy credentials (CLOUDSIGMA_ENABLE_LEGACY_CREDENTIALS=true)")
	}

	klog.Infof("Starting CCM with impersonation=%v, legacyFallback=%v, csiToken=%v", impersonationClient != nil, legacyCredentialsEnabled, csiTokenEnabled)

	// Create and start node reconciler
	reconciler := &controllers.NodeReconciler{
		TenantKubeconfig:         kubeconfig,
		ClusterName:              clusterName,
		CloudSigmaUsername:       cloudsigmaUsername,
		CloudSigmaPassword:       cloudsigmaPassword,
		CloudSigmaRegion:         cloudsigmaRegion,
		ImpersonationClient:      impersonationClient,
		LegacyCredentialsEnabled: legacyCredentialsEnabled,
		UserEmail:                userEmail,
	}

	if err := reconciler.Start(ctx); err != nil {
		klog.Fatalf("Failed to start node reconciler: %v", err)
	}

	// Start CSI token controller if enabled
	if csiTokenEnabled {
		if impersonationClient == nil {
			klog.Fatal("CSI token provisioning requires impersonation mode")
		}
		if userEmail == "" {
			klog.Fatal("CSI token provisioning requires --user-email")
		}

		csiTokenController := &controllers.CSITokenController{
			TenantClient:        reconciler.GetTenantClient(),
			ImpersonationClient: impersonationClient,
			UserEmail:           userEmail,
			Region:              cloudsigmaRegion,
			Enabled:             true,
		}

		if err := csiTokenController.Start(ctx); err != nil {
			klog.Fatalf("Failed to start CSI token controller: %v", err)
		}
		klog.Infof("CSI token controller started for user: %s", userEmail)
	}

	// Wait for context cancellation
	<-ctx.Done()
	klog.Info("CloudSigma CCM shutting down")
}
