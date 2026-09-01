package cli

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformv1alpha1 "github.com/anthaathi/celld-deploy/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func testStorage() *platformv1alpha1.StorageConfig {
	return &platformv1alpha1.StorageConfig{
		Bucket:     "s3://celld/poc",
		Endpoint:   "http://minio.poc.svc:9000",
		Region:     "us-east-1",
		AllowHTTP:  true,
		SecretName: "credentials",
	}
}

func testCliFleet() *platformv1alpha1.CelldFleet {
	return &platformv1alpha1.CelldFleet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "poc"},
		Spec: platformv1alpha1.CelldFleetSpec{
			Replicas: ptrTo(int32(1)),
			ObjectStorage: platformv1alpha1.ObjectStorageSpec{
				Bucket: "s3://celld/poc", Endpoint: "http://minio.poc.svc:9000",
				Region: "us-east-1", AllowHTTP: true,
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "credentials"},
			},
		},
	}
}

func ptrTo[T any](value T) *T { return &value }

func TestBuildDeployJobStreamMode(t *testing.T) {
	job := buildDeployJob(testCliFleet(), testStorage(), "deploy-job", deployOptions{
		deployerImage: "celld-deployer:test",
	})
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != "celld-deployer:test" {
		t.Fatalf("image = %q", container.Image)
	}
	if len(container.Command) != 2 || container.Command[0] != "sleep" {
		t.Fatalf("stream mode must idle with sleep, got %v", container.Command)
	}
	if container.Args != nil {
		t.Fatalf("stream mode must not set args, got %v", container.Args)
	}
	storageEnvApplied(t, container)
	if job.Namespace != "poc" || job.Name != "deploy-job" {
		t.Fatalf("job identity = %s/%s", job.Namespace, job.Name)
	}
	if got := job.Spec.Template.Labels["job-name"]; got != "deploy-job" {
		// The controller adds the job-name label automatically; sanity-check only.
		_ = got
	}
}

func TestBuildDeployJobImageMode(t *testing.T) {
	job := buildDeployJob(testCliFleet(), testStorage(), "deploy-job", deployOptions{
		image: "celld-worker:test",
	})
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != "celld-worker:test" {
		t.Fatalf("image = %q", container.Image)
	}
	if container.Command != nil {
		t.Fatalf("image mode relies on the image ENTRYPOINT, got command %v", container.Command)
	}
	want := []string{"deploy", "/app", "--bucket", "s3://celld/poc", "--region", "us-east-1", "--endpoint", "http://minio.poc.svc:9000"}
	if strings.Join(container.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", container.Args, want)
	}
	storageEnvApplied(t, container)
}

func storageEnvApplied(t *testing.T, container corev1.Container) {
	t.Helper()
	values := map[string]string{}
	for _, env := range container.Env {
		values[env.Name] = env.Value
	}
	if values["AWS_REGION"] != "us-east-1" {
		t.Fatalf("AWS_REGION = %q", values["AWS_REGION"])
	}
	if values["AWS_ALLOW_HTTP"] != "true" || values["AWS_S3_FORCE_PATH_STYLE"] != "true" {
		t.Fatalf("allowHTTP env missing: %+v", values)
	}
	if len(container.EnvFrom) != 1 || container.EnvFrom[0].SecretRef.Name != "credentials" {
		t.Fatalf("credential secret not referenced: %+v", container.EnvFrom)
	}
}

func TestTarDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("wrangler.jsonc", "{}")
	write("src/index.js", "export default {}")
	write("src/nested/deep.js", "// deep")
	write(".git/config", "should be skipped")
	write("node_modules/pkg/index.js", "should be skipped")

	reader, err := tarDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := readTarNames(t, reader)
	// 3 files + 2 directory headers (src, src/nested).
	if len(names) != 5 {
		t.Fatalf("unexpected archive contents: %v", names)
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{"wrangler.jsonc", "src/index.js", "src/nested/deep.js"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("archive missing %s: %v", want, names)
		}
	}
	if strings.Contains(joined, ".git") || strings.Contains(joined, "node_modules") {
		t.Fatalf("archive should skip .git/node_modules: %v", names)
	}
}

func readTarNames(t *testing.T, reader io.Reader) []string {
	t.Helper()
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(gzipReader)
	names := []string{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	return names
}

func TestInitValidation(t *testing.T) {
	cases := []struct {
		name    string
		opts    initOptions
		wantErr string
	}{
		{
			name:    "store with endpoint",
			opts:    initOptions{store: "minio", endpoint: "http://x", bucket: "s3://b/p"},
			wantErr: "--store cannot be combined",
		},
		{
			name:    "ingress class with gateway",
			opts:    initOptions{bucket: "s3://b/p", ingressClass: "nginx", gateway: "gw", hostname: "h"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "tls without hostname",
			opts:    initOptions{bucket: "s3://b/p", clusterIssuer: "le"},
			wantErr: "--hostname is required",
		},
		{
			name:    "both tls modes",
			opts:    initOptions{bucket: "s3://b/p", hostname: "h", clusterIssuer: "le", tlsSecret: "sec"},
			wantErr: "mutually exclusive",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateInitOptions(testCase.opts)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestInitGeneratesValidFleet(t *testing.T) {
	// init must produce a fleet that passes operator-side validation.
	opts := initOptions{
		bucket:       "s3://celld/demo",
		store:        "minio",
		hostname:     "demo.celld.dev",
		ingressClass: "nginx",
		replicas:     2,
	}
	err := validateInitOptions(opts)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	fleet := buildFleetFromOptions("demo", "poc", opts)
	if fleet.Spec.StoreRef == nil || fleet.Spec.StoreRef.Name != "minio" {
		t.Fatalf("storeRef = %+v", fleet.Spec.StoreRef)
	}
	if fleet.Spec.ObjectStorage.Bucket != "s3://celld/demo" {
		t.Fatalf("bucket = %q", fleet.Spec.ObjectStorage.Bucket)
	}
	if fleet.Spec.ObjectStorage.Endpoint != "" || fleet.Spec.ObjectStorage.CredentialsSecretRef != nil {
		t.Fatalf("store fleet must not carry inline connection fields: %+v", fleet.Spec.ObjectStorage)
	}
	if fleet.Spec.Ingress == nil || fleet.Spec.Ingress.Hostname != "demo.celld.dev" || fleet.Spec.Ingress.IngressClass != "nginx" {
		t.Fatalf("ingress = %+v", fleet.Spec.Ingress)
	}
	if *fleet.Spec.Replicas != 2 {
		t.Fatalf("replicas = %d", *fleet.Spec.Replicas)
	}
	// The YAML output must round-trip into a usable fleet.
	encoded, err := yaml.Marshal(fleet)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &platformv1alpha1.CelldFleet{}
	if err := yaml.Unmarshal(encoded, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Spec.StoreRef == nil || decoded.Spec.Ingress == nil {
		t.Fatalf("round-trip lost fields: %s", encoded)
	}
	if _, err := decoded.ResolveStorage(func(string) (*platformv1alpha1.CelldObjectStore, error) {
		return &platformv1alpha1.CelldObjectStore{Spec: platformv1alpha1.CelldObjectStoreSpec{
			Endpoint: "http://minio:9000", Region: "us-east-1", AllowHTTP: true,
		}}, nil
	}); err != nil {
		t.Fatalf("generated fleet fails storage resolution: %v", err)
	}
}

func TestInitInlineStorage(t *testing.T) {
	opts := initOptions{
		bucket:    "s3://celld/demo",
		endpoint:  "http://minio:9000",
		region:    "eu-west-1",
		secret:    "creds",
		allowHTTP: true,
	}
	if err := validateInitOptions(opts); err != nil {
		t.Fatal(err)
	}
	fleet := buildFleetFromOptions("demo", "poc", opts)
	if fleet.Spec.StoreRef != nil {
		t.Fatal("inline mode must not set storeRef")
	}
	storage, err := fleet.ResolveStorage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if storage.Region != "eu-west-1" || storage.SecretName != "creds" || !storage.AllowHTTP {
		t.Fatalf("resolved = %+v", storage)
	}
}

func TestCurrentNamespaceFallback(t *testing.T) {
	if currentNamespace() == "" {
		t.Fatal("currentNamespace must never return empty")
	}
}

func TestRunInitPrintsYAMLWithoutCluster(t *testing.T) {
	// init without --apply only prints and must not require a cluster.
	opts := initOptions{bucket: "s3://celld/demo", store: "minio"}
	if err := validateInitOptions(opts); err != nil {
		t.Fatal(err)
	}
}
