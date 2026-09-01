package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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

	// StoreRef references a CelldObjectStore that provides the shared
	// connection configuration (endpoint, region, credentials, allowHTTP) for
	// this fleet's object storage. When set, objectStorage.bucket is still
	// required, but objectStorage connection fields (endpoint, region,
	// allowHTTP, credentialsSecretRef) must be empty — they come from the store.
	StoreRef *corev1.LocalObjectReference `json:"storeRef,omitempty"`

	ObjectStorage ObjectStorageSpec `json:"objectStorage"`
	LocalStorage  LocalStorageSpec  `json:"localStorage,omitempty"`
	Service       ServiceSpec       `json:"service,omitempty"`

	// Ingress optionally configures automatic external exposure of the public
	// Worker Service through a classic Ingress or a Gateway API HTTPRoute.
	Ingress *IngressSpec `json:"ingress,omitempty"`

	// Resources configures requests and limits for each celld node.
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type ObjectStorageSpec struct {
	// Bucket is an s3:// bucket with an optional prefix. One prefix is one fleet.
	// +kubebuilder:validation:Pattern=`^s3://[^/]+(/.*)?$`
	Bucket string `json:"bucket"`

	// Endpoint configures an S3-compatible service such as R2 or MinIO.
	// Leave empty when spec.storeRef is set; the store provides the endpoint.
	Endpoint string `json:"endpoint,omitempty"`

	// Region defaults to us-east-1. Leave empty when spec.storeRef is set;
	// the store provides the region. The default is applied at resolution
	// time rather than by the API server, so fleets using storeRef can keep
	// objectStorage free of connection fields.
	Region string `json:"region,omitempty"`

	// CredentialsSecretRef names a Secret whose keys are injected as environment variables.
	// Common keys are AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, and AWS_SESSION_TOKEN.
	// Leave empty when spec.storeRef is set; the store provides the secret.
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`

	// AllowHTTP permits a plain-HTTP S3-compatible endpoint. Use only for local development.
	// Leave false when spec.storeRef is set; the store provides the setting.
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

// IngressSpec describes automatic external exposure of the public Worker Service.
// When set, the operator creates an Ingress and/or Gateway API HTTPRoute for the
// fleet, so a fleet behaves like a Knative Service: apply a hostname, get a URL.
// Exactly one of ingressClass or gatewayRefs must be set.
type IngressSpec struct {
	// Hostname is the fully qualified public hostname for this fleet, for
	// example demo.example.com. It is used for Ingress rules and HTTPRoute
	// hostnames, and for the https:// URL reported in status.
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`

	// Path is an optional path prefix routed to the fleet. Defaults to "/".
	// +kubebuilder:validation:Pattern=`^/.*`
	// +kubebuilder:default=/
	Path string `json:"path,omitempty"`

	// StripPrefix removes the Path prefix from the request before it reaches
	// the Worker. This lets one hostname serve multiple fleets, each mounted
	// at its own subpath: a request to /api/hello with Path=/api and
	// StripPrefix=true reaches the Worker as /hello. It is implemented with
	// the nginx rewrite annotation for Ingress, and URL rewrite filters for
	// Gateway API HTTPRoutes. Defaults to false.
	StripPrefix *bool `json:"stripPrefix,omitempty"`

	// PathType for the Ingress rule. Defaults to Prefix.
	// +kubebuilder:validation:Enum=Exact;Prefix;ImplementationSpecific
	// +kubebuilder:default=Prefix
	PathType networkingv1.PathType `json:"pathType,omitempty"`

	// IngressClass is the spec.ingressClassName of the created Ingress. Set
	// this to use classic Ingress (nginx, traefik, kong, ...). Exactly one of
	// ingressClass and gatewayRefs must be set.
	IngressClass string `json:"ingressClass,omitempty"`

	// GatewayRefs attaches the fleet to one or more existing Gateways by
	// namespace/name. Set this to use Gateway API HTTPRoutes instead of
	// classic Ingress. Exactly one of ingressClass and gatewayRefs must be set.
	GatewayRefs []GatewayRef `json:"gatewayRefs,omitempty"`

	// TLS configures certificate termination for the created route.
	TLS *IngressTLS `json:"tls,omitempty"`

	// Annotations are merged into the created route's metadata.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// GatewayRef names a Gateway in the same namespace as the CelldFleet unless
// Namespace is set.
type GatewayRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`
}

// IngressTLS configures TLS for the created route. Exactly one of
// certificateSecretRef and clusterIssuer must be set; if neither is set the
// route is served plain HTTP.
type IngressTLS struct {
	// CertificateSecretRef references an existing TLS Secret. For classic
	// Ingress it is used directly. For HTTPRoute it becomes a
	// GatewayTLSConfig reference.
	CertificateSecretRef *string `json:"certificateSecretRef,omitempty"`

	// ClusterIssuer requests a certificate with cert-manager. The operator
	// annotates the route (cert-manager.io/cluster-issuer) so cert-manager
	// creates the companion Certificate/Secret for the hostname.
	ClusterIssuer string `json:"clusterIssuer,omitempty"`
}

type CelldFleetStatus struct {
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	ReadyReplicas      int32  `json:"readyReplicas,omitempty"`
	ServiceName        string `json:"serviceName,omitempty"`
	// URL reports the public entry point when ingress is configured, for
	// example https://demo.example.com.
	URL        string             `json:"url,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.objectStorage.bucket`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
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
