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

1. creates a `celld-poc` Kind cluster;
2. builds and installs the operator;
3. starts MinIO as a local S3-compatible object store;
4. creates the `celld` bucket;
5. builds and runs a deployer Job containing celld v0.4.0 and esbuild;
6. executes `celld deploy` for the multi-file project in `poc/worker`;
7. creates a one-node `CelldFleet` using ephemeral local working storage.

Expose the Worker:

```bash
kubectl -n celld-poc port-forward service/local 8080:80
```

In another terminal:

```bash
curl http://127.0.0.1:8080/hello
```

Expected response:

```json
{"message":"hello from a locally deployed multi-file Worker","runtime":"celld","path":"/hello"}
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
    type: LoadBalancer
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
- No Ingress/Gateway resource; the public Service is exposed explicitly.
- No automatic Worker source reconciliation. `celld deploy` remains a visible
  Job or CI step.
- No NetworkPolicy, PodDisruptionBudget, autoscaling, or version-aware upgrade
  orchestration yet.
- The local MinIO release passes celld's conditional-write tests but upstream
  does not qualify community MinIO for production.
- Some celld version upgrades cannot be rolled. Always read upstream release
  notes before changing the runtime image.