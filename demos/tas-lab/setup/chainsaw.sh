#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Setup step 5: install the Kyverno Chainsaw CLI (CHAINSAW_VERSION in versions.env) that runs the
# declarative scenarios under scenarios/. It is a client tool, so nothing is installed in the
# cluster. A chainsaw of the right version already on PATH, or in .bin from an earlier run, is
# reused; otherwise the release for this OS and CPU is downloaded, its checksum verified, and
# unpacked into .bin (no sudo, ignored by git).

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

bin_dir="$lab_dir/.bin"
want="${CHAINSAW_VERSION#v}"

installed_version() { # <binary>: prints the version, or nothing when it cannot run
    "$1" version 2>/dev/null | awk '/^Version:/ { sub(/^v/, "", $2); print $2 }'
}

# scenarios/run.sh puts .bin first, so a stale copy there would shadow a matching chainsaw on PATH:
# remove it (a matching one is kept, and a missing match is downloaded below).
if [[ -e "$bin_dir/chainsaw" && "$(installed_version "$bin_dir/chainsaw")" != "$want" ]]; then
    echo "removing the stale $bin_dir/chainsaw (version: $(installed_version "$bin_dir/chainsaw" || true))"
    rm -f "$bin_dir/chainsaw"
fi

for candidate in "$bin_dir/chainsaw" "$(command -v chainsaw || true)"; do
    if [[ -n "$candidate" && -x "$candidate" && "$(installed_version "$candidate")" == "$want" ]]; then
        echo "chainsaw $want already installed: $candidate"
        exit 0
    fi
done

case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) echo "error: no Chainsaw release for $(uname -s); install it from https://kyverno.github.io/chainsaw/" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) echo "error: no Chainsaw release for $(uname -m); install it from https://kyverno.github.io/chainsaw/" >&2; exit 1 ;;
esac

asset="chainsaw_${os}_${arch}.tar.gz"
url="https://github.com/kyverno/chainsaw/releases/download/$CHAINSAW_VERSION"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading chainsaw $CHAINSAW_VERSION ($asset)"
curl -fsSL -o "$tmp/$asset" "$url/$asset" \
    || { echo "error: could not download $url/$asset (is CHAINSAW_VERSION=$CHAINSAW_VERSION a release?)" >&2; exit 1; }
curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt" \
    || { echo "error: could not download $url/checksums.txt" >&2; exit 1; }

expected="$(awk -v a="$asset" '$2 == a { print $1 }' "$tmp/checksums.txt")"
[[ -n "$expected" ]] || { echo "error: $asset is not listed in checksums.txt" >&2; exit 1; }
if command -v sha256sum >/dev/null; then
    actual="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
    actual="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
[[ "$actual" == "$expected" ]] || { echo "error: checksum mismatch for $asset" >&2; exit 1; }

mkdir -p "$bin_dir"
tar -xzf "$tmp/$asset" -C "$tmp" chainsaw
mv "$tmp/chainsaw" "$bin_dir/chainsaw"
chmod +x "$bin_dir/chainsaw"
echo "Installed $("$bin_dir/chainsaw" version | awk '/^Version:/ { print $2 }') to $bin_dir/chainsaw"
