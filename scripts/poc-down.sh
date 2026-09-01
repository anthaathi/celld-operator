#!/usr/bin/env bash
set -euo pipefail

CLUSTER=${KIND_CLUSTER:-celld-poc}
if kind get clusters | grep -qx "$CLUSTER"; then
  kind delete cluster --name "$CLUSTER"
else
  echo "Kind cluster $CLUSTER does not exist"
fi