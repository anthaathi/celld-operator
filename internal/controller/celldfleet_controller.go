package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
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
// +kubebuilder:rbac:groups=platform.celld.dev,resources=celldobjectstores,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete

func (r *CelldFleetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fleet platformv1alpha1.CelldFleet
	if err := r.Get(ctx, req.NamespacedName, &fleet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := validateFleet(&fleet); err != nil {
		return ctrl.Result{}, r.setCondition(ctx, &fleet, metav1.ConditionFalse, "InvalidConfiguration", err.Error())
	}

	storage, err := resolveFleetStorage(ctx, r.Client, &fleet)
	if err != nil {
		return ctrl.Result{Requeue: true}, r.setCondition(ctx, &fleet, metav1.ConditionFalse, "InvalidConfiguration", err.Error())
	}

	resources := []client.Object{
		BuildPeerService(&fleet),
		BuildPublicService(&fleet),
		BuildStatefulSet(&fleet, storage),
	}
	// Ingress/HTTPRoute are optional; build only when spec.ingress is set.
	for _, object := range BuildRoutes(&fleet) {
		resources = append(resources, object)
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
	fleet.Status.URL = publicURL(&fleet)
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
	case *networkingv1.Ingress:
		return apiequality.Semantic.DeepDerivative(wanted.Spec, current.(*networkingv1.Ingress).Spec) &&
			annotationsMatch(wanted.Annotations, current.(*networkingv1.Ingress).Annotations)
	case *gatewayv1.HTTPRoute:
		return apiequality.Semantic.DeepDerivative(wanted.Spec, current.(*gatewayv1.HTTPRoute).Spec) &&
			annotationsMatch(wanted.Annotations, current.(*gatewayv1.HTTPRoute).Annotations)
	default:
		return false
	}
}

// annotationsMatch compares only the annotations the operator manages. Any
// annotations added by users or controllers (e.g. cert-manager) are preserved.
func annotationsMatch(desired, current map[string]string) bool {
	for key, value := range desired {
		if current[key] != value {
			return false
		}
	}
	return true
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
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.CelldFleet{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		// Re-reconcile fleets when a referenced CelldObjectStore changes, so
		// endpoint/credential rotations reach the StatefulSets.
		Watches(&platformv1alpha1.CelldObjectStore{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, store client.Object) []reconcile.Request {
			var fleets platformv1alpha1.CelldFleetList
			if err := r.List(ctx, &fleets, client.InNamespace(store.GetNamespace())); err != nil {
				return nil
			}
			requests := []reconcile.Request{}
			for _, fleet := range fleets.Items {
				if fleet.Spec.StoreRef != nil && fleet.Spec.StoreRef.Name == store.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: fleet.Namespace, Name: fleet.Name}})
				}
			}
			return requests
		}))
	// HTTPRoute watches require the Gateway API CRDs. Watching on a cluster
	// without them blocks the controller start, so register the watch only
	// when the CRD is actually served.
	if gatewayAPIInstalled(mgr) {
		builder = builder.Owns(&gatewayv1.HTTPRoute{})
	}
	return builder.Named("celldfleet").Complete(r)
}

// resolveFleetStorage resolves the fleet's effective storage configuration,
// returning a wrapped error that names the missing store when applicable.
func resolveFleetStorage(ctx context.Context, c client.Client, fleet *platformv1alpha1.CelldFleet) (*platformv1alpha1.StorageConfig, error) {
	return fleet.ResolveStorage(func(name string) (*platformv1alpha1.CelldObjectStore, error) {
		store := &platformv1alpha1.CelldObjectStore{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: fleet.Namespace, Name: name}, store); err != nil {
			return nil, fmt.Errorf("store %q not found: %w", name, err)
		}
		return store, nil
	})
}

// gatewayAPIInstalled reports whether the gateway.networking.k8s.io/v1
// HTTPRoute kind is served by the cluster.
func gatewayAPIInstalled(mgr ctrl.Manager) bool {
	_, err := mgr.GetRESTMapper().RESTMapping(
		schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, "v1")
	return err == nil
}

func validateFleet(fleet *platformv1alpha1.CelldFleet) error {
	if replicas(fleet) < 1 {
		return fmt.Errorf("replicas must be at least 1")
	}
	if !strings.HasPrefix(fleet.Spec.ObjectStorage.Bucket, "s3://") {
		return fmt.Errorf("objectStorage.bucket must start with s3://")
	}
	if err := fleet.ValidateStorage(); err != nil {
		return err
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
	return validateIngress(fleet)
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

func BuildStatefulSet(fleet *platformv1alpha1.CelldFleet, storage *platformv1alpha1.StorageConfig) *appsv1.StatefulSet {
	labels := labelsFor(fleet)
	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "CELLD_BUCKET", Value: storage.Bucket},
		{Name: "AWS_REGION", Value: storage.Region},
		{Name: "CELLD_ADDR", Value: fmt.Sprintf("0.0.0.0:%d", publicPort)},
		{Name: "CELLD_INTERNAL_ADDR", Value: fmt.Sprintf("0.0.0.0:%d", internalPort)},
		{Name: "CELLD_ADVERTISE", Value: fmt.Sprintf("$(POD_NAME).%s-peer.$(POD_NAMESPACE).svc:%d", fleet.Name, internalPort)},
		{Name: "CELLD_WATCH", Value: watchPath},
	}
	if storage.Endpoint != "" {
		env = append(env, corev1.EnvVar{Name: "S3_ENDPOINT", Value: storage.Endpoint})
	}
	if storage.AllowHTTP {
		env = append(env,
			corev1.EnvVar{Name: "AWS_ALLOW_HTTP", Value: "true"},
			corev1.EnvVar{Name: "AWS_S3_FORCE_PATH_STYLE", Value: "true"},
		)
	}
	envFrom := []corev1.EnvFromSource{}
	if storage.SecretName != "" {
		envFrom = append(envFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: storage.SecretName}}})
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

// BuildRoutes returns the external-exposure objects for the fleet: either an
// Ingress (spec.ingress.ingressClass) or an HTTPRoute (spec.ingress.gatewayRefs),
// or nothing when spec.ingress is unset.
func BuildRoutes(fleet *platformv1alpha1.CelldFleet) []client.Object {
	ingress := fleet.Spec.Ingress
	if ingress == nil {
		return nil
	}
	var routes []client.Object
	if ingress.IngressClass != "" {
		routes = append(routes, BuildIngress(fleet))
	}
	if len(ingress.GatewayRefs) > 0 {
		routes = append(routes, BuildHTTPRoute(fleet))
	}
	return routes
}

// ingressPath returns the configured path with a sane default.
func ingressPath(ingress *platformv1alpha1.IngressSpec) string {
	if ingress.Path == "" {
		return "/"
	}
	return ingress.Path
}

// ingressPathType returns the configured path type with a sane default.
func ingressPathType(ingress *platformv1alpha1.IngressSpec) networkingv1.PathType {
	if ingress.PathType == "" {
		return networkingv1.PathTypePrefix
	}
	return ingress.PathType
}

// routeAnnotations merges user annotations with controller-managed ones
// (cert-manager issuer, nginx prefix rewrite).
func routeAnnotations(ingress *platformv1alpha1.IngressSpec) map[string]string {
	var annotations map[string]string
	if ingress.TLS != nil && ingress.TLS.ClusterIssuer != "" {
		annotations = map[string]string{
			"cert-manager.io/cluster-issuer": ingress.TLS.ClusterIssuer,
		}
	}
	if strip, path := ingress.StripPrefix != nil && *ingress.StripPrefix, ingressPath(ingress); strip && path != "/" {
		if annotations == nil {
			annotations = map[string]string{}
		}
		// nginx ingress: strip the matched prefix before proxying upstream.
		annotations["nginx.ingress.kubernetes.io/rewrite-target"] = "/$2"
		annotations["nginx.ingress.kubernetes.io/use-regex"] = "true"
	}
	for key, value := range ingress.Annotations {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[key] = value
	}
	return annotations
}

// routeTLSHosts returns the TLS host entry when TLS is explicitly configured.
func routeTLSHosts(fleet *platformv1alpha1.CelldFleet, ingress *platformv1alpha1.IngressSpec) []networkingv1.IngressTLS {
	if ingress.TLS == nil {
		return nil
	}
	if ingress.TLS.CertificateSecretRef == nil && ingress.TLS.ClusterIssuer == "" {
		return nil
	}
	secretName := ""
	if ingress.TLS.CertificateSecretRef != nil {
		secretName = *ingress.TLS.CertificateSecretRef
	} else {
		// cert-manager fills the Secret named after the fleet hostname.
		secretName = fleet.Name + "-tls"
	}
	return []networkingv1.IngressTLS{{Hosts: []string{ingress.Hostname}, SecretName: secretName}}
}

// BuildIngress creates the classic Ingress routing hostname(+path) to the
// public Service. TLS is either an existing Secret or cert-managed.
func BuildIngress(fleet *platformv1alpha1.CelldFleet) *networkingv1.Ingress {
	ingress := fleet.Spec.Ingress
	pathType := ingressPathType(ingress)
	path := ingressPath(ingress)
	// Prefix stripping on nginx is implemented with a regex capture and
	// rewrite-target /$2 (see routeAnnotations), which requires the
	// ImplementationSpecific path type.
	if stripsPrefix(ingress) && path != "/" {
		pathType = networkingv1.PathTypeImplementationSpecific
		path = path + "(/|$)(.*)"
	}
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: fleet.Name, Namespace: fleet.Namespace, Labels: labelsFor(fleet),
			Annotations: routeAnnotations(ingress),
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &ingress.IngressClass,
			TLS:              routeTLSHosts(fleet, ingress),
			Rules: []networkingv1.IngressRule{{
				Host: ingress.Hostname,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     path,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: fleet.Name,
									Port: networkingv1.ServiceBackendPort{Name: "http"},
								},
							},
						}},
					},
				},
			}},
		},
	}
}

// stripsPrefix reports whether the route should strip the configured path
// prefix before the request reaches the Worker.
func stripsPrefix(ingress *platformv1alpha1.IngressSpec) bool {
	return ingress.StripPrefix != nil && *ingress.StripPrefix
}

// BuildHTTPRoute creates a Gateway API HTTPRoute attaching the fleet to the
// configured Gateways. TLS with an existing Secret becomes a
// GatewayTLSConfig; cert-manager is handled through annotations on the
// Gateway or a companion Certificate the user creates.
func BuildHTTPRoute(fleet *platformv1alpha1.CelldFleet) *gatewayv1.HTTPRoute {
	ingress := fleet.Spec.Ingress
	parentRefs := make([]gatewayv1.ParentReference, 0, len(ingress.GatewayRefs))
	for _, ref := range ingress.GatewayRefs {
		parentRef := gatewayv1.ParentReference{
			Name:        gatewayv1.ObjectName(ref.Name),
			SectionName: nil,
		}
		if ref.Namespace != "" {
			ns := gatewayv1.Namespace(ref.Namespace)
			parentRef.Namespace = &ns
		}
		// Default group/kind to Gateway, as required by the Gateway API spec.
		group := gatewayv1.Group("gateway.networking.k8s.io")
		kind := gatewayv1.Kind("Gateway")
		parentRef.Group = &group
		parentRef.Kind = &kind
		parentRefs = append(parentRefs, parentRef)
	}
	// Note: TLS termination for HTTPRoute is configured on the Gateway
	// listener, not the route. cert-manager flows through the annotations
	// below; an existing Secret should be referenced by the Gateway listener.
	//
	// Prefix stripping is implemented with the standard URL Rewrite filter,
	// which every conformant Gateway implementation must support.
	rule := gatewayv1.HTTPRouteRule{
		Matches: []gatewayv1.HTTPRouteMatch{
			{Path: &gatewayv1.HTTPPathMatch{Type: pathMatchType(ingress), Value: ptr.To(ingressPath(ingress))}},
		},
		BackendRefs: []gatewayv1.HTTPBackendRef{
			{BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName(fleet.Name),
					Port: ptr.To(gatewayv1.PortNumber(80)),
				},
				Weight: ptr.To(int32(100)),
			}},
		},
	}
	if stripsPrefix(ingress) && ingressPath(ingress) != "/" {
		rule.Filters = []gatewayv1.HTTPRouteFilter{{
			Type: gatewayv1.HTTPRouteFilterURLRewrite,
			URLRewrite: &gatewayv1.HTTPURLRewriteFilter{
				Path: &gatewayv1.HTTPPathModifier{
					Type:               gatewayv1.PrefixMatchHTTPPathModifier,
					ReplacePrefixMatch: ptr.To("/"),
				},
			},
		}}
	}
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: fleet.Name, Namespace: fleet.Namespace, Labels: labelsFor(fleet),
			Annotations: routeAnnotations(ingress),
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: parentRefs},
			Hostnames:       []gatewayv1.Hostname{gatewayv1.Hostname(ingress.Hostname)},
			Rules: []gatewayv1.HTTPRouteRule{rule},
		},
	}
}

// pathMatchType maps the Ingress-style path type to Gateway API enums.
func pathMatchType(ingress *platformv1alpha1.IngressSpec) *gatewayv1.PathMatchType {
	pathType := ingressPathType(ingress)
	var matchType gatewayv1.PathMatchType
	switch pathType {
	case networkingv1.PathTypeExact:
		matchType = gatewayv1.PathMatchExact
	case networkingv1.PathTypeImplementationSpecific:
		matchType = gatewayv1.PathMatchRegularExpression
	default:
		matchType = gatewayv1.PathMatchPathPrefix
	}
	return &matchType
}

// publicURL reports the fleet's public https URL when ingress is configured.
func publicURL(fleet *platformv1alpha1.CelldFleet) string {
	if fleet.Spec.Ingress == nil || fleet.Spec.Ingress.Hostname == "" {
		return ""
	}
	scheme := "http"
	if fleet.Spec.Ingress.TLS != nil &&
		(fleet.Spec.Ingress.TLS.CertificateSecretRef != nil || fleet.Spec.Ingress.TLS.ClusterIssuer != "") {
		scheme = "https"
	}
	path := strings.TrimSuffix(ingressPath(fleet.Spec.Ingress), "/")
	return fmt.Sprintf("%s://%s%s", scheme, fleet.Spec.Ingress.Hostname, path)
}

// validateIngress validates the ingress section of the fleet spec.
func validateIngress(fleet *platformv1alpha1.CelldFleet) error {
	ingress := fleet.Spec.Ingress
	if ingress == nil {
		return nil
	}
	if ingress.Hostname == "" {
		return fmt.Errorf("ingress.hostname is required when ingress is configured")
	}
	if ingress.IngressClass == "" && len(ingress.GatewayRefs) == 0 {
		return fmt.Errorf("either ingress.ingressClass or ingress.gatewayRefs must be set")
	}
	if ingress.IngressClass != "" && len(ingress.GatewayRefs) > 0 {
		return fmt.Errorf("ingress.ingressClass and ingress.gatewayRefs are mutually exclusive")
	}
	if tls := ingress.TLS; tls != nil {
		if tls.CertificateSecretRef == nil && tls.ClusterIssuer == "" {
			return fmt.Errorf("ingress.tls requires either certificateSecretRef or clusterIssuer")
		}
		if tls.CertificateSecretRef != nil && tls.ClusterIssuer != "" {
			return fmt.Errorf("ingress.tls.certificateSecretRef and clusterIssuer are mutually exclusive")
		}
	}
	if stripsPrefix(ingress) && ingress.Path == "" {
		return fmt.Errorf("ingress.stripPrefix requires an explicit ingress.path")
	}
	if stripsPrefix(ingress) && ingress.Path == "/" {
		return fmt.Errorf("ingress.stripPrefix with path \"/\" would strip everything; use a subpath like \"/api\"")
	}
	return nil
}
