package v1alpha1

import (
	"errors"
	"fmt"
	"net/url"
)

// StorageConfig is the fully-resolved object-storage configuration for one
// fleet: either the fleet's inline objectStorage spec, or the connection fields
// from a referenced CelldObjectStore combined with the fleet's bucket.
// BuildStatefulSet, deploy Jobs, and the kubectl plugin all consume this.
type StorageConfig struct {
	Bucket     string
	Endpoint   string
	Region     string
	AllowHTTP  bool
	SecretName string
}

// HasStoreConnection reports whether the fleet's objectStorage sets any
// connection field that would conflict with spec.storeRef.
func (s *ObjectStorageSpec) HasStoreConnection() bool {
	return s.Endpoint != "" || s.Region != "" || s.CredentialsSecretRef != nil || s.AllowHTTP
}

// ValidateStorage checks the fleet's object-storage configuration in
// isolation: the bucket pattern and the storeRef/inline exclusivity.
func (f *CelldFleet) ValidateStorage() error {
	if f.Spec.ObjectStorage.Bucket == "" {
		return errors.New("objectStorage.bucket is required")
	}
	if f.Spec.StoreRef != nil && f.Spec.ObjectStorage.HasStoreConnection() {
		return errors.New("objectStorage connection fields (endpoint, region, allowHTTP, credentialsSecretRef) must be empty when storeRef is set")
	}
	return nil
}

// ResolveStorage returns the effective storage configuration for the fleet.
// When spec.storeRef is set, the CelldObjectStore lookup is delegated to the
// provided getter (the controller's client or the plugin's client); an error
// is returned when the store does not exist. Otherwise the inline
// objectStorage spec is normalized (region default us-east-1).
func (f *CelldFleet) ResolveStorage(getStore func(name string) (*CelldObjectStore, error)) (*StorageConfig, error) {
	if err := f.ValidateStorage(); err != nil {
		return nil, err
	}
	objectStorage := f.Spec.ObjectStorage
	region := objectStorage.Region
	secretName := ""
	if objectStorage.CredentialsSecretRef != nil {
		secretName = objectStorage.CredentialsSecretRef.Name
	}
	config := &StorageConfig{Bucket: objectStorage.Bucket}
	if f.Spec.StoreRef != nil {
		store, err := getStore(f.Spec.StoreRef.Name)
		if err != nil {
			return nil, err
		}
		if err := store.Spec.Validate(); err != nil {
			return nil, fmt.Errorf("store %q is invalid: %w", f.Spec.StoreRef.Name, err)
		}
		config.Endpoint = store.Spec.Endpoint
		config.Region = store.Spec.Region
		config.AllowHTTP = store.Spec.AllowHTTP
		if store.Spec.CredentialsSecretRef != nil {
			config.SecretName = store.Spec.CredentialsSecretRef.Name
		}
	} else {
		config.Endpoint = objectStorage.Endpoint
		config.Region = region
		config.AllowHTTP = objectStorage.AllowHTTP
		config.SecretName = secretName
	}
	if config.Endpoint != "" {
		endpoint, err := url.Parse(config.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return nil, errors.New("object storage endpoint must be an absolute HTTP(S) URL")
		}
		if endpoint.Scheme == "http" && !config.AllowHTTP {
			return nil, errors.New("allowHTTP must be true for an http object storage endpoint")
		}
	}
	if config.Region == "" {
		config.Region = "us-east-1"
	}
	return config, nil
}

// ResolveStorage validates the store spec itself: an http endpoint requires
// allowHTTP, and the endpoint must be a valid absolute URL when set.
func (s *CelldObjectStoreSpec) Validate() error {
	if s.Endpoint == "" {
		return nil
	}
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || !endpoint.IsAbs() {
		return fmt.Errorf("spec.endpoint must be an absolute HTTP(S) URL")
	}
	if endpoint.Scheme == "http" && !s.AllowHTTP {
		return fmt.Errorf("spec.allowHTTP must be true for an http endpoint")
	}
	return nil
}
