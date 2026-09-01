#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER=${KIND_CLUSTER:-celld-poc}
DEPLOYER_IMAGE=${DEPLOYER_IMAGE:-celld-deployer:poc}

docker info >/dev/null 2>&1 || { echo "Docker daemon is not accessible." >&2; exit 1; }
docker build -f "$ROOT/poc/deployer.Dockerfile" -t "$DEPLOYER_IMAGE" "$ROOT"
kind load docker-image --name "$CLUSTER" "$DEPLOYER_IMAGE"
kubectl -n celld-poc delete job deploy-celld-worker --ignore-not-found
kubectl apply -f "$ROOT/poc/kind/deploy-worker-job.yaml"
kubectl -n celld-poc wait --for=condition=complete job/deploy-celld-worker --timeout=120s

echo "Worker uploaded. celld nodes will adopt it within 30 seconds."