#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [--push] <vX.Y.Z>

Resolve the current tip of the matching vX.Y.Z branch. By default, the
script only validates and displays the commit. With --push, it creates and
pushes the annotated release tag.

Options:
  --push     Create and push the annotated tag after validation.
  -h, --help Show this help.
EOF
}

push_tag=false

case "${1:-}" in
    --push)
        push_tag=true
        shift
        ;;
    -h|--help)
        usage
        exit 0
        ;;
    --*)
        echo "unknown option: $1" >&2
        usage >&2
        exit 2
        ;;
esac

if (( $# != 1 )); then
    usage >&2
    exit 2
fi

release_tag="$1"

if [[ ! "${release_tag}" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    echo "release tag must match vX.Y.Z; got ${release_tag}" >&2
    exit 2
fi

release_branch="${release_tag}"

if ! command -v git >/dev/null 2>&1; then
    echo "required command not found: git" >&2
    exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
cd "${repo_root}"

git fetch --prune --no-tags origin \
    "+refs/heads/${release_branch}:refs/remotes/origin/${release_branch}"

release_ref="refs/remotes/origin/${release_branch}"
release_head=$(git rev-parse --verify "${release_ref}^{commit}")

remote_tag=$(git ls-remote --tags origin "refs/tags/${release_tag}")
if [[ -n "${remote_tag}" ]]; then
    echo "remote tag already exists: ${release_tag}" >&2
    exit 1
fi

local_tag_exists=false
if git rev-parse --verify --quiet "refs/tags/${release_tag}" >/dev/null; then
    local_tag_commit=$(git rev-parse "refs/tags/${release_tag}^{commit}")
    local_tag_type=$(git cat-file -t "refs/tags/${release_tag}")
    if [[ "${local_tag_commit}" != "${release_head}" ]]; then
        echo "local tag ${release_tag} targets ${local_tag_commit}, not ${release_head}" >&2
        exit 1
    fi
    if [[ "${local_tag_type}" != "tag" ]]; then
        echo "local tag ${release_tag} is not annotated" >&2
        exit 1
    fi
    local_tag_exists=true
fi

echo "Release tag validation passed:"
echo "  tag:    ${release_tag}"
echo "  branch: ${release_branch}"
echo "  commit: ${release_head}"
git show --no-patch --oneline "${release_head}"

if [[ "${push_tag}" != "true" ]]; then
    echo
    echo "Dry run only; re-run with --push to create and push ${release_tag}."
    exit 0
fi

if [[ "${local_tag_exists}" != "true" ]]; then
    git tag -a "${release_tag}" "${release_head}" -m "Release ${release_tag}"
fi

git push origin "refs/tags/${release_tag}"
echo "Pushed ${release_tag}."
