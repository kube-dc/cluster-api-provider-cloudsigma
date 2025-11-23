package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// NetworkReadyCondition reports on the successful reconciliation of CloudSigma network
	NetworkReadyCondition clusterv1.ConditionType = "NetworkReady"

	// NetworkCreateFailedReason used when network/VLAN creation fails
	NetworkCreateFailedReason = "NetworkCreateFailed"
)

// CloudSigmaClusterSpec defines the desired state of CloudSigmaCluster
type CloudSigmaClusterSpec struct {
	// ControlPlaneEndpoint represents the endpoint used to communicate with the control plane.
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint"`

	// Region is the CloudSigma datacenter region (e.g., "zrh", "fra", "next")
	// +kubebuilder:validation:Required
	Region string `json:"region"`

	// VLAN specifies the VLAN configuration for the cluster network
	// +optional
	VLAN *VLANSpec `json:"vlan,omitempty"`

	// LoadBalancer specifies the load balancer configuration
	// +optional
	LoadBalancer *LoadBalancerSpec `json:"loadBalancer,omitempty"`

	// CredentialsRef is a reference to a Secret containing CloudSigma credentials
	// +optional
	CredentialsRef *ObjectReference `json:"credentialsRef,omitempty"`
}

// VLANSpec defines the VLAN configuration
type VLANSpec struct {
	// UUID is the existing VLAN UUID to use
	// +optional
	UUID string `json:"uuid,omitempty"`

	// Name is the name for a new VLAN to create
	// +optional
	Name string `json:"name,omitempty"`

	// CIDR is the IP range for a new VLAN (e.g., "10.220.0.0/16")
	// +optional
	// +kubebuilder:validation:Pattern=`^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$`
	CIDR string `json:"cidr,omitempty"`
}

// LoadBalancerSpec defines the load balancer configuration
type LoadBalancerSpec struct {
	// Enabled specifies whether to create a load balancer
	Enabled bool `json:"enabled"`

	// Type specifies the load balancer type (tcp or http)
	// +optional
	// +kubebuilder:validation:Enum=tcp;http
	Type string `json:"type,omitempty"`
}

// ObjectReference contains information to locate a referenced object
type ObjectReference struct {
	// Name of the referenced object
	Name string `json:"name"`

	// Namespace of the referenced object
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// CloudSigmaClusterStatus defines the observed state of CloudSigmaCluster
type CloudSigmaClusterStatus struct {
	// Ready indicates the cluster infrastructure is ready
	Ready bool `json:"ready"`

	// Network contains the cluster network information
	// +optional
	Network *NetworkStatus `json:"network,omitempty"`

	// LoadBalancer contains the load balancer information
	// +optional
	LoadBalancer *LoadBalancerStatus `json:"loadBalancer,omitempty"`

	// Conditions defines current service state of the cluster
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`

	// FailureReason indicates there is a fatal problem reconciling the cluster
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// FailureMessage indicates a human-readable message about why the cluster is in a failed state
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// NetworkStatus contains cluster network status information
type NetworkStatus struct {
	// VLANUUID is the UUID of the VLAN
	// +optional
	VLANUUID string `json:"vlanUUID,omitempty"`

	// CIDR is the IP range of the network
	// +optional
	CIDR string `json:"cidr,omitempty"`
}

// LoadBalancerStatus contains load balancer status information
type LoadBalancerStatus struct {
	// IP is the load balancer IP address
	// +optional
	IP string `json:"ip,omitempty"`

	// Ready indicates the load balancer is ready
	Ready bool `json:"ready"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=cloudsigmaclusters,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels.cluster\\.x-k8s\\.io/cluster-name",description="Cluster"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.ready",description="Cluster infrastructure is ready"
// +kubebuilder:printcolumn:name="Region",type="string",JSONPath=".spec.region",description="CloudSigma region"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".spec.controlPlaneEndpoint.host",description="Control plane endpoint"

// CloudSigmaCluster is the Schema for the cloudsigmaclusters API
type CloudSigmaCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudSigmaClusterSpec   `json:"spec,omitempty"`
	Status CloudSigmaClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CloudSigmaClusterList contains a list of CloudSigmaCluster
type CloudSigmaClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudSigmaCluster `json:"items"`
}

// GetConditions returns the conditions for the CloudSigmaCluster
func (c *CloudSigmaCluster) GetConditions() clusterv1.Conditions {
	return c.Status.Conditions
}

// SetConditions sets the conditions for the CloudSigmaCluster
func (c *CloudSigmaCluster) SetConditions(conditions clusterv1.Conditions) {
	c.Status.Conditions = conditions
}

func init() {
	SchemeBuilder.Register(&CloudSigmaCluster{}, &CloudSigmaClusterList{})
}
