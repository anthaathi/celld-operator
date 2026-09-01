#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER=${KIND_CLUSTER:-celld-poc}
OPERATOR_IMAGE=${OPERATOR_IMAGE:-celld-operator:poc}
DEPLOYER_IMAGE=${DEPLOYER_IMAGE:-celld-deployer:poc}

for command in docker kind kubectl; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || {
  echo "Docker daemon is not accessible. Start Docker or grant this user access, then rerun make poc-up." >&2
  exit 1
}

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --config "$ROOT/poc/kind/cluster.yaml" --wait 120s
fi

docker build -t "$OPERATOR_IMAGE" "$ROOT"
docker build -f "$ROOT/poc/deployer.Dockerfile" -t "$DEPLOYER_IMAGE" "$ROOT"
kind load docker-image --name "$CLUSTER" "$OPERATOR_IMAGE" "$DEPLOYER_IMAGE"

kubectl apply -k "$ROOT/config"
kubectl -n celld-system rollout status deployment/celld-operator --timeout=120s

# Ingress controller for the automatic Ingress created by spec.ingress.
kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.14.0/deploy/static/provider/kind/deploy.yaml
kubectl -n ingress-nginx rollout status deployment/ingress-nginx-controller --timeout=300s

kubectl apply -f "$ROOT/poc/kind/minio.yaml"
kubectl -n celld-poc rollout status deployment/minio --timeout=120s
kubectl -n celld-poc wait --for=condition=complete job/create-celld-bucket --timeout=120s

# Shared storage backend referenced by all POC fleets through spec.storeRef.
kubectl apply -f "$ROOT/poc/kind/store.yaml"

# Application code must exist before celld nodes boot and resolve deploy/current.json.
kubectl -n celld-poc delete job deploy-celld-worker --ignore-not-found
kubectl apply -f "$ROOT/poc/kind/deploy-worker-job.yaml"
kubectl -n celld-poc wait --for=condition=complete job/deploy-celld-worker --timeout=120s
kubectl apply -f "$ROOT/poc/kind/fleet.yaml"
kubectl -n celld-poc rollout status statefulset/local --timeout=240s

# A second fleet mounted on the /api subpath of the same hostname,
# demonstrating path-based routing with prefix stripping.
kubectl -n celld-poc delete job deploy-celld-api-worker --ignore-not-found
kubectl apply -f "$ROOT/poc/kind/deploy-api-worker-job.yaml"
kubectl -n celld-poc wait --for=condition=complete job/deploy-celld-api-worker --timeout=120s
kubectl apply -f "$ROOT/poc/kind/fleet-api.yaml"
kubectl -n celld-poc rollout status statefulset/local-api --timeout=240s

cat <<'EOF'

celld POC is ready.

Two fleets share the hostname local.celld.dev:
  /      -> fleet "local"     (root Worker)
  /api/* -> fleet "local-api" (mounted on subpath, prefix stripped)

Map the hostname to your kind node, then test it directly:
  grep -q 'local.celld.dev' /etc/hosts || echo 'local.celld.dev 127.0.0.1' | sudo tee -a /etc/hosts
  curl http://local.celld.dev/hello
  curl http://local.celld.dev/api/hello

Alternatively port-forward as before:
  kubectl -n celld-poc port-forward service/local 8080:80
  curl http://127.0.0.1:8080/hello

Gateway API demo (optional, requires Envoy Gateway):
  kubectl apply --server-side -f https://github.com/envoyproxy/gateway/releases/download/v1.5.0/install.yaml
  kubectl -n envoy-gateway-system rollout status deployment/envoy-gateway --timeout=300s
  kubectl apply -f poc/kind/gateway.yaml
  kubectl apply -f poc/kind/fleet-gw.yaml
  GW_SVC=$(kubectl -n envoy-gateway-system get svc -l gateway.envoyproxy.io/owner=$(kubectl -n celld-poc get gateway celld -o jsonpath='{.status.infrastructureRef.name}') -o name | head -1)
  kubectl -n envoy-gateway-system port-forward $GW_SVC 8081:80 &
  curl -H 'Host: gw.celld.dev' http://127.0.0.1:8081/hello

Redeploy after editing poc/worker by rebuilding/reloading the deployer image:
  make poc-deploy
EOF