package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// ServerReadyCondition reports on the successful reconciliation of CloudSigma server
	ServerReadyCondition clusterv1.ConditionType = "ServerReady"

	// ServerCreateFailedReason used when server creation fails
	ServerCreateFailedReason = "ServerCreateFailed"

	// ServerDeleteFailedReason used when server deletion fails
	ServerDeleteFailedReason = "ServerDeleteFailed"
)

// CloudSigmaMachineSpec defines the desired state of CloudSigmaMachine
type CloudSigmaMachineSpec struct {
	// ProviderID is the unique identifier as specified by the cloud provider
	// Format: cloudsigma://server-uuid
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// CPU is the CPU frequency in MHz
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:validation:Maximum=100000
	CPU int `json:"cpu"`

	// Memory is the memory size in MB
	// +kubebuilder:validation:Minimum=512
	// +kubebuilder:validation:Maximum=524288
	Memory int `json:"memory"`

	// Disks defines the disk configuration
	// +kubebuilder:validation:MinItems=1
	Disks []CloudSigmaDisk `json:"disks"`

	// NICs defines the network interface configuration
	// When empty, CloudSigma will auto-assign a public NAT IP
	// +optional
	NICs []CloudSigmaNIC `json:"nics,omitempty"`

	// Tags are metadata tags for the server
	// +optional
	Tags []string `json:"tags,omitempty"`

	// Meta is custom metadata for the server
	// +optional
	Meta map[string]string `json:"meta,omitempty"`
}

// CloudSigmaDisk defines a disk configuration
type CloudSigmaDisk struct {
	// UUID is the drive/image UUID
	UUID string `json:"uuid"`

	// Device is the device type (virtio or ide)
	// +kubebuilder:validation:Enum=virtio;ide
	Device string `json:"device"`

	// BootOrder is the boot priority
	BootOrder int `json:"boot_order"`

	// Size is the disk size in bytes
	Size int64 `json:"size"`
}

// CloudSigmaNIC defines a network interface configuration
type CloudSigmaNIC struct {
	// VLAN is the VLAN UUID
	VLAN string `json:"vlan"`

	// IPv4Conf is the IPv4 configuration
	IPv4Conf CloudSigmaIPConf `json:"ipv4_conf"`
}

// CloudSigmaIPConf defines IP configuration
type CloudSigmaIPConf struct {
	// Conf is the configuration type (dhcp, static, or manual)
	// +kubebuilder:validation:Enum=dhcp;static;manual
	Conf string `json:"conf"`

	// IP is the IP address reference for static configuration
	// +optional
	IP *CloudSigmaIPRef `json:"ip,omitempty"`
}

// CloudSigmaIPRef references an IP address
type CloudSigmaIPRef struct {
	// UUID is the IP address UUID
	UUID string `json:"uuid"`
}

// CloudSigmaMachineStatus defines the observed state of CloudSigmaMachine
type CloudSigmaMachineStatus struct {
	// Ready indicates the machine is ready
	Ready bool `json:"ready"`

	// InstanceID is the CloudSigma server UUID
	// +optional
	InstanceID string `json:"instanceID,omitempty"`

	// InstanceState is the current server state
	// +optional
	InstanceState string `json:"instanceState,omitempty"`

	// Addresses contains the machine's network addresses
	// +optional
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// Conditions defines current service state of the machine
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`

	// FailureReason indicates there is a fatal problem reconciling the machine
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// FailureMessage indicates a human-readable message about why the machine is in a failed state
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=cloudsigmamachines,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels.cluster\\.x-k8s\\.io/cluster-name",description="Cluster"
// +kubebuilder:printcolumn:name="Machine",type="string",JSONPath=".metadata.ownerReferences[?(@.kind==\"Machine\")].name",description="Machine"
// +kubebuilder:printcolumn:name="InstanceID",type="string",JSONPath=".status.instanceID",description="CloudSigma instance ID"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.instanceState",description="CloudSigma instance state"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.ready",description="Machine ready status"

// CloudSigmaMachine is the Schema for the cloudsigmamachines API
type CloudSigmaMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudSigmaMachineSpec   `json:"spec,omitempty"`
	Status CloudSigmaMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CloudSigmaMachineList contains a list of CloudSigmaMachine
type CloudSigmaMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudSigmaMachine `json:"items"`
}

// GetConditions returns the conditions for the CloudSigmaMachine
func (m *CloudSigmaMachine) GetConditions() clusterv1.Conditions {
	return m.Status.Conditions
}

// SetConditions sets the conditions for the CloudSigmaMachine
func (m *CloudSigmaMachine) SetConditions(conditions clusterv1.Conditions) {
	m.Status.Conditions = conditions
}

func init() {
	SchemeBuilder.Register(&CloudSigmaMachine{}, &CloudSigmaMachineList{})
}
