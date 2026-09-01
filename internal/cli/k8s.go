package cli

import (
	"context"
	"fmt"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newClient builds a controller-runtime client from the standard kubeconfig
// loading rules (KUBECONFIG, ~/.kube/config, in-cluster fallback).
func newClient() (client.Client, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme))
	return client.New(config, client.Options{Scheme: scheme})
}

// currentNamespace mirrors kubectl's namespace resolution: the contexts's
// namespace, then "default".
func currentNamespace() string {
	rawConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).RawConfig()
	if err != nil {
		return "default"
	}
	if context, ok := rawConfig.Contexts[rawConfig.CurrentContext]; ok && context.Namespace != "" {
		return context.Namespace
	}
	return "default"
}

// getFleet fetches a CelldFleet by namespace/name.
func getFleet(ctx context.Context, c client.Client, namespace, name string) (*platformv1alpha1.CelldFleet, error) {
	fleet := &platformv1alpha1.CelldFleet{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, fleet); err != nil {
		return nil, fmt.Errorf("get fleet %s/%s: %w", namespace, name, err)
	}
	return fleet, nil
}

// resolveFleetStorage resolves the fleet's effective storage configuration,
// including the CelldObjectStore lookup when spec.storeRef is set. Shared by
// the deploy command so the deploy Job sees exactly what the nodes see.
func resolveFleetStorage(ctx context.Context, c client.Client, fleet *platformv1alpha1.CelldFleet) (*platformv1alpha1.StorageConfig, error) {
	return fleet.ResolveStorage(func(name string) (*platformv1alpha1.CelldObjectStore, error) {
		store := &platformv1alpha1.CelldObjectStore{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: fleet.Namespace, Name: name}, store); err != nil {
			return nil, fmt.Errorf("get store %s/%s: %w", fleet.Namespace, name, err)
		}
		return store, nil
	})
}
