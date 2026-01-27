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
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1 "github.com/kube-dc/cluster-api-provider-cloudsigma/api/v1beta1"
	"github.com/kube-dc/cluster-api-provider-cloudsigma/controllers"
	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/auth"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(infrav1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	// Legacy credential-based authentication
	var cloudsigmaUsername string
	var cloudsigmaPassword string
	var cloudsigmaRegion string

	// Impersonation-based authentication (preferred)
	var impersonationEnabled bool
	var oauthURL string
	var clientID string
	var clientSecret string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	// Legacy credentials (deprecated when impersonation is enabled)
	flag.StringVar(&cloudsigmaUsername, "cloudsigma-username", os.Getenv("CLOUDSIGMA_USERNAME"), "CloudSigma API username (legacy mode)")
	flag.StringVar(&cloudsigmaPassword, "cloudsigma-password", os.Getenv("CLOUDSIGMA_PASSWORD"), "CloudSigma API password (legacy mode)")
	flag.StringVar(&cloudsigmaRegion, "cloudsigma-region", os.Getenv("CLOUDSIGMA_REGION"), "CloudSigma region (default: zrh)")

	// Impersonation configuration
	flag.BoolVar(&impersonationEnabled, "impersonation-enabled", os.Getenv("CLOUDSIGMA_IMPERSONATION_ENABLED") == "true", "Enable OAuth impersonation mode")
	flag.StringVar(&oauthURL, "oauth-url", os.Getenv("CLOUDSIGMA_OAUTH_URL"), "CloudSigma OAuth/Keycloak URL for impersonation")
	flag.StringVar(&clientID, "client-id", os.Getenv("CLOUDSIGMA_CLIENT_ID"), "Service account client ID for impersonation")
	flag.StringVar(&clientSecret, "client-secret", os.Getenv("CLOUDSIGMA_CLIENT_SECRET"), "Service account client secret for impersonation")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Determine authentication mode
	var impersonationClient *auth.ImpersonationClient

	if impersonationEnabled {
		// Validate impersonation configuration
		if oauthURL == "" || clientID == "" || clientSecret == "" {
			setupLog.Error(nil, "Impersonation mode requires CLOUDSIGMA_OAUTH_URL, CLOUDSIGMA_CLIENT_ID, and CLOUDSIGMA_CLIENT_SECRET")
			os.Exit(1)
		}

		var err error
		impersonationClient, err = auth.NewImpersonationClient(auth.ImpersonationConfig{
			OAuthURL:     oauthURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
		})
		if err != nil {
			setupLog.Error(err, "Failed to create impersonation client")
			os.Exit(1)
		}

		setupLog.Info("Impersonation mode enabled", "oauthURL", oauthURL, "clientID", clientID)
	} else {
		// Validate legacy credentials
		if cloudsigmaUsername == "" || cloudsigmaPassword == "" {
			setupLog.Error(nil, "CloudSigma credentials are required. Set CLOUDSIGMA_USERNAME and CLOUDSIGMA_PASSWORD, or enable impersonation mode.")
			os.Exit(1)
		}
		setupLog.Info("Legacy credential mode enabled")
	}

	if cloudsigmaRegion == "" {
		cloudsigmaRegion = "zrh" // Default to Zurich
	}

	setupLog.Info("Starting CAPCS", "region", cloudsigmaRegion, "impersonation", impersonationEnabled)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "cloudsigma.cluster.x-k8s.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.CloudSigmaClusterReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		CloudSigmaUsername:  cloudsigmaUsername,
		CloudSigmaPassword:  cloudsigmaPassword,
		CloudSigmaRegion:    cloudsigmaRegion,
		ImpersonationClient: impersonationClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CloudSigmaCluster")
		os.Exit(1)
	}

	if err = (&controllers.CloudSigmaMachineReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		CloudSigmaUsername:  cloudsigmaUsername,
		CloudSigmaPassword:  cloudsigmaPassword,
		CloudSigmaRegion:    cloudsigmaRegion,
		ImpersonationClient: impersonationClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CloudSigmaMachine")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
