package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newLogsCommand() *cobra.Command {
	var namespace string
	var follow bool
	var tail int64
	var container string
	cmd := &cobra.Command{
		Use:   "logs <fleet>",
		Short: "Stream logs from a fleet's celld nodes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd.Context(), args[0], namespace, follow, tail, container)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "fleet namespace (defaults to the current context namespace)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream logs as they arrive")
	cmd.Flags().Int64Var(&tail, "tail", 100, "lines of recent log history per pod (0 for all)")
	cmd.Flags().StringVar(&container, "container", "celld", "container name inside the celld pods")
	return cmd
}

func runLogs(ctx context.Context, name, namespaceFlag string, follow bool, tail int64, container string) error {
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
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels(fleetLabels(fleet))); err != nil {
		return err
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found for fleet %s/%s", namespace, name)
	}
	if follow && len(pods.Items) > 1 {
		fmt.Fprintf(os.Stderr, "Note: following %d pods interleaved; use --tail for history\n", len(pods.Items))
	}

	restConfig, err := restConfigForCurrentContext()
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	for _, pod := range pods.Items {
		logOptions := corev1.PodLogOptions{Container: container, Follow: follow}
		if tail > 0 {
			logOptions.TailLines = &tail
		}
		stream, err := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &logOptions).Stream(ctx)
		if err != nil {
			return fmt.Errorf("get logs for %s: %w", pod.Name, err)
		}
		fmt.Printf("=== %s ===\n", pod.Name)
		if _, err := copyStream(os.Stdout, stream); err != nil {
			_ = stream.Close()
			return err
		}
		_ = stream.Close()
	}
	return nil
}

func copyStream(dst io.Writer, stream io.Reader) (int64, error) {
	return io.Copy(dst, stream)
}
