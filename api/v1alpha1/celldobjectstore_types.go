package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CelldObjectStore describes one S3-compatible storage backend shared by many
// CelldFleets. Define the endpoint, region, and credentials once and reference
// the store from each fleet with spec.storeRef, keeping only the bucket prefix
// on the fleet.
type CelldObjectStoreSpec struct {
	// Endpoint configures an S3-compatible service such as AWS S3, Cloudflare
	// R2, or MinIO. Leave empty for AWS S3 itself.
	Endpoint string `json:"endpoint,omitempty"`

	// Region defaults to us-east-1.
	// +kubebuilder:validation:default="us-east-1"
	Region string `json:"region,omitempty"`

	// CredentialsSecretRef names a Secret whose keys are injected as
	// environment variables on the celld nodes and deploy Jobs. Common keys
	// are AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, and AWS_SESSION_TOKEN.
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`

	// AllowHTTP permits a plain-HTTP S3-compatible endpoint. Use only for
	// local development.
	AllowHTTP bool `json:"allowHTTP,omitempty"`
}

type CelldObjectStoreStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpoint`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.spec.region`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type CelldObjectStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CelldObjectStoreSpec   `json:"spec,omitempty"`
	Status CelldObjectStoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CelldObjectStoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CelldObjectStore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CelldObjectStore{}, &CelldObjectStoreList{})
}
