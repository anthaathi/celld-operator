#!/usr/bin/env bash
# End-to-end test: install the operator into a fresh kind cluster and drive a
# full fleet lifecycle with the kubectl-celld plugin.
#
# Steps:
#   1. load locally built operator + deployer images into the cluster
#   2. install CRDs/RBAC/operator
#   3. start MinIO + create the bucket + CelldObjectStore
#   4. kubectl celld init --apply a fleet with a NodePort service
#   5. kubectl celld deploy the POC Worker (stream mode)
#   6. assert the fleet reports Ready and serves the Worker over HTTP
#   7. kubectl celld status / logs smoke tests
#
# Usage: scripts/e2e.sh <operator-image> <deployer-image>
set -euo pipefail

OPERATOR_IMAGE=${1:?usage: e2e.sh <operator-image> <deployer-image>}
DEPLOYER_IMAGE=${2:?usage: e2e.sh <operator-image> <deployer-image>}
CLUSTER=${KIND_CLUSTER:-celld-e2e}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PLUGIN="$ROOT/bin/kubectl-celld"

for command in docker kind kubectl; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done
[ -x "$PLUGIN" ] || { echo "plugin not built: $PLUGIN (run make plugin)" >&2; exit 1; }

cleanup() {
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

kind create cluster --name "$CLUSTER" --wait 60s
kind load docker-image --name "$CLUSTER" "$OPERATOR_IMAGE" "$DEPLOYER_IMAGE"

kubectl apply -k "$ROOT/config"
kubectl -n celld-system patch deployment celld-operator \
  -p "{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"manager\",\"image\":\"$OPERATOR_IMAGE\"}]}}}}"
kubectl -n celld-system rollout status deployment/celld-operator --timeout=120s

kubectl apply -f "$ROOT/poc/kind/minio.yaml"
kubectl -n celld-poc rollout status deployment/minio --timeout=180s
kubectl -n celld-poc wait --for=condition=complete job/create-celld-bucket --timeout=180s
kubectl apply -f "$ROOT/poc/kind/store.yaml"

# init + deploy through the plugin (NodePort so we can curl without ingress).
"$PLUGIN" init e2e \
  --store minio \
  --bucket s3://celld/e2e \
  --replicas 1 \
  -n celld-poc \
  --apply >/dev/null
"$PLUGIN" deploy "$ROOT/poc/worker" --fleet e2e -n celld-poc --deployer-image "$DEPLOYER_IMAGE"

# The fleet is Ready when the celld node has adopted the deployment.
for _ in $(seq 1 30); do
  ready=$(kubectl -n celld-poc get celldfleet e2e -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
  [ "$ready" = "True" ] && break
  sleep 5
done
[ "$ready" = "True" ] || {
  kubectl -n celld-poc get pods -o wide
  kubectl -n celld-system logs deployment/celld-operator --tail 50
  echo "fleet never became Ready" >&2
  exit 1
}

"$PLUGIN" status e2e -n celld-poc | grep -q "Ready=True" || {
  echo "status output missing Ready=True" >&2
  exit 1
}
"$PLUGIN" logs e2e -n celld-poc --tail 5 >/dev/null || {
  echo "logs command failed" >&2
  exit 1
}

POD=$(kubectl -n celld-poc get pod -l app.kubernetes.io/instance=e2e -o jsonpath='{.items[0].metadata.name}')
BODY=$(kubectl -n celld-poc exec "$POD" -- curl -s http://127.0.0.1:8080/hello)
echo "worker response: $BODY"
echo "$BODY" | grep -q "hello from a locally deployed multi-file Worker" || {
  echo "unexpected worker response" >&2
  exit 1
}

echo
echo "E2E OK"
