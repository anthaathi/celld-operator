package controller

import (
	"context"
	"fmt"
	"testing"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func pointer[T any](value T) *T { return &value }

func testFleet() *platformv1alpha1.CelldFleet {
	return &platformv1alpha1.CelldFleet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "poc"},
		Spec: platformv1alpha1.CelldFleetSpec{
			Replicas: pointer[int32](2),
			ObjectStorage: platformv1alpha1.ObjectStorageSpec{
				Bucket: "s3://celld/poc", Endpoint: "http://minio.poc.svc:9000",
				Region: "us-east-1", AllowHTTP: true,
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "credentials"},
			},
		},
	}
}

func mustResolveStorage(t *testing.T, fleet *platformv1alpha1.CelldFleet) *platformv1alpha1.StorageConfig {
	t.Helper()
	storage, err := fleet.ResolveStorage(func(name string) (*platformv1alpha1.CelldObjectStore, error) {
		return nil, fmt.Errorf("unexpected store lookup: %s", name)
	})
	if err != nil {
		t.Fatalf("resolve storage: %v", err)
	}
	return storage
}

func TestBuildStatefulSetEphemeral(t *testing.T) {
	fleet := testFleet()
	statefulSet := BuildStatefulSet(fleet, mustResolveStorage(t, fleet))
	if got := *statefulSet.Spec.Replicas; got != 2 {
		t.Fatalf("replicas = %d", got)
	}
	if statefulSet.Spec.ServiceName != "demo-peer" {
		t.Fatalf("serviceName = %q", statefulSet.Spec.ServiceName)
	}
	if len(statefulSet.Spec.Template.Spec.Volumes) != 1 || statefulSet.Spec.Template.Spec.Volumes[0].EmptyDir == nil {
		t.Fatal("expected emptyDir state volume")
	}
	container := statefulSet.Spec.Template.Spec.Containers[0]
	values := map[string]string{}
	for _, item := range container.Env {
		values[item.Name] = item.Value
	}
	if values["CELLD_BUCKET"] != "s3://celld/poc" {
		t.Fatalf("bucket = %q", values["CELLD_BUCKET"])
	}
	if values["CELLD_ADVERTISE"] != "$(POD_NAME).demo-peer.$(POD_NAMESPACE).svc:8081" {
		t.Fatalf("advertise = %q", values["CELLD_ADVERTISE"])
	}
	if values["AWS_S3_FORCE_PATH_STYLE"] != "true" {
		t.Fatal("local S3 path-style setting missing")
	}
	if len(container.EnvFrom) != 1 || container.EnvFrom[0].SecretRef.Name != "credentials" {
		t.Fatal("credential secret not referenced")
	}
	if container.ReadinessProbe.HTTPGet.Path != "/.well-known/celld/health" {
		t.Fatalf("health path = %q", container.ReadinessProbe.HTTPGet.Path)
	}
}

func TestBuildStatefulSetPersistent(t *testing.T) {
	fleet := testFleet()
	fleet.Spec.LocalStorage = platformv1alpha1.LocalStorageSpec{Type: platformv1alpha1.StoragePersistent, Size: "5Gi"}
	statefulSet := BuildStatefulSet(fleet, mustResolveStorage(t, fleet))
	if len(statefulSet.Spec.VolumeClaimTemplates) != 1 {
		t.Fatal("expected one volume claim template")
	}
	got := statefulSet.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	if got.String() != "5Gi" {
		t.Fatalf("storage request = %s", got.String())
	}
}

func TestServiceSeparation(t *testing.T) {
	fleet := testFleet()
	public := BuildPublicService(fleet)
	peer := BuildPeerService(fleet)
	if public.Spec.Ports[0].TargetPort.IntVal != 8080 {
		t.Fatalf("public target port = %d", public.Spec.Ports[0].TargetPort.IntVal)
	}
	if peer.Spec.ClusterIP != "None" || peer.Spec.Ports[0].Port != 8081 {
		t.Fatal("peer service is not a private headless service")
	}
}

func ingressFleet() *platformv1alpha1.CelldFleet {
	fleet := testFleet()
	fleet.Spec.Ingress = &platformv1alpha1.IngressSpec{
		Hostname:    "demo.example.com",
		IngressClass: "nginx",
	}
	return fleet
}

func TestBuildRoutesWithoutIngress(t *testing.T) {
	if routes := BuildRoutes(testFleet()); len(routes) != 0 {
		t.Fatalf("expected no routes, got %d", len(routes))
	}
}

func TestResolveStorageFromStoreRef(t *testing.T) {
	fleet := testFleet()
	fleet.Spec.StoreRef = &corev1.LocalObjectReference{Name: "shared"}
	fleet.Spec.ObjectStorage = platformv1alpha1.ObjectStorageSpec{Bucket: "s3://celld/poc"}
	store := &platformv1alpha1.CelldObjectStore{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: fleet.Namespace},
		Spec: platformv1alpha1.CelldObjectStoreSpec{
			Endpoint: "http://minio.poc.svc:9000", Region: "eu-west-1", AllowHTTP: true,
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "store-credentials"},
		},
	}
	storage, err := fleet.ResolveStorage(func(name string) (*platformv1alpha1.CelldObjectStore, error) {
		if name != "shared" {
			t.Fatalf("looked up store %q", name)
		}
		return store, nil
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if storage.Bucket != "s3://celld/poc" || storage.Endpoint != store.Spec.Endpoint ||
		storage.Region != "eu-west-1" || !storage.AllowHTTP || storage.SecretName != "store-credentials" {
		t.Fatalf("resolved storage = %+v", storage)
	}
	statefulSet := BuildStatefulSet(fleet, storage)
	container := statefulSet.Spec.Template.Spec.Containers[0]
	values := map[string]string{}
	for _, item := range container.Env {
		values[item.Name] = item.Value
	}
	if values["AWS_REGION"] != "eu-west-1" || values["S3_ENDPOINT"] != store.Spec.Endpoint {
		t.Fatalf("store config not applied to node env: %+v", values)
	}
	if len(container.EnvFrom) != 1 || container.EnvFrom[0].SecretRef.Name != "store-credentials" {
		t.Fatal("store credential secret not referenced")
	}
}

func TestResolveStorageRejectsConflictingConnectionFields(t *testing.T) {
	fleet := testFleet()
	fleet.Spec.StoreRef = &corev1.LocalObjectReference{Name: "shared"}
	if _, err := fleet.ResolveStorage(nil); err == nil {
		t.Fatal("expected conflict error for endpoint with storeRef")
	}
}

func TestResolveStorageMissingStore(t *testing.T) {
	fleet := testFleet()
	fleet.Spec.StoreRef = &corev1.LocalObjectReference{Name: "gone"}
	fleet.Spec.ObjectStorage = platformv1alpha1.ObjectStorageSpec{Bucket: "s3://celld/poc"}
	if _, err := fleet.ResolveStorage(func(name string) (*platformv1alpha1.CelldObjectStore, error) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "platform.celld.dev", Resource: "celldobjectstores"}, name)
	}); err == nil {
		t.Fatal("expected missing store error")
	}
}

func TestBuildIngress(t *testing.T) {
	fleet := ingressFleet()
	routes := BuildRoutes(fleet)
	if len(routes) != 1 {
		t.Fatalf("expected exactly one route object, got %d", len(routes))
	}
	ingress, ok := routes[0].(*networkingv1.Ingress)
	if !ok {
		t.Fatalf("expected Ingress, got %T", routes[0])
	}
	if ingress.Name != fleet.Name || ingress.Namespace != fleet.Namespace {
		t.Fatalf("unexpected ingress name/namespace: %s/%s", ingress.Namespace, ingress.Name)
	}
	if ingress.Spec.IngressClassName == nil || *ingress.Spec.IngressClassName != "nginx" {
		t.Fatal("ingress class name missing")
	}
	rule := ingress.Spec.Rules[0]
	if rule.Host != "demo.example.com" {
		t.Fatalf("rule host = %q", rule.Host)
	}
	path := rule.HTTP.Paths[0]
	if path.Path != "/" || *path.PathType != networkingv1.PathTypePrefix {
		t.Fatalf("unexpected path: %s (%s)", path.Path, *path.PathType)
	}
	if path.Backend.Service.Name != "demo" || path.Backend.Service.Port.Name != "http" {
		t.Fatalf("unexpected backend: %s:%s", path.Backend.Service.Name, path.Backend.Service.Port.Name)
	}
	if ingress.Spec.TLS != nil {
		t.Fatal("plain ingress must not have TLS")
	}
}

func TestBuildIngressTLS(t *testing.T) {
	fleet := ingressFleet()
	fleet.Spec.Ingress.TLS = &platformv1alpha1.IngressTLS{ClusterIssuer: "letsencrypt"}
	routes := BuildRoutes(fleet)
	ingress := routes[0].(*networkingv1.Ingress)
	if ingress.Annotations["cert-manager.io/cluster-issuer"] != "letsencrypt" {
		t.Fatal("cert-manager annotation missing")
	}
	if len(ingress.Spec.TLS) != 1 || ingress.Spec.TLS[0].SecretName != "demo-tls" {
		t.Fatalf("unexpected TLS spec: %#v", ingress.Spec.TLS)
	}
	if got := publicURL(fleet); got != "https://demo.example.com" {
		t.Fatalf("public URL = %q", got)
	}

	fleet.Spec.Ingress.TLS = &platformv1alpha1.IngressTLS{CertificateSecretRef: ptr.To("custom-secret")}
	ingress = BuildIngress(fleet)
	if len(ingress.Spec.TLS) != 1 || ingress.Spec.TLS[0].SecretName != "custom-secret" {
		t.Fatalf("unexpected TLS secret: %#v", ingress.Spec.TLS)
	}
}

func TestBuildIngressStripPrefix(t *testing.T) {
	fleet := ingressFleet()
	fleet.Spec.Ingress.Path = "/api"
	fleet.Spec.Ingress.StripPrefix = ptr.To(true)

	ingress := BuildIngress(fleet)
	path := ingress.Spec.Rules[0].HTTP.Paths[0]
	if path.Path != "/api(/|$)(.*)" {
		t.Fatalf("regex path = %q", path.Path)
	}
	if path.PathType == nil || *path.PathType != networkingv1.PathTypeImplementationSpecific {
		t.Fatal("strip-prefix ingress must use ImplementationSpecific path type")
	}
	if ingress.Annotations["nginx.ingress.kubernetes.io/rewrite-target"] != "/$2" {
		t.Fatal("nginx rewrite-target annotation missing")
	}
	if ingress.Annotations["nginx.ingress.kubernetes.io/use-regex"] != "true" {
		t.Fatal("nginx use-regex annotation missing")
	}

	route := BuildHTTPRoute(fleet)
	rewrite := route.Spec.Rules[0].Filters
	if len(rewrite) != 1 || rewrite[0].Type != gatewayv1.HTTPRouteFilterURLRewrite {
		t.Fatalf("expected URLRewrite filter, got %#v", rewrite)
	}
	modifier := rewrite[0].URLRewrite.Path
	if modifier.Type != gatewayv1.PrefixMatchHTTPPathModifier || modifier.ReplacePrefixMatch == nil || *modifier.ReplacePrefixMatch != "/" {
		t.Fatalf("unexpected path modifier: %#v", modifier)
	}
	// The match itself must stay a plain prefix match on the configured path.
	if match := route.Spec.Rules[0].Matches[0].Path; *match.Type != gatewayv1.PathMatchPathPrefix || *match.Value != "/api" {
		t.Fatalf("unexpected match: %#v", match)
	}
}

func TestBuildIngressWithoutStripPrefix(t *testing.T) {
	fleet := ingressFleet()
	fleet.Spec.Ingress.Path = "/api"

	ingress := BuildIngress(fleet)
	path := ingress.Spec.Rules[0].HTTP.Paths[0]
	if path.Path != "/api" {
		t.Fatalf("plain path = %q", path.Path)
	}
	if path.PathType == nil || *path.PathType != networkingv1.PathTypePrefix {
		t.Fatal("non-stripping ingress must keep the configured path type")
	}
	if _, ok := ingress.Annotations["nginx.ingress.kubernetes.io/rewrite-target"]; ok {
		t.Fatal("rewrite annotations must not be set without stripPrefix")
	}

	route := BuildHTTPRoute(fleet)
	if filters := route.Spec.Rules[0].Filters; len(filters) != 0 {
		t.Fatalf("unexpected filters: %#v", filters)
	}
}

func TestValidateIngressStripPrefix(t *testing.T) {
	fleet := ingressFleet()
	fleet.Spec.Ingress.StripPrefix = ptr.To(true)
	if err := validateFleet(fleet); err == nil {
		t.Fatal("stripPrefix without explicit path must be rejected")
	}

	fleet.Spec.Ingress.Path = "/"
	if err := validateFleet(fleet); err == nil {
		t.Fatal("stripPrefix with root path must be rejected")
	}

	fleet.Spec.Ingress.Path = "/api"
	if err := validateFleet(fleet); err != nil {
		t.Fatalf("stripPrefix with subpath rejected: %v", err)
	}

	fleet.Spec.Ingress.StripPrefix = ptr.To(false)
	fleet.Spec.Ingress.Path = ""
	if err := validateFleet(fleet); err != nil {
		t.Fatalf("disabled stripPrefix without path rejected: %v", err)
	}
}

func TestBuildHTTPRoute(t *testing.T) {
	fleet := testFleet()
	fleet.Spec.Ingress = &platformv1alpha1.IngressSpec{
		Hostname: "demo.example.com",
		GatewayRefs: []platformv1alpha1.GatewayRef{
			{Name: "main", Namespace: "gateway-system"},
			{Name: "other"},
		},
	}
	routes := BuildRoutes(fleet)
	if len(routes) != 1 {
		t.Fatalf("expected exactly one route object, got %d", len(routes))
	}
	route, ok := routes[0].(*gatewayv1.HTTPRoute)
	if !ok {
		t.Fatalf("expected HTTPRoute, got %T", routes[0])
	}
	if route.Name != fleet.Name {
		t.Fatalf("route name = %q", route.Name)
	}
	if len(route.Spec.ParentRefs) != 2 {
		t.Fatalf("parent refs = %d", len(route.Spec.ParentRefs))
	}
	first := route.Spec.ParentRefs[0]
	if first.Name != "main" || first.Namespace == nil || string(*first.Namespace) != "gateway-system" {
		t.Fatalf("unexpected parent ref: %#v", first)
	}
	if first.Group == nil || *first.Group != "gateway.networking.k8s.io" || first.Kind == nil || *first.Kind != "Gateway" {
		t.Fatal("parent ref group/kind not defaulted to Gateway")
	}
	second := route.Spec.ParentRefs[1]
	if second.Namespace != nil {
		t.Fatal("same-namespace parent ref must not set namespace")
	}
	if len(route.Spec.Hostnames) != 1 || string(route.Spec.Hostnames[0]) != "demo.example.com" {
		t.Fatalf("hostnames = %#v", route.Spec.Hostnames)
	}
	match := route.Spec.Rules[0].Matches[0].Path
	if match.Type == nil || *match.Type != gatewayv1.PathMatchPathPrefix || *match.Value != "/" {
		t.Fatalf("unexpected path match: %#v", match)
	}
	backend := route.Spec.Rules[0].BackendRefs[0]
	if backend.Name != "demo" || backend.Port == nil || *backend.Port != 80 {
		t.Fatalf("unexpected backend: %#v", backend)
	}
}

func TestValidateIngressRules(t *testing.T) {
	fleet := ingressFleet()
	if err := validateFleet(fleet); err != nil {
		t.Fatalf("valid fleet rejected: %v", err)
	}

	fleet = ingressFleet()
	fleet.Spec.Ingress.IngressClass = ""
	if err := validateFleet(fleet); err == nil {
		t.Fatal("expected error when neither ingressClass nor gatewayRefs set")
	}

	fleet = ingressFleet()
	fleet.Spec.Ingress.GatewayRefs = []platformv1alpha1.GatewayRef{{Name: "main"}}
	if err := validateFleet(fleet); err == nil {
		t.Fatal("expected error when both ingressClass and gatewayRefs set")
	}

	fleet = ingressFleet()
	fleet.Spec.Ingress.TLS = &platformv1alpha1.IngressTLS{}
	if err := validateFleet(fleet); err == nil {
		t.Fatal("expected error when TLS has neither secretRef nor clusterIssuer")
	}

	fleet = ingressFleet()
	fleet.Spec.Ingress.TLS = &platformv1alpha1.IngressTLS{ClusterIssuer: "le", CertificateSecretRef: ptr.To("s")}
	if err := validateFleet(fleet); err == nil {
		t.Fatal("expected error when TLS sets both secretRef and clusterIssuer")
	}
}

func TestReconcileCreatesIngressOwnedByFleet(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gatewayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fleet := ingressFleet()
	fleet.UID = types.UID("fleet-uid")
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.CelldFleet{}, &appsv1.StatefulSet{}).
		WithObjects(fleet).
		Build()
	reconciler := CelldFleetReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: fleet.Namespace, Name: fleet.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var ingress networkingv1.Ingress
	if err := client.Get(context.Background(), request.NamespacedName, &ingress); err != nil {
		t.Fatalf("get Ingress: %v", err)
	}
	if len(ingress.OwnerReferences) != 1 || ingress.OwnerReferences[0].UID != fleet.UID {
		t.Fatal("Ingress is not owned by the CelldFleet")
	}

	var updated platformv1alpha1.CelldFleet
	if err := client.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.URL != "http://demo.example.com" {
		t.Fatalf("status URL = %q", updated.Status.URL)
	}

	// A second pass must recognize already-managed state and remain successful.
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestValidateHTTPRequiresOptIn(t *testing.T) {
	fleet := testFleet()
	fleet.Spec.ObjectStorage.AllowHTTP = false
	if _, err := fleet.ResolveStorage(nil); err == nil {
		t.Fatal("expected plain HTTP endpoint validation error")
	}
	// The same rule applies to http endpoints provided through a store.
	stored := testFleet()
	stored.Spec.StoreRef = &corev1.LocalObjectReference{Name: "shared"}
	stored.Spec.ObjectStorage = platformv1alpha1.ObjectStorageSpec{Bucket: "s3://celld/poc"}
	if _, err := stored.ResolveStorage(func(string) (*platformv1alpha1.CelldObjectStore, error) {
		return &platformv1alpha1.CelldObjectStore{Spec: platformv1alpha1.CelldObjectStoreSpec{
			Endpoint: "http://minio.poc.svc:9000",
		}}, nil
	}); err == nil {
		t.Fatal("expected plain HTTP store endpoint validation error")
	}
}

func TestReconcileCreatesFleetResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fleet := testFleet()
	fleet.UID = types.UID("fleet-uid")
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.CelldFleet{}, &appsv1.StatefulSet{}).
		WithObjects(fleet).
		Build()
	reconciler := CelldFleetReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: fleet.Namespace, Name: fleet.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var statefulSet appsv1.StatefulSet
	if err := client.Get(context.Background(), request.NamespacedName, &statefulSet); err != nil {
		t.Fatal(err)
	}
	if len(statefulSet.OwnerReferences) != 1 || statefulSet.OwnerReferences[0].UID != fleet.UID {
		t.Fatal("StatefulSet is not owned by the CelldFleet")
	}
	for _, name := range []string{"demo", "demo-peer"} {
		var service corev1.Service
		if err := client.Get(context.Background(), types.NamespacedName{Namespace: fleet.Namespace, Name: name}, &service); err != nil {
			t.Fatalf("get Service %s: %v", name, err)
		}
	}
	var updated platformv1alpha1.CelldFleet
	if err := client.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.ServiceName != "demo" || len(updated.Status.Conditions) != 1 {
		t.Fatalf("unexpected fleet status: %#v", updated.Status)
	}

	// A second pass must recognize already-managed state and remain successful.
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}
