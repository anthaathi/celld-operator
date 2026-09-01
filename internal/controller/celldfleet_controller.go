package controller

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	publicPort   = 8080
	internalPort = 8081
	watchPath    = "/var/lib/celld/state"
)

type CelldFleetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.celld.dev,resources=celldfleets,verbs=get;list;watch
// +kubebuilder:rbac:groups=platform.celld.dev,resources=celldfleets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.celld.dev,resources=celldfleets/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

func (r *CelldFleetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fleet platformv1alpha1.CelldFleet
	if err := r.Get(ctx, req.NamespacedName, &fleet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := validateFleet(&fleet); err != nil {
		return ctrl.Result{}, r.setCondition(ctx, &fleet, metav1.ConditionFalse, "InvalidConfiguration", err.Error())
	}

	resources := []client.Object{
		BuildPeerService(&fleet),
		BuildPublicService(&fleet),
		BuildStatefulSet(&fleet),
	}
	for _, object := range resources {
		if err := controllerutil.SetControllerReference(&fleet, object, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.apply(ctx, object); err != nil {
			return ctrl.Result{}, err
		}
	}

	var statefulSet appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Namespace: fleet.Namespace, Name: fleet.Name}, &statefulSet); err != nil {
		return ctrl.Result{}, err
	}
	status := metav1.ConditionFalse
	reason := "Progressing"
	message := fmt.Sprintf("%d of %d celld nodes are ready", statefulSet.Status.ReadyReplicas, replicas(&fleet))
	if statefulSet.Status.ReadyReplicas == replicas(&fleet) {
		status, reason = metav1.ConditionTrue, "Ready"
	}
	previousStatus := fleet.Status.DeepCopy()
	fleet.Status.ObservedGeneration = fleet.Generation
	fleet.Status.ReadyReplicas = statefulSet.Status.ReadyReplicas
	fleet.Status.ServiceName = fleet.Name
	apiMeta.SetStatusCondition(&fleet.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: message,
		ObservedGeneration: fleet.Generation,
	})
	if reflect.DeepEqual(previousStatus, &fleet.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Update(ctx, &fleet)
}

func (r *CelldFleetReconciler) apply(ctx context.Context, desired client.Object) error {
	current := desired.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, desired)
		}
		return err
	}
	if managedStateMatches(desired, current) {
		return nil
	}
	desired.SetResourceVersion(current.GetResourceVersion())
	// Keep fields allocated by the API server. Clearing any of these on an
	// update either fails validation or needlessly reallocates a NodePort.
	if desiredService, ok := desired.(*corev1.Service); ok {
		currentService := current.(*corev1.Service)
		desiredService.Spec.ClusterIP = currentService.Spec.ClusterIP
		desiredService.Spec.ClusterIPs = currentService.Spec.ClusterIPs
		desiredService.Spec.IPFamilies = currentService.Spec.IPFamilies
		desiredService.Spec.IPFamilyPolicy = currentService.Spec.IPFamilyPolicy
		desiredService.Spec.HealthCheckNodePort = currentService.Spec.HealthCheckNodePort
		for i := range desiredService.Spec.Ports {
			for _, allocated := range currentService.Spec.Ports {
				if desiredService.Spec.Ports[i].Name == allocated.Name && desiredService.Spec.Ports[i].NodePort == 0 {
					desiredService.Spec.Ports[i].NodePort = allocated.NodePort
				}
			}
		}
	}
	return r.Update(ctx, desired)
}

func managedStateMatches(desired, current client.Object) bool {
	if !reflect.DeepEqual(desired.GetLabels(), current.GetLabels()) ||
		!reflect.DeepEqual(desired.GetOwnerReferences(), current.GetOwnerReferences()) {
		return false
	}
	switch wanted := desired.(type) {
	case *corev1.Service:
		return apiequality.Semantic.DeepDerivative(wanted.Spec, current.(*corev1.Service).Spec)
	case *appsv1.StatefulSet:
		return apiequality.Semantic.DeepDerivative(wanted.Spec, current.(*appsv1.StatefulSet).Spec)
	default:
		return false
	}
}

func (r *CelldFleetReconciler) setCondition(ctx context.Context, fleet *platformv1alpha1.CelldFleet, status metav1.ConditionStatus, reason, message string) error {
	previous := fleet.Status.DeepCopy()
	apiMeta.SetStatusCondition(&fleet.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: message,
		ObservedGeneration: fleet.Generation,
	})
	if reflect.DeepEqual(previous, &fleet.Status) {
		return nil
	}
	return r.Status().Update(ctx, fleet)
}

func (r *CelldFleetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.CelldFleet{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("celldfleet").
		Complete(r)
}

func validateFleet(fleet *platformv1alpha1.CelldFleet) error {
	if replicas(fleet) < 1 {
		return fmt.Errorf("replicas must be at least 1")
	}
	if !strings.HasPrefix(fleet.Spec.ObjectStorage.Bucket, "s3://") {
		return fmt.Errorf("objectStorage.bucket must start with s3://")
	}
	if fleet.Spec.ObjectStorage.Endpoint != "" {
		endpoint, err := url.Parse(fleet.Spec.ObjectStorage.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return fmt.Errorf("objectStorage.endpoint must be an absolute HTTP(S) URL")
		}
		if endpoint.Scheme == "http" && !fleet.Spec.ObjectStorage.AllowHTTP {
			return fmt.Errorf("objectStorage.allowHTTP must be true for an http endpoint")
		}
	}
	storageType := fleet.Spec.LocalStorage.Type
	if storageType != "" && storageType != platformv1alpha1.StorageEphemeral && storageType != platformv1alpha1.StoragePersistent {
		return fmt.Errorf("localStorage.type must be Ephemeral or Persistent")
	}
	if storageType == platformv1alpha1.StoragePersistent {
		if _, err := resource.ParseQuantity(storageSize(fleet)); err != nil {
			return fmt.Errorf("localStorage.size is invalid: %w", err)
		}
	}
	return nil
}

func labelsFor(fleet *platformv1alpha1.CelldFleet) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "celld",
		"app.kubernetes.io/instance":   fleet.Name,
		"app.kubernetes.io/managed-by": "celld-operator",
	}
}

func replicas(fleet *platformv1alpha1.CelldFleet) int32 {
	if fleet.Spec.Replicas == nil {
		return 1
	}
	return *fleet.Spec.Replicas
}

func image(fleet *platformv1alpha1.CelldFleet) string {
	if fleet.Spec.Image == "" {
		return platformv1alpha1.DefaultCelldImage
	}
	return fleet.Spec.Image
}

func region(fleet *platformv1alpha1.CelldFleet) string {
	if fleet.Spec.ObjectStorage.Region == "" {
		return "us-east-1"
	}
	return fleet.Spec.ObjectStorage.Region
}

func storageType(fleet *platformv1alpha1.CelldFleet) string {
	if fleet.Spec.LocalStorage.Type == "" {
		return platformv1alpha1.StorageEphemeral
	}
	return fleet.Spec.LocalStorage.Type
}

func storageSize(fleet *platformv1alpha1.CelldFleet) string {
	if fleet.Spec.LocalStorage.Size == "" {
		return "10Gi"
	}
	return fleet.Spec.LocalStorage.Size
}

func publicServiceType(fleet *platformv1alpha1.CelldFleet) corev1.ServiceType {
	if fleet.Spec.Service.Type == "" {
		return corev1.ServiceTypeClusterIP
	}
	return fleet.Spec.Service.Type
}

func BuildPeerService(fleet *platformv1alpha1.CelldFleet) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: fleet.Name + "-peer", Namespace: fleet.Namespace, Labels: labelsFor(fleet)},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None", PublishNotReadyAddresses: true, Selector: labelsFor(fleet),
			Ports: []corev1.ServicePort{{Name: "internal", Port: internalPort, Protocol: corev1.ProtocolTCP}},
		},
	}
}

func BuildPublicService(fleet *platformv1alpha1.CelldFleet) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: fleet.Name, Namespace: fleet.Namespace, Labels: labelsFor(fleet)},
		Spec: corev1.ServiceSpec{
			Type: publicServiceType(fleet), Selector: labelsFor(fleet),
			Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(publicPort), Protocol: corev1.ProtocolTCP}},
		},
	}
}

func BuildStatefulSet(fleet *platformv1alpha1.CelldFleet) *appsv1.StatefulSet {
	labels := labelsFor(fleet)
	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "CELLD_BUCKET", Value: fleet.Spec.ObjectStorage.Bucket},
		{Name: "AWS_REGION", Value: region(fleet)},
		{Name: "CELLD_ADDR", Value: fmt.Sprintf("0.0.0.0:%d", publicPort)},
		{Name: "CELLD_INTERNAL_ADDR", Value: fmt.Sprintf("0.0.0.0:%d", internalPort)},
		{Name: "CELLD_ADVERTISE", Value: fmt.Sprintf("$(POD_NAME).%s-peer.$(POD_NAMESPACE).svc:%d", fleet.Name, internalPort)},
		{Name: "CELLD_WATCH", Value: watchPath},
	}
	if fleet.Spec.ObjectStorage.Endpoint != "" {
		env = append(env, corev1.EnvVar{Name: "S3_ENDPOINT", Value: fleet.Spec.ObjectStorage.Endpoint})
	}
	if fleet.Spec.ObjectStorage.AllowHTTP {
		env = append(env,
			corev1.EnvVar{Name: "AWS_ALLOW_HTTP", Value: "true"},
			corev1.EnvVar{Name: "AWS_S3_FORCE_PATH_STYLE", Value: "true"},
		)
	}
	envFrom := []corev1.EnvFromSource{}
	if ref := fleet.Spec.ObjectStorage.CredentialsSecretRef; ref != nil {
		envFrom = append(envFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: *ref}})
	}
	probe := &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/.well-known/celld/health", Port: intstr.FromInt(publicPort)}},
		InitialDelaySeconds: 2, PeriodSeconds: 2, TimeoutSeconds: 1, FailureThreshold: 90,
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: fleet.Name, Namespace: fleet.Namespace, Labels: labels},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: fleet.Name + "-peer", Replicas: ptr.To(replicas(fleet)),
			Selector:            &metav1.LabelSelector{MatchLabels: labels},
			PodManagementPolicy: appsv1.ParallelPodManagement,
			UpdateStrategy:      appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To[int64](90),
					Containers: []corev1.Container{{
						Name: "celld", Image: image(fleet), ImagePullPolicy: corev1.PullIfNotPresent,
						Env: env, EnvFrom: envFrom, Resources: fleet.Spec.Resources,
						Ports:          []corev1.ContainerPort{{Name: "http", ContainerPort: publicPort}, {Name: "internal", ContainerPort: internalPort}},
						ReadinessProbe: probe, StartupProbe: probe.DeepCopy(),
						VolumeMounts: []corev1.VolumeMount{{Name: "state", MountPath: "/var/lib/celld"}},
					}},
				},
			},
		},
	}
	if storageType(fleet) == platformv1alpha1.StoragePersistent {
		statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "state"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: fleet.Spec.LocalStorage.StorageClassName,
				Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(storageSize(fleet))}},
			},
		}}
	} else {
		statefulSet.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	}
	return statefulSet
}
