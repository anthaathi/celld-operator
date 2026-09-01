package controller

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestBuildStatefulSetEphemeral(t *testing.T) {
	fleet := testFleet()
	statefulSet := BuildStatefulSet(fleet)
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
	statefulSet := BuildStatefulSet(fleet)
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

func TestValidateHTTPRequiresOptIn(t *testing.T) {
	fleet := testFleet()
	fleet.Spec.ObjectStorage.AllowHTTP = false
	if err := validateFleet(fleet); err == nil {
		t.Fatal("expected plain HTTP endpoint validation error")
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
