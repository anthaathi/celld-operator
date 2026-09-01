package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultCelldImage = "ghcr.io/denoland/celld:v0.4.0"
	StorageEphemeral  = "Ephemeral"
	StoragePersistent = "Persistent"
)

// CelldFleetSpec describes one celld fleet and therefore one public application.
type CelldFleetSpec struct {
	// Replicas is the number of celld nodes. Two or more enable low-latency fleet durability.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Image is the celld runtime image.
	// +kubebuilder:default="ghcr.io/denoland/celld:v0.4.0"
	Image string `json:"image,omitempty"`

	ObjectStorage ObjectStorageSpec `json:"objectStorage"`
	LocalStorage  LocalStorageSpec  `json:"localStorage,omitempty"`
	Service       ServiceSpec       `json:"service,omitempty"`

	// Resources configures requests and limits for each celld node.
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type ObjectStorageSpec struct {
	// Bucket is an s3:// bucket with an optional prefix. One prefix is one fleet.
	// +kubebuilder:validation:Pattern=`^s3://[^/]+(/.*)?$`
	Bucket string `json:"bucket"`

	// Endpoint configures an S3-compatible service such as R2 or MinIO.
	Endpoint string `json:"endpoint,omitempty"`

	// Region defaults to us-east-1.
	// +kubebuilder:default="us-east-1"
	Region string `json:"region,omitempty"`

	// CredentialsSecretRef names a Secret whose keys are injected as environment variables.
	// Common keys are AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, and AWS_SESSION_TOKEN.
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`

	// AllowHTTP permits a plain-HTTP S3-compatible endpoint. Use only for local development.
	AllowHTTP bool `json:"allowHTTP,omitempty"`
}

type LocalStorageSpec struct {
	// Type is Ephemeral or Persistent. Ephemeral is intended for this local POC.
	// +kubebuilder:validation:Enum=Ephemeral;Persistent
	// +kubebuilder:default=Ephemeral
	Type string `json:"type,omitempty"`

	// Size is required by Persistent storage and defaults to 10Gi.
	// +kubebuilder:default="10Gi"
	Size string `json:"size,omitempty"`

	StorageClassName *string `json:"storageClassName,omitempty"`
}

type ServiceSpec struct {
	// Type controls the public Worker Service.
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type,omitempty"`
}

type CelldFleetStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	ReadyReplicas      int32              `json:"readyReplicas,omitempty"`
	ServiceName        string             `json:"serviceName,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.objectStorage.bucket`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type CelldFleet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CelldFleetSpec   `json:"spec,omitempty"`
	Status CelldFleetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CelldFleetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CelldFleet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CelldFleet{}, &CelldFleetList{})
}
