package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newStatusCommand() *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "status <fleet>",
		Short: "Show fleet health, rollout, route, and recent events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context(), args[0], namespace)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "fleet namespace (defaults to the current context namespace)")
	return cmd
}

func runStatus(ctx context.Context, name, namespaceFlag string) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	namespace := namespaceFlag
	if namespace == "" {
		namespace = currentNamespace()
	}
	fleet, err := getFleet(ctx, c, namespace, name)
	if err != nil {
		return err
	}

	fmt.Printf("Fleet %s/%s\n", namespace, fleet.Name)
	if fleet.Status.URL != "" {
		fmt.Printf("  URL:       %s\n", fleet.Status.URL)
	}
	fmt.Printf("  Image:     %s\n", fleetImage(fleet))
	fmt.Printf("  Replicas:  %d\n", replicasOrOne(fleet))
	storage, err := resolveFleetStorage(ctx, c, fleet)
	if err != nil {
		fmt.Printf("  Storage:   unresolved: %v\n", err)
	} else {
		if fleet.Spec.StoreRef != nil {
			fmt.Printf("  Store:     %s (CelldObjectStore)\n", fleet.Spec.StoreRef.Name)
		}
		fmt.Printf("  Bucket:    %s\n", storage.Bucket)
		if storage.Endpoint != "" {
			fmt.Printf("  Endpoint:  %s\n", storage.Endpoint)
		}
	}

	fmt.Println("\nStatefulSet:")
	statefulSet := &appsv1.StatefulSet{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: fleet.Name}, statefulSet); err != nil {
		fmt.Printf("  not created yet\n")
	} else {
		fmt.Printf("  Ready:     %d/%d\n", statefulSet.Status.ReadyReplicas, replicasOrOne(fleet))
		fmt.Printf("  Revision:  %s\n", statefulSet.Status.UpdateRevision)
	}

	fmt.Println("\nPods:")
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels(fleetLabels(fleet))); err == nil {
		if len(pods.Items) == 0 {
			fmt.Println("  none")
		}
		for _, pod := range pods.Items {
			restarts := int32(0)
			for _, status := range pod.Status.ContainerStatuses {
				restarts += status.RestartCount
			}
			fmt.Printf("  %s  %s  %s  restarts=%d\n", pod.Name, string(pod.Status.Phase), age(pod.CreationTimestamp), restarts)
		}
	}

	fmt.Println("\nConditions:")
	if len(fleet.Status.Conditions) == 0 {
		fmt.Println("  none reported")
	}
	for _, condition := range fleet.Status.Conditions {
		fmt.Printf("  %s=%s %s: %s\n", condition.Type, condition.Status, condition.Reason, condition.Message)
	}
	return nil
}

func fleetImage(fleet *platformv1alpha1.CelldFleet) string {
	if fleet.Spec.Image == "" {
		return platformv1alpha1.DefaultCelldImage
	}
	return fleet.Spec.Image
}

func replicasOrOne(fleet *platformv1alpha1.CelldFleet) int32 {
	if fleet.Spec.Replicas == nil {
		return 1
	}
	return *fleet.Spec.Replicas
}

// fleetLabels mirrors the operator's labelsFor selector.
func fleetLabels(fleet *platformv1alpha1.CelldFleet) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "celld",
		"app.kubernetes.io/instance":   fleet.Name,
		"app.kubernetes.io/managed-by": "celld-operator",
	}
}

func age(timestamp metav1.Time) string {
	duration := time.Since(timestamp.Time).Round(time.Second)
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm%ds", int(duration.Minutes()), int(duration.Seconds())%60)
	}
	return strings.TrimSpace(duration.String())
}
