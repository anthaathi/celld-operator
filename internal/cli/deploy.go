package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// defaultDeployerImage carries celld + esbuild. Build it with
	// `make deployer-image`.
	defaultDeployerImage = "celld-deployer:latest"
	deployContainerName  = "deploy"
	deploySourceDir      = "/app"
)

type deployOptions struct {
	sourceDir     string
	fleet         string
	namespace     string
	deployerImage string
	image         string
	keepJob       bool
}

func newDeployCommand() *cobra.Command {
	opts := deployOptions{}
	cmd := &cobra.Command{
		Use:   "deploy <worker-source-dir> --fleet <name>",
		Short: "Upload a new Worker version to a fleet's bucket",
		Long: `Upload a new Worker version to a fleet's bucket.

The default mode streams the local Worker source directory into an ephemeral
deploy pod (an image with celld and esbuild) and runs "celld deploy" inside
the cluster, so no Docker or local registry access is required. The fleet's
object-storage configuration is resolved exactly as the celld nodes see it,
including CelldObjectStore references.

With --image the Job instead runs an image that already contains the Worker
source at /app (the pattern used by the local kind POC).

celld nodes poll the deployment pointer and adopt the new version within 30
seconds, without restarting.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.sourceDir = args[0]
			return runDeploy(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVarP(&opts.fleet, "fleet", "f", "", "CelldFleet name to deploy to (required)")
	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "", "fleet namespace (defaults to the current context namespace)")
	cmd.Flags().StringVar(&opts.deployerImage, "deployer-image", defaultDeployerImage, "deployer image (celld + esbuild) used by stream mode")
	cmd.Flags().StringVar(&opts.image, "image", "", "deploy this image with Worker source baked at /app instead of streaming local source")
	cmd.Flags().BoolVar(&opts.keepJob, "keep-job", false, "keep the deploy Job after completion for debugging")
	_ = cmd.MarkFlagRequired("fleet")
	return cmd
}

func runDeploy(ctx context.Context, opts deployOptions) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	namespace := opts.namespace
	if namespace == "" {
		namespace = currentNamespace()
	}
	fleet, err := getFleet(ctx, c, namespace, opts.fleet)
	if err != nil {
		return err
	}
	storage, err := resolveFleetStorage(ctx, c, fleet)
	if err != nil {
		return fmt.Errorf("resolve object storage: %w", err)
	}
	sourceDir, err := filepath.Abs(opts.sourceDir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(sourceDir); err != nil || !info.IsDir() {
		return fmt.Errorf("worker source directory %q not found", sourceDir)
	}

	jobName := fmt.Sprintf("celld-deploy-%s-%d", fleet.Name, time.Now().Unix())
	job := buildDeployJob(fleet, storage, jobName, opts)
	if err := c.Create(ctx, job); err != nil {
		return fmt.Errorf("create deploy Job: %w", err)
	}
	if !opts.keepJob {
		defer func() {
			_ = c.Delete(context.Background(), job, client.PropagationPolicy(metav1.DeletePropagationBackground))
		}()
	}
	fmt.Printf("Deploying %s to fleet %s/%s (job %s)\n", sourceDir, namespace, fleet.Name, jobName)

	restConfig, err := restConfigForCurrentContext()
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	if opts.image != "" {
		// Image mode: the Job itself runs `celld deploy /app ...` to completion.
		if err := waitForJobCompletion(ctx, c, namespace, jobName); err != nil {
			printPodLogs(ctx, clientset, namespace, jobName)
			return err
		}
		printPodLogs(ctx, clientset, namespace, jobName)
	} else {
		// Stream mode: wait for the pod, copy the source in, then exec celld.
		pod, err := waitForRunningPod(ctx, clientset, namespace, jobName)
		if err != nil {
			return err
		}
		fmt.Println("Streaming Worker source to the deploy pod...")
		if err := execInPod(restConfig, pod, []string{"sh", "-c", "mkdir -p " + deploySourceDir + " && tar -xzf - -C " + deploySourceDir}, func() (io.Reader, error) {
			return tarDirectory(sourceDir)
		}, os.Stdout, os.Stderr); err != nil {
			return fmt.Errorf("copy source: %w", err)
		}
		args := []string{"celld", "deploy", deploySourceDir, "--bucket", storage.Bucket, "--region", storage.Region}
		if storage.Endpoint != "" {
			args = append(args, "--endpoint", storage.Endpoint)
		}
		fmt.Printf("Running %s\n", strings.Join(args, " "))
		if err := execInPod(restConfig, pod, args, nil, os.Stdout, os.Stderr); err != nil {
			return fmt.Errorf("celld deploy: %w", err)
		}
	}
	fmt.Println("Worker deployed. celld nodes will adopt the new version within 30 seconds.")
	return nil
}

// buildDeployJob creates the ephemeral deploy Job. In image mode the container
// runs `celld deploy` itself; in stream mode it idles so the plugin can copy
// source in and exec celld interactively.
func buildDeployJob(fleet *platformv1alpha1.CelldFleet, storage *platformv1alpha1.StorageConfig, name string, opts deployOptions) *batchv1.Job {
	env := []corev1.EnvVar{{Name: "AWS_REGION", Value: storage.Region}}
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
	container := corev1.Container{
		Name:            deployContainerName,
		Image:           opts.deployerImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             env,
		EnvFrom:         envFrom,
	}
	if opts.image != "" {
		// Image mode: args go through the image's ENTRYPOINT (celld).
		container.Image = opts.image
		container.Args = []string{"deploy", deploySourceDir, "--bucket", storage.Bucket, "--region", storage.Region}
		if storage.Endpoint != "" {
			container.Args = append(container.Args, "--endpoint", storage.Endpoint)
		}
	} else {
		// Stream mode: idle until the plugin copies source in and execs.
		container.Command = []string{"sleep", "3600"}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: fleet.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "celld-deploy",
				"app.kubernetes.io/instance":   fleet.Name,
				"app.kubernetes.io/managed-by": "kubectl-celld",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To[int32](2),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{container},
				},
			},
		},
	}
	return job
}

// waitForRunningPod polls the Job's pod until it is Running.
func waitForRunningPod(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) (*corev1.Pod, error) {
	var pod *corev1.Pod
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
		if err != nil || len(pods.Items) == 0 {
			return false, nil
		}
		candidate := &pods.Items[0]
		switch candidate.Status.Phase {
		case corev1.PodRunning:
			pod = candidate
			return true, nil
		case corev1.PodFailed, corev1.PodSucceeded:
			return false, fmt.Errorf("deploy pod finished before receiving source (phase %s)", candidate.Status.Phase)
		default:
			return false, nil
		}
	})
	if err != nil {
		return nil, fmt.Errorf("wait for deploy pod: %w", err)
	}
	return pod, nil
}

func waitForJobCompletion(ctx context.Context, c client.Client, namespace, jobName string) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		job := &batchv1.Job{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, job); err != nil {
			return false, nil
		}
		if job.Status.Succeeded > 0 {
			return true, nil
		}
		if job.Status.Failed > 0 {
			return false, fmt.Errorf("deploy Job failed")
		}
		return false, nil
	})
}

func printPodLogs(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
	if err != nil || len(pods.Items) == 0 {
		return
	}
	logs, err := clientset.CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{Container: deployContainerName}).Stream(ctx)
	if err != nil {
		return
	}
	defer logs.Close()
	_, _ = io.Copy(os.Stdout, logs)
}

// execInPod runs a command inside a pod, optionally piping stdin from the
// provided factory, and streams stdout/stderr to the given writers.
func execInPod(config *rest.Config, pod *corev1.Pod, command []string, stdinFactory func() (io.Reader, error), stdout, stderr io.Writer) error {
	var stdin io.Reader
	if stdinFactory != nil {
		reader, err := stdinFactory()
		if err != nil {
			return err
		}
		stdin = reader
	}
	execRequest := kubernetes.NewForConfigOrDie(config).CoreV1().RESTClient().
		Post().
		Namespace(pod.Namespace).
		Resource("pods").
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdin:   stdin != nil,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", execRequest.URL())
	if err != nil {
		return err
	}
	return executor.StreamWithContext(context.Background(), remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    false,
	})
}

// tarDirectory packs dir into an in-memory gzipped tarball, skipping .git and
// node_modules trees.
func tarDirectory(dir string) (io.Reader, error) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if parts[0] == ".git" || parts[0] == "node_modules" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := io.Copy(tarWriter, file); err != nil {
				return err
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return &buffer, nil
}

// restConfigForCurrentContext loads a *rest.Config using the standard
// kubeconfig loading rules.
func restConfigForCurrentContext() (*rest.Config, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return config, nil
}
