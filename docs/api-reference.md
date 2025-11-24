# CloudSigma CAPI Provider - CRD Design

## CloudSigmaMachine

Represents a single CloudSigma server instance.

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaMachine
metadata:
  name: cluster-worker-0
  namespace: default
  labels:
    cluster.x-k8s.io/cluster-name: my-cluster
    cluster.x-k8s.io/machine-pool: workers
spec:
  # CloudSigma server configuration
  cpu: 2000  # MHz (required)
  memory: 4096  # MB (required)
  
  # Disk configuration
  disks:
    - uuid: "boot-image-uuid"  # Boot drive UUID
      device: "virtio"  # virtio or ide
      boot_order: 1
      size: 53687091200  # 50GB in bytes
  
  # Network interface cards
  nics:
    - vlan: "vlan-uuid"
      ipv4_conf:
        conf: "dhcp"  # dhcp, static, or manual
  
  # Optional: Static IP configuration
  # nics:
  #   - vlan: "vlan-uuid"
  #     ipv4_conf:
  #       conf: "static"
  #       ip:
  #         uuid: "ip-uuid"
  
  # Server metadata
  tags:
    - kubernetes
    - worker
    - production
  
  meta:
    cluster: "my-cluster"
    pool: "workers"
  
  # Provider ID (set by controller)
  providerID: ""  # Format: cloudsigma://server-uuid
  
status:
  ready: false
  
  # Server UUID from CloudSigma
  instanceID: ""
  
  # Current server state
  instanceState: ""  # stopped, starting, running, stopping, unavailable
  
  # Network addresses
  addresses:
    - type: InternalIP
      address: "10.220.0.5"
    - type: ExternalIP
      address: "185.12.34.56"
  
  # Conditions
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2025-11-23T10:00:00Z"
      reason: "ServerRunning"
      message: "CloudSigma server is running"
    - type: BootstrapDataReady
      status: "True"
      lastTransitionTime: "2025-11-23T09:58:00Z"
      reason: "BootstrapDataProvided"
    - type: InfrastructureReady
      status: "True"
      lastTransitionTime: "2025-11-23T09:59:30Z"
      reason: "ServerProvisioned"
```

### Fields Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.cpu` | int | Yes | CPU frequency in MHz (e.g., 2000 = 2GHz) |
| `spec.memory` | int | Yes | Memory in MB (e.g., 4096 = 4GB) |
| `spec.disks` | []Disk | Yes | Disk configuration, must include boot disk |
| `spec.disks[].uuid` | string | Yes | Drive/image UUID from CloudSigma |
| `spec.disks[].device` | string | Yes | Device type: virtio (recommended) or ide |
| `spec.disks[].boot_order` | int | Yes | Boot order (1 for primary boot) |
| `spec.disks[].size` | int64 | Yes | Disk size in bytes |
| `spec.nics` | []NIC | Yes | Network interface configuration |
| `spec.nics[].vlan` | string | Yes | VLAN UUID |
| `spec.nics[].ipv4_conf.conf` | string | Yes | IP config: dhcp, static, manual |
| `spec.tags` | []string | No | CloudSigma tags for organization |
| `spec.meta` | map[string]string | No | Custom metadata |
| `spec.providerID` | string | No | Set by controller after creation |

---

## CloudSigmaCluster

Represents cluster-wide CloudSigma infrastructure (network, load balancer).

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  # Control plane endpoint (from Kamaji)
  controlPlaneEndpoint:
    host: "cluster-api.example.com"
    port: 6443
  
  # CloudSigma region/datacenter
  region: "zrh"  # zrh (Zurich), lvs (Las Vegas), sjc (San Jose), tyo (Tokyo)
  
  # VLAN configuration
  vlan:
    # Option 1: Use existing VLAN
    uuid: "existing-vlan-uuid"
    
    # Option 2: Create new VLAN
    # name: "my-cluster-vlan"
    # cidr: "10.220.0.0/16"
  
  # Load balancer for worker node traffic (optional)
  loadBalancer:
    enabled: false
    # type: "tcp"  # tcp or http
    # backends: []  # Populated by controller
  
  # API credentials (from Secret)
  credentialsRef:
    name: cloudsigma-credentials
    namespace: kube-system
  
status:
  ready: false
  
  # VLAN information
  network:
    vlanUUID: "vlan-uuid-12345"
    cidr: "10.220.0.0/16"
  
  # Load balancer info
  loadBalancer:
    ip: "185.12.34.100"
    ready: true
  
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2025-11-23T09:55:00Z"
      reason: "InfrastructureReady"
    - type: NetworkReady
      status: "True"
      lastTransitionTime: "2025-11-23T09:55:00Z"
      reason: "VLANProvisioned"
```

### Fields Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.controlPlaneEndpoint` | Endpoint | Yes | Control plane API server endpoint |
| `spec.region` | string | Yes | CloudSigma datacenter (zrh, lvs, sjc, tyo) |
| `spec.vlan.uuid` | string | No | Existing VLAN UUID (mutually exclusive with name/cidr) |
| `spec.vlan.name` | string | No | New VLAN name (requires cidr) |
| `spec.vlan.cidr` | string | No | CIDR for new VLAN (e.g., "10.220.0.0/16") |
| `spec.loadBalancer.enabled` | bool | No | Enable load balancer for workers |
| `spec.credentialsRef` | ObjectRef | Yes | Secret containing CloudSigma credentials |

---

## CloudSigmaMachineTemplate

Template for creating CloudSigmaMachine instances.

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaMachineTemplate
metadata:
  name: worker-template
  namespace: default
spec:
  template:
    spec:
      cpu: 2000
      memory: 4096
      disks:
        - uuid: "k8s-worker-image-uuid"
          device: "virtio"
          boot_order: 1
          size: 53687091200  # 50GB
      nics:
        - vlan: "vlan-uuid"
          ipv4_conf:
            conf: "dhcp"
      tags:
        - kubernetes
        - worker
```

### Usage with MachineDeployment

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: my-cluster-workers
  namespace: default
spec:
  clusterName: my-cluster
  replicas: 3
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
      cluster.x-k8s.io/deployment-name: my-cluster-workers
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: my-cluster
        cluster.x-k8s.io/deployment-name: my-cluster-workers
    spec:
      clusterName: my-cluster
      version: v1.34.0
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfigTemplate
          name: my-cluster-workers-bootstrap
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: CloudSigmaMachineTemplate
        name: worker-template
```

---

## Go Type Definitions

```go
package v1beta1

import (
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// CloudSigmaMachine represents a CloudSigma server
type CloudSigmaMachine struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    Spec   CloudSigmaMachineSpec   `json:"spec,omitempty"`
    Status CloudSigmaMachineStatus `json:"status,omitempty"`
}

type CloudSigmaMachineSpec struct {
    // CPU in MHz
    CPU int `json:"cpu"`
    
    // Memory in MB
    Memory int `json:"memory"`
    
    // Disk configuration
    Disks []CloudSigmaDisk `json:"disks"`
    
    // Network interfaces
    NICs []CloudSigmaNIC `json:"nics"`
    
    // Tags
    Tags []string `json:"tags,omitempty"`
    
    // Metadata
    Meta map[string]string `json:"meta,omitempty"`
    
    // ProviderID
    ProviderID *string `json:"providerID,omitempty"`
}

type CloudSigmaDisk struct {
    UUID      string `json:"uuid"`
    Device    string `json:"device"`
    BootOrder int    `json:"boot_order"`
    Size      int64  `json:"size"`
}

type CloudSigmaNIC struct {
    VLAN      string           `json:"vlan"`
    IPv4Conf  CloudSigmaIPConf `json:"ipv4_conf"`
}

type CloudSigmaIPConf struct {
    Conf string              `json:"conf"` // dhcp, static, manual
    IP   *CloudSigmaIPRef    `json:"ip,omitempty"`
}

type CloudSigmaIPRef struct {
    UUID string `json:"uuid"`
}

type CloudSigmaMachineStatus struct {
    Ready         bool                       `json:"ready"`
    InstanceID    string                     `json:"instanceID,omitempty"`
    InstanceState string                     `json:"instanceState,omitempty"`
    Addresses     []clusterv1.MachineAddress `json:"addresses,omitempty"`
    Conditions    clusterv1.Conditions       `json:"conditions,omitempty"`
}

// CloudSigmaCluster represents cluster-wide infrastructure
type CloudSigmaCluster struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    Spec   CloudSigmaClusterSpec   `json:"spec,omitempty"`
    Status CloudSigmaClusterStatus `json:"status,omitempty"`
}

type CloudSigmaClusterSpec struct {
    ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint"`
    Region               string                 `json:"region"`
    VLAN                 CloudSigmaVLANSpec    `json:"vlan"`
    LoadBalancer         *LoadBalancerSpec     `json:"loadBalancer,omitempty"`
    CredentialsRef       *ObjectReference      `json:"credentialsRef"`
}

type CloudSigmaVLANSpec struct {
    UUID string `json:"uuid,omitempty"`
    Name string `json:"name,omitempty"`
    CIDR string `json:"cidr,omitempty"`
}

type LoadBalancerSpec struct {
    Enabled bool   `json:"enabled"`
    Type    string `json:"type,omitempty"`
}

type ObjectReference struct {
    Name      string `json:"name"`
    Namespace string `json:"namespace"`
}

type CloudSigmaClusterStatus struct {
    Ready        bool                 `json:"ready"`
    Network      NetworkStatus        `json:"network,omitempty"`
    LoadBalancer *LoadBalancerStatus  `json:"loadBalancer,omitempty"`
    Conditions   clusterv1.Conditions `json:"conditions,omitempty"`
}

type NetworkStatus struct {
    VLANUUID string `json:"vlanUUID,omitempty"`
    CIDR     string `json:"cidr,omitempty"`
}

type LoadBalancerStatus struct {
    IP    string `json:"ip,omitempty"`
    Ready bool   `json:"ready"`
}
```

---

## Validation Rules

### CloudSigmaMachine

```go
// +kubebuilder:validation:Minimum=1000
// +kubebuilder:validation:Maximum=100000
CPU int `json:"cpu"`

// +kubebuilder:validation:Minimum=512
// +kubebuilder:validation:Maximum=524288
Memory int `json:"memory"`

// +kubebuilder:validation:MinItems=1
Disks []CloudSigmaDisk `json:"disks"`

// +kubebuilder:validation:MinItems=1
NICs []CloudSigmaNIC `json:"nics"`

// +kubebuilder:validation:Enum=virtio;ide
Device string `json:"device"`

// +kubebuilder:validation:Enum=dhcp;static;manual
Conf string `json:"conf"`
```

### CloudSigmaCluster

```go
// +kubebuilder:validation:Enum=zrh;lvs;sjc;tyo
Region string `json:"region"`

// +kubebuilder:validation:Pattern=`^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$`
CIDR string `json:"cidr,omitempty"`
```

---

## Controller Contracts

### CloudSigmaMachine Controller

**Responsibilities:**
1. Create CloudSigma server with bootstrap data (cloud-init)
2. Start server
3. Wait for server to reach "running" state
4. Retrieve and set machine addresses
5. Set providerID: `cloudsigma://<server-uuid>`
6. Handle graceful deletion with finalizer

**Status Conditions:**
- `Ready`: True when server is running and ready
- `BootstrapDataReady`: True when bootstrap secret is available
- `InfrastructureReady`: True when server is provisioned

### CloudSigmaCluster Controller

**Responsibilities:**
1. Create or validate VLAN
2. Configure network (if creating new VLAN)
3. Optionally create load balancer
4. Mark cluster as ready

**Status Conditions:**
- `Ready`: True when all infrastructure is ready
- `NetworkReady`: True when VLAN is configured
- `LoadBalancerReady`: True when LB is configured (if enabled)
