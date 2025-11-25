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
)

func main() {
	var metricsAddr string
	var probeAddr string
	var clusterName string
	var kubeconfig string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&clusterName, "cluster-name", "", "Name of the cluster being managed.")
	flag.StringVar(&kubeconfig, "tenant-kubeconfig", "", "Path to kubeconfig file for connecting to the tenant cluster.")

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

	// Create and start node reconciler
	reconciler := &controllers.NodeReconciler{
		TenantKubeconfig: kubeconfig,
		ClusterName:      clusterName,
	}

	if err := reconciler.Start(ctx); err != nil {
		klog.Fatalf("Failed to start node reconciler: %v", err)
	}

	// Wait for context cancellation
	<-ctx.Done()
	klog.Info("CloudSigma CCM shutting down")
}
