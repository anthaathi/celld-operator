# celld Kubernetes operator POC

This repository is a proof-of-concept Kubernetes operator for
[denoland/celld](https://github.com/denoland/celld). A `CelldFleet` resource
defines one celld fleet, which serves one public Worker application from one
object-storage bucket prefix.

The POC reconciles:

- a `StatefulSet` of celld nodes;
- a public Worker `Service` on port 80 → celld port 8080;
- a private headless peer `Service` on port 8081;
- ephemeral (`emptyDir`) or persistent (`PersistentVolumeClaim`) local working
  storage;
- S3-compatible credentials through a referenced Kubernetes Secret.

> celld v0.4.0 is alpha. Do not use this POC as-is for hostile multi-tenant or
> production workloads. The internal port exposes peer traffic and an alpha,
> partly unauthenticated operator API; never publish it outside the trusted
> cluster network.

## Local one-command demo

Requirements:

- Docker with a daemon accessible to the current user;
- [Kind](https://kind.sigs.k8s.io/);
- `kubectl`;
- Go 1.24+ only when developing the operator.

Run:

```bash
make poc-up
```

The command:

1. creates a `celld-poc` Kind cluster with host ports 80/443 mapped to the
   ingress controller;
2. builds and installs the operator;
3. installs ingress-nginx for automatic Ingress exposure;
4. starts MinIO as a local S3-compatible object store;
5. creates the `celld` bucket;
6. builds and runs a deployer Job containing celld v0.4.0 and esbuild;
7. executes `celld deploy` for the multi-file project in `poc/worker`;
8. creates a one-node `CelldFleet` using ephemeral local working storage;
9. creates a second `CelldFleet` mounted on the `/api` subpath of the same
   hostname, demonstrating subpath routing with prefix stripping.

The fleet declares an ingress, so the operator creates the Ingress
automatically — no manual Service exposure is needed:

```yaml
ingress:
  hostname: local.celld.dev
  ingressClass: nginx
```

Map the hostname to your loopback interface (the cluster listens on
127.0.0.1:80), then curl it:

```bash
grep -q 'local.celld.dev' /etc/hosts || echo 'local.celld.dev 127.0.0.1' | sudo tee -a /etc/hosts
curl http://local.celld.dev/hello
```

A second fleet is mounted on the `/api` subpath of the same hostname with
`stripPrefix: true`, so `/api/hello` reaches that Worker as `/hello`:

```bash
curl http://local.celld.dev/api/hello
```

Expected response:

```json
{"message":"hello from a locally deployed multi-file Worker","runtime":"celld","path":"/hello"}
```

Alternatively, port-forward the Service:

```bash
kubectl -n celld-poc port-forward service/local 8080:80
curl http://127.0.0.1:8080/hello
```

Edit `poc/worker`, then publish a new application version:

```bash
make poc-deploy
```

Nodes poll the deployment pointer and adopt the new version within 30 seconds,
without restarting. Remove the local cluster with:

```bash
make poc-down
```

## Gateway API demo

The POC also supports Gateway API routing. Instead of `ingressClass: nginx`,
point the fleet at an existing Gateway through `gatewayRefs`; the operator
creates an `HTTPRoute` attached to it. The demo uses Envoy Gateway:

```bash
# once: install Envoy Gateway and Gateway API CRDs
kubectl apply --server-side -f https://github.com/envoyproxy/gateway/releases/download/v1.5.0/install.yaml
kubectl -n envoy-gateway-system rollout status deployment/envoy-gateway --timeout=300s

# Gateway + GatewayClass (the v1.5.0 install manifest ships without the class)
kubectl apply -f poc/kind/gateway.yaml
kubectl apply -f poc/kind/fleet-gw.yaml
kubectl -n celld-poc rollout status statefulset/local-gw --timeout=240s
```

Envoy provisions a data-plane service named `envoy-celld-poc-celld-<hash>` in
`envoy-gateway-system`. kind does not provision LoadBalancer services, so
port-forward it and curl with the fleet's hostname:

```bash
GW_SVC=$(kubectl -n envoy-gateway-system get svc -l gateway.envoyproxy.io/owner=$(kubectl -n celld-poc get gateway celld -o jsonpath='{.status.infrastructureRef.name}') -o name | head -1)
kubectl -n envoy-gateway-system port-forward $GW_SVC 8081:80 &
curl -H 'Host: gw.celld.dev' http://127.0.0.1:8081/hello
```

Expected response:

```json
{"message":"hello from a locally deployed multi-file Worker","runtime":"celld","path":"/hello"}
```

The fleet behind the Gateway:

```yaml
ingress:
  hostname: gw.celld.dev
  gatewayRefs:
  - name: celld
```

Remove the demo again with:

```bash
kubectl delete -f poc/kind/fleet-gw.yaml
kubectl delete -f poc/kind/gateway.yaml
```

## What application deployment means

Fleet installation and Worker deployment are separate operations:

```text
Worker source ── celld deploy ──► S3 deploy/current.json
                                      │
                                      ▼
CelldFleet ── operator ──► celld StatefulSet ──► public Service
```

The official celld runtime image does not contain esbuild. This POC therefore
uses `poc/deployer.Dockerfile` only for the deployment Job. The long-running
StatefulSet uses the official `ghcr.io/denoland/celld:v0.4.0` image.

One fleet has one fleet-wide public deployment. Use imports for a multi-file
Worker. For independent public applications, create separate `CelldFleet`
resources with separate bucket prefixes, for example:

```text
s3://company-celld/api
s3://company-celld/admin
```

## Shared object stores (CelldObjectStore)

Define the S3-compatible connection once and share it across fleets:

```yaml
apiVersion: platform.celld.dev/v1alpha1
kind: CelldObjectStore
metadata:
  name: minio
  namespace: celld-poc
spec:
  endpoint: http://minio.celld-poc.svc:9000
  region: us-east-1
  allowHTTP: true
  credentialsSecretRef:
    name: celld-s3-credentials
```

Fleets reference the store and keep only their bucket prefix:

```yaml
spec:
  storeRef:
    name: minio
  objectStorage:
    bucket: s3://celld/local-poc
```

`storeRef` is mutually exclusive with inline `objectStorage` connection
fields (`endpoint`, `region`, `allowHTTP`, `credentialsSecretRef`): set
exactly one. Rotating credentials or endpoints on the store updates every
referencing fleet's nodes on the next reconcile. Inline `objectStorage`
remains fully supported for one-off fleets.

## kubectl plugin

The `kubectl-celld` binary is a kubectl plugin for the full fleet workflow —
deploy Workers, inspect fleets, and bootstrap new ones — without raw
manifests or `kubectl` invocations:

```bash
make plugin
sudo cp bin/kubectl-celld /usr/local/bin/   # or anywhere on PATH
kubectl celld --help
```

Stream a local Worker directory into an in-cluster deploy pod (no Docker, no
registry needed — the fleet's storage config, including `CelldObjectStore`
references, is resolved exactly as the nodes see it):

```bash
kubectl celld deploy ./my-worker --fleet local-api -n celld-poc
```

Deploy an image that already contains the source at `/app` (the kind POC
pattern):

```bash
kubectl celld deploy --image celld-deployer:myapp --fleet local-api
```

Inspect a fleet — URL, storage resolution, rollout, pods, conditions:

```bash
kubectl celld status local-api -n celld-poc
```

Stream node logs:

```bash
kubectl celld logs local-api -n celld-poc --follow
```

Bootstrap a new fleet from flags, Knative-style (prints YAML; `--apply`
creates it):

```bash
kubectl celld init demo-app \
  --store minio \
  --bucket s3://celld/demo-app \
  --hostname demo.celld.dev \
  --ingress-class nginx \
  --path /demo --strip-prefix \
  --apply
```

The plugin uses your kubeconfig credentials and needs read access to
`CelldFleet`/`CelldObjectStore` plus Job/Pod create, `pods/exec`, and
`pods/log`. The stream-mode deployer image (celld + esbuild, no source baked
in) is built from the root `deployer.Dockerfile`:

```bash
make deployer-image
```

They may share the same physical S3 bucket, but each prefix is a separate
fleet. celld named service bindings can co-host internal scripts, but native
`celld deploy` also moves the public pointer; deploy internal targets first and
the public gateway last.

## Use an existing S3 bucket

Create credentials without committing their values:

```bash
kubectl create secret generic celld-s3-credentials \
  --from-literal=AWS_ACCESS_KEY_ID='...' \
  --from-literal=AWS_SECRET_ACCESS_KEY='...'
```

Apply `examples/fleet-s3.yaml` after changing the bucket and namespace. For
Cloudflare R2, use `examples/fleet-r2.yaml` and configure the account endpoint.
The bucket must support conditional creates, conditional overwrites, and
read-after-write consistency. AWS S3 and Cloudflare R2 are supported by celld.

Deploy an application from your workstation or CI using the same bucket
prefix and storage credentials:

```bash
celld deploy ./my-worker \
  --bucket s3://my-celld-bucket/example \
  --region us-east-1
```

Worker projects require `esbuild` on `PATH`. celld v0.4.0 accepts
`wrangler.json` and `wrangler.jsonc`, not `wrangler.toml`.

## CelldFleet example

```yaml
apiVersion: platform.celld.dev/v1alpha1
kind: CelldFleet
metadata:
  name: example
spec:
  replicas: 2
  objectStorage:
    bucket: s3://my-celld-bucket/example
    region: us-east-1
    credentialsSecretRef:
      name: celld-s3-credentials
  localStorage:
    type: Persistent
    size: 10Gi
  service:
    type: ClusterIP
  ingress:
    hostname: example.celld.dev
    ingressClass: nginx
    path: /api          # optional: mount on a subpath
    stripPrefix: true   # Worker sees /hello when clients call /api/hello
    tls:
      clusterIssuer: letsencrypt-prod
```

`Ephemeral` local storage is suitable for the local POC and bucket-durable
setups where restart cost is acceptable. `Persistent` storage preserves local
SQLite working files, caches, and follower replication data across container
restarts. The object-storage prefix remains the fleet's long-term authority.

## Development

```bash
make manifests
make verify
docker build -t celld-operator:poc .
```

Generated files are checked into `api/v1alpha1/zz_generated.deepcopy.go`,
`config/crd/bases`, and `config/rbac/role.yaml`.

## POC limitations

- S3-compatible storage only; celld also supports GCS and Azure, but those are
  outside this POC API.
- No Ingress/Gateway controller management. The operator creates Ingress or
  HTTPRoute resources for a fleet (`spec.ingress`), but installing and
  configuring the controller itself (nginx, Traefik, Envoy Gateway, ...) is
  outside its scope.
- No automatic Worker source reconciliation. `celld deploy` remains a visible
  Job or CI step.
- No NetworkPolicy, PodDisruptionBudget, autoscaling, or version-aware upgrade
  orchestration yet.
- The local MinIO release passes celld's conditional-write tests but upstream
  does not qualify community MinIO for production.
- Some celld version upgrades cannot be rolled. Always read upstream release
  notes before changing the runtime image.