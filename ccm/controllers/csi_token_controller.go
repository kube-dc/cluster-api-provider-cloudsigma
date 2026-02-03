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

package controllers

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/auth"
)

const (
	// CSITokenSecretName is the name of the secret containing CSI credentials
	CSITokenSecretName = "cloudsigma-token"
	// CSINamespace is the namespace where CSI driver is deployed
	CSINamespace = "cloudsigma-csi"
	// TokenRefreshInterval is how often to refresh the token
	TokenRefreshInterval = 10 * time.Minute
	// TokenRefreshBuffer is the time before expiry to refresh
	TokenRefreshBuffer = 5 * time.Minute
	// InitialRetryInterval is the starting interval for initial provisioning retries
	InitialRetryInterval = 5 * time.Second
	// MaxRetryInterval is the maximum interval between retries
	MaxRetryInterval = 2 * time.Minute
)

// CSITokenController manages CloudSigma API tokens for the CSI driver
// It uses impersonation to obtain tokens and stores them in the tenant cluster
type CSITokenController struct {
	// TenantClient is the Kubernetes client for the tenant cluster
	TenantClient kubernetes.Interface
	// ImpersonationClient handles OAuth token acquisition
	ImpersonationClient *auth.ImpersonationClient
	// UserEmail is the user to impersonate for CSI operations
	UserEmail string
	// Region is the CloudSigma region
	Region string
	// Enabled indicates if CSI token provisioning is enabled
	Enabled bool
}

// Start begins the CSI token management loop
func (c *CSITokenController) Start(ctx context.Context) error {
	if !c.Enabled {
		klog.Info("CSI token provisioning disabled")
		return nil
	}

	if c.ImpersonationClient == nil {
		return fmt.Errorf("impersonation client required for CSI token provisioning")
	}

	if c.UserEmail == "" {
		return fmt.Errorf("user email required for CSI token provisioning")
	}

	klog.Infof("Starting CSI token controller for user: %s, region: %s", c.UserEmail, c.Region)

	// Start provisioning loop with retry (non-blocking)
	go c.provisioningLoop(ctx)

	return nil
}

// provisioningLoop handles initial provisioning with exponential backoff,
// then switches to regular refresh interval once successful
func (c *CSITokenController) provisioningLoop(ctx context.Context) {
	backoff := InitialRetryInterval
	provisioned := false

	for !provisioned {
		select {
		case <-ctx.Done():
			klog.Info("CSI token provisioning loop stopped (context cancelled)")
			return
		default:
			if err := c.ensureCSIToken(ctx); err != nil {
				klog.Warningf("CSI token provisioning failed (retrying in %v): %v", backoff, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
					// Exponential backoff with cap
					backoff = backoff * 2
					if backoff > MaxRetryInterval {
						backoff = MaxRetryInterval
					}
				}
				continue
			}
			klog.Info("CSI token provisioned successfully, starting refresh loop")
			provisioned = true
		}
	}

	// Once provisioned, switch to normal refresh interval
	c.refreshLoop(ctx)
}

// refreshLoop periodically refreshes the CSI token
func (c *CSITokenController) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(TokenRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Info("CSI token refresh loop stopped")
			return
		case <-ticker.C:
			if err := c.ensureCSIToken(ctx); err != nil {
				klog.Errorf("CSI token refresh failed: %v", err)
			}
		}
	}
}

// ensureCSIToken ensures the CSI token secret exists and is valid
func (c *CSITokenController) ensureCSIToken(ctx context.Context) error {
	klog.V(2).Infof("Ensuring CSI token for user: %s", c.UserEmail)

	// Ensure namespace exists
	if err := c.ensureNamespace(ctx); err != nil {
		return fmt.Errorf("failed to ensure namespace: %w", err)
	}

	// Get impersonated token
	token, err := c.ImpersonationClient.GetImpersonatedToken(ctx, c.UserEmail, c.Region)
	if err != nil {
		return fmt.Errorf("failed to get impersonated token: %w", err)
	}

	// Create or update secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CSITokenSecretName,
			Namespace: CSINamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cloudsigma-ccm",
				"app.kubernetes.io/component":  "csi-credentials",
			},
			Annotations: map[string]string{
				"cloudsigma.com/user-email":   c.UserEmail,
				"cloudsigma.com/region":       c.Region,
				"cloudsigma.com/refreshed-at": time.Now().UTC().Format(time.RFC3339),
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"access_token": token,
			"region":       c.Region,
			"user_email":   c.UserEmail,
		},
	}

	existing, err := c.TenantClient.CoreV1().Secrets(CSINamespace).Get(ctx, CSITokenSecretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Create new secret
			_, err = c.TenantClient.CoreV1().Secrets(CSINamespace).Create(ctx, secret, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create CSI token secret: %w", err)
			}
			klog.Infof("Created CSI token secret in namespace %s", CSINamespace)
			return nil
		}
		return fmt.Errorf("failed to get existing secret: %w", err)
	}

	// Update existing secret
	existing.Data = nil // Clear old data
	existing.StringData = secret.StringData
	existing.Labels = secret.Labels
	existing.Annotations = secret.Annotations

	_, err = c.TenantClient.CoreV1().Secrets(CSINamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update CSI token secret: %w", err)
	}

	klog.V(2).Infof("Updated CSI token secret in namespace %s", CSINamespace)
	return nil
}

// ensureNamespace ensures the CSI namespace exists
func (c *CSITokenController) ensureNamespace(ctx context.Context) error {
	_, err := c.TenantClient.CoreV1().Namespaces().Get(ctx, CSINamespace, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: CSINamespace,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "cloudsigma-ccm",
					},
				},
			}
			_, err = c.TenantClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
			if err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create namespace: %w", err)
			}
			klog.Infof("Created namespace %s", CSINamespace)
			return nil
		}
		return fmt.Errorf("failed to get namespace: %w", err)
	}
	return nil
}
