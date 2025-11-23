package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CloudSigmaMachineTemplateSpec defines the desired state of CloudSigmaMachineTemplate
type CloudSigmaMachineTemplateSpec struct {
	Template CloudSigmaMachineTemplateResource `json:"template"`
}

// CloudSigmaMachineTemplateResource describes the data needed to create a CloudSigmaMachine from a template
type CloudSigmaMachineTemplateResource struct {
	// Spec is the specification of the desired behavior of the machine.
	Spec CloudSigmaMachineSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=cloudsigmamachinetemplates,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion

// CloudSigmaMachineTemplate is the Schema for the cloudsigmamachinetemplates API
type CloudSigmaMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec CloudSigmaMachineTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// CloudSigmaMachineTemplateList contains a list of CloudSigmaMachineTemplate
type CloudSigmaMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudSigmaMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudSigmaMachineTemplate{}, &CloudSigmaMachineTemplateList{})
}
