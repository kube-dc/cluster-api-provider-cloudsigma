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

package driver

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

const (
	// DriverName is the name of the CSI driver
	DriverName = "csi.cloudsigma.com"

	// DriverVersion is the version of the CSI driver
	DriverVersion = "0.1.0"

	// TopologyKey is the topology key for CloudSigma region
	TopologyKey = "topology.cloudsigma.com/region"
)

// Mode represents the mode the driver is running in
type Mode string

const (
	// ControllerMode indicates the driver is running as a controller
	ControllerMode Mode = "controller"
	// NodeMode indicates the driver is running as a node plugin
	NodeMode Mode = "node"
	// AllMode indicates the driver is running in both modes
	AllMode Mode = "all"
)

// Driver represents the CloudSigma CSI driver
type Driver struct {
	name     string
	version  string
	nodeID   string
	region   string
	endpoint string
	mode     Mode

	cloudClient *cloudsigma.Client

	srv *grpc.Server

	// CSI capability flags
	controllerCaps []csi.ControllerServiceCapability_RPC_Type
	nodeCaps       []csi.NodeServiceCapability_RPC_Type
	volumeCaps     []csi.VolumeCapability_AccessMode_Mode

	// Mutex for serializing volume attachment per server to prevent race conditions
	serverAttachMu   sync.Mutex
	serverAttachLocks map[string]*sync.Mutex
	
	// Mutex for serializing device discovery on node to prevent race conditions
	nodeDeviceMu sync.Mutex
}

// Config holds the driver configuration
type Config struct {
	Name     string
	Version  string
	NodeID   string
	Region   string
	Endpoint string
	Mode     Mode

	CloudSigmaUsername string
	CloudSigmaPassword string
}

// NewDriver creates a new CloudSigma CSI driver
func NewDriver(cfg *Config) (*Driver, error) {
	klog.Infof("Initializing CloudSigma CSI driver: name=%s, version=%s, nodeID=%s, region=%s, mode=%s",
		cfg.Name, cfg.Version, cfg.NodeID, cfg.Region, cfg.Mode)

	// Create CloudSigma client
	var cloudClient *cloudsigma.Client
	if cfg.CloudSigmaUsername != "" && cfg.CloudSigmaPassword != "" {
		cred := cloudsigma.NewUsernamePasswordCredentialsProvider(cfg.CloudSigmaUsername, cfg.CloudSigmaPassword)
		region := cfg.Region
		if region == "" {
			region = "zrh"
		}
		cloudClient = cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
		klog.Infof("CloudSigma client initialized for region: %s", region)
	}

	driver := &Driver{
		name:              cfg.Name,
		version:           cfg.Version,
		nodeID:            cfg.NodeID,
		region:            cfg.Region,
		endpoint:          cfg.Endpoint,
		mode:              cfg.Mode,
		cloudClient:       cloudClient,
		serverAttachLocks: make(map[string]*sync.Mutex),
	}

	// Set controller capabilities
	driver.controllerCaps = []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
	}

	// Set node capabilities
	driver.nodeCaps = []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
	}

	// Set volume capabilities
	driver.volumeCaps = []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
	}

	return driver, nil
}

// Run starts the CSI driver gRPC server
func (d *Driver) Run() error {
	u, err := url.Parse(d.endpoint)
	if err != nil {
		return err
	}

	var listener net.Listener
	if u.Scheme == "unix" {
		socketPath := u.Path
		// Remove existing socket file
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		// Create parent directory if needed
		if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
			return err
		}
		listener, err = net.Listen("unix", socketPath)
		if err != nil {
			return err
		}
	} else {
		listener, err = net.Listen("tcp", u.Host)
		if err != nil {
			return err
		}
	}

	// Create gRPC server with logging interceptor
	d.srv = grpc.NewServer(
		grpc.UnaryInterceptor(loggingInterceptor),
	)

	// Register CSI services based on mode
	csi.RegisterIdentityServer(d.srv, d)

	switch d.mode {
	case ControllerMode:
		csi.RegisterControllerServer(d.srv, d)
	case NodeMode:
		csi.RegisterNodeServer(d.srv, d)
	case AllMode:
		csi.RegisterControllerServer(d.srv, d)
		csi.RegisterNodeServer(d.srv, d)
	}

	klog.Infof("Starting CSI driver server at %s", d.endpoint)
	return d.srv.Serve(listener)
}

// Stop gracefully stops the driver
func (d *Driver) Stop() {
	if d.srv != nil {
		d.srv.GracefulStop()
	}
}

// loggingInterceptor logs all gRPC calls
func loggingInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	klog.V(4).Infof("gRPC call: %s", info.FullMethod)
	klog.V(6).Infof("gRPC request: %+v", req)

	resp, err := handler(ctx, req)
	if err != nil {
		klog.Errorf("gRPC error: %s: %v", info.FullMethod, err)
	} else {
		klog.V(6).Infof("gRPC response: %+v", resp)
	}

	return resp, err
}
