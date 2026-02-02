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

	"k8s.io/klog/v2"

	"github.com/kube-dc/cluster-api-provider-cloudsigma/csi/driver"
)

func main() {
	var endpoint string
	var region string
	var cloudsigmaUsername string
	var cloudsigmaPassword string
	var cloudsigmaToken string
	var tokenFile string

	flag.StringVar(&endpoint, "endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	flag.StringVar(&region, "region", os.Getenv("CLOUDSIGMA_REGION"), "CloudSigma region")
	flag.StringVar(&cloudsigmaUsername, "cloudsigma-username", os.Getenv("CLOUDSIGMA_USERNAME"), "CloudSigma API username (legacy)")
	flag.StringVar(&cloudsigmaPassword, "cloudsigma-password", os.Getenv("CLOUDSIGMA_PASSWORD"), "CloudSigma API password (legacy)")
	flag.StringVar(&cloudsigmaToken, "cloudsigma-token", os.Getenv("CLOUDSIGMA_ACCESS_TOKEN"), "CloudSigma API access token (recommended)")
	flag.StringVar(&tokenFile, "token-file", os.Getenv("CLOUDSIGMA_TOKEN_FILE"), "Path to file containing access token (refreshed by CCM)")

	klog.InitFlags(nil)
	flag.Parse()

	// Token-based auth takes priority
	if cloudsigmaToken == "" && tokenFile != "" {
		// Read token from file (CCM refreshes this)
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			klog.Warningf("Failed to read token file %s: %v", tokenFile, err)
		} else {
			cloudsigmaToken = string(data)
			klog.Infof("Loaded access token from file: %s", tokenFile)
		}
	}

	// Validate we have some auth method
	if cloudsigmaToken == "" && (cloudsigmaUsername == "" || cloudsigmaPassword == "") {
		klog.Fatal("CloudSigma credentials required: set CLOUDSIGMA_ACCESS_TOKEN or CLOUDSIGMA_USERNAME/CLOUDSIGMA_PASSWORD")
	}

	klog.Infof("Starting CloudSigma CSI Controller")
	klog.Infof("Endpoint: %s", endpoint)
	klog.Infof("Region: %s", region)

	cfg := &driver.Config{
		Name:               driver.DriverName,
		Version:            driver.DriverVersion,
		Endpoint:           endpoint,
		Region:             region,
		Mode:               driver.ControllerMode,
		CloudSigmaUsername: cloudsigmaUsername,
		CloudSigmaPassword: cloudsigmaPassword,
		CloudSigmaToken:    cloudsigmaToken,
		TokenFile:          tokenFile,
	}

	drv, err := driver.NewDriver(cfg)
	if err != nil {
		klog.Fatalf("Failed to create driver: %v", err)
	}

	if err := drv.Run(); err != nil {
		klog.Fatalf("Failed to run driver: %v", err)
	}
}
