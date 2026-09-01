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
  kind create cluster --name "$CLUSTER" --wait 120s
fi

docker build -t "$OPERATOR_IMAGE" "$ROOT"
docker build -f "$ROOT/poc/deployer.Dockerfile" -t "$DEPLOYER_IMAGE" "$ROOT"
kind load docker-image --name "$CLUSTER" "$OPERATOR_IMAGE" "$DEPLOYER_IMAGE"

kubectl apply -k "$ROOT/config"
kubectl -n celld-system rollout status deployment/celld-operator --timeout=120s

kubectl apply -f "$ROOT/poc/kind/minio.yaml"
kubectl -n celld-poc rollout status deployment/minio --timeout=120s
kubectl -n celld-poc wait --for=condition=complete job/create-celld-bucket --timeout=120s

# Application code must exist before celld nodes boot and resolve deploy/current.json.
kubectl -n celld-poc delete job deploy-celld-worker --ignore-not-found
kubectl apply -f "$ROOT/poc/kind/deploy-worker-job.yaml"
kubectl -n celld-poc wait --for=condition=complete job/deploy-celld-worker --timeout=120s
kubectl apply -f "$ROOT/poc/kind/fleet.yaml"
kubectl -n celld-poc rollout status statefulset/local --timeout=240s

cat <<'EOF'

celld POC is ready.

In another terminal run:
  kubectl -n celld-poc port-forward service/local 8080:80

Then test it:
  curl http://127.0.0.1:8080/hello

Redeploy after editing poc/worker by rebuilding/reloading the deployer image:
  make poc-deploy
EOF