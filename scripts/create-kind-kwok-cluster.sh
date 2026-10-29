#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

CLUSTER="topograph"
KIND_CONFIG=""
MODEL=""
OUTPUT=""
CPU="96"
MEMORY="1024Gi"
PODS="110"
EPHEMERAL_STORAGE="10Gi"
GPUS="0"
GPU_RESOURCE_NAME="nvidia.com/gpu"
KWOK_RELEASE="latest"
WAIT="120s"

usage() {
  cat <<EOF
Usage: $0 -m <model-file> [options]

Create or reuse a kind cluster, install KWOK in it, and populate it with
virtual nodes rendered from a Topograph model file.

Options:
  -m, --model <file>              Model file. Basenames resolve from tests/models.
  -c, --cluster <name>            kind cluster name. Default: ${CLUSTER}
      --kind-config <file>        Optional kind cluster config for cluster creation.
  -o, --output <file>             Keep the generated Node manifest at this path.
      --kwok-release <tag>        KWOK release tag to install, or latest. Default: ${KWOK_RELEASE}
      --wait <duration>           kind cluster readiness wait timeout. Default: ${WAIT}
      --cpu <quantity>            Node CPU capacity. Default: ${CPU}
      --memory <quantity>         Node memory capacity. Default: ${MEMORY}
      --pods <quantity>           Node pod capacity. Default: ${PODS}
      --ephemeral-storage <qty>   Node ephemeral-storage capacity. Default: ${EPHEMERAL_STORAGE}
      --gpus <count>              GPU capacity per node. Default: ${GPUS}
      --gpu-resource-name <name>  GPU extended resource name. Default: ${GPU_RESOURCE_NAME}
  -h, --help                      Show this help.

Environment:
  KIND                            kind binary. Default: kind
  KUBECTL                         kubectl binary. Default: kubectl
  KWOK_NODES_BIN                  kwok-nodes binary. Default: bin/kwok-nodes
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--model)
      MODEL="$2"
      shift 2
      ;;
    -c|--cluster)
      CLUSTER="$2"
      shift 2
      ;;
    --kind-config)
      KIND_CONFIG="$2"
      shift 2
      ;;
    -o|--output)
      OUTPUT="$2"
      shift 2
      ;;
    --kwok-release)
      KWOK_RELEASE="$2"
      shift 2
      ;;
    --wait)
      WAIT="$2"
      shift 2
      ;;
    --cpu)
      CPU="$2"
      shift 2
      ;;
    --memory)
      MEMORY="$2"
      shift 2
      ;;
    --pods)
      PODS="$2"
      shift 2
      ;;
    --ephemeral-storage)
      EPHEMERAL_STORAGE="$2"
      shift 2
      ;;
    --gpus)
      GPUS="$2"
      shift 2
      ;;
    --gpu-resource-name)
      GPU_RESOURCE_NAME="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${MODEL}" ]]; then
  echo "missing required --model" >&2
  usage >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
KIND="${KIND:-kind}"
KUBECTL="${KUBECTL:-kubectl}"
KWOK_NODES_BIN="${KWOK_NODES_BIN:-${REPO_ROOT}/bin/kwok-nodes}"
CONTEXT="kind-${CLUSTER}"

kwok_release_url() {
  local file="$1"
  if [[ "${KWOK_RELEASE}" == "latest" ]]; then
    echo "https://github.com/kubernetes-sigs/kwok/releases/latest/download/${file}"
  else
    echo "https://github.com/kubernetes-sigs/kwok/releases/download/${KWOK_RELEASE}/${file}"
  fi
}

apply_with_retry() {
  local description="$1"
  local manifest="$2"

  for attempt in 1 2 3 4 5; do
    if "${KUBECTL}" --context "${CONTEXT}" apply -f "${manifest}"; then
      return 0
    fi
    echo "Retrying ${description} install (${attempt}/5)" >&2
    sleep 2
  done

  echo "failed to apply ${description}: ${manifest}" >&2
  return 1
}

if [[ ! -x "${KWOK_NODES_BIN}" ]]; then
  echo "Building ${KWOK_NODES_BIN}"
  (cd "${REPO_ROOT}" && go build -o "${KWOK_NODES_BIN}" ./cmd/kwok-nodes)
fi

TMP_DIR=""
if [[ -n "${OUTPUT}" ]]; then
  mkdir -p "$(dirname "${OUTPUT}")"
  MANIFEST="${OUTPUT}"
else
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "${TMP_DIR}"' EXIT
  MANIFEST="${TMP_DIR}/kwok-nodes.yaml"
fi

"${KWOK_NODES_BIN}" \
  --model "${MODEL}" \
  --output "${MANIFEST}" \
  --cpu "${CPU}" \
  --memory "${MEMORY}" \
  --pods "${PODS}" \
  --ephemeral-storage "${EPHEMERAL_STORAGE}" \
  --gpus "${GPUS}" \
  --gpu-resource-name "${GPU_RESOURCE_NAME}"

if "${KIND}" get clusters 2>/dev/null | grep -Fxq "${CLUSTER}"; then
  echo "Reusing kind cluster ${CLUSTER}"
else
  create_args=(create cluster --name "${CLUSTER}" --wait "${WAIT}")
  if [[ -n "${KIND_CONFIG}" ]]; then
    create_args+=(--config "${KIND_CONFIG}")
  fi
  "${KIND}" "${create_args[@]}"
fi

KWOK_MANIFEST_URL="$(kwok_release_url kwok.yaml)"
KWOK_STAGE_URL="$(kwok_release_url stage-fast.yaml)"

"${KUBECTL}" --context "${CONTEXT}" apply -f "${KWOK_MANIFEST_URL}"
apply_with_retry "KWOK stage configuration" "${KWOK_STAGE_URL}"
"${KUBECTL}" --context "${CONTEXT}" apply -f "${MANIFEST}"

echo "kind cluster ${CONTEXT} is ready with KWOK nodes from ${MODEL}"
if [[ -n "${OUTPUT}" ]]; then
  echo "Node manifest written to ${OUTPUT}"
fi
