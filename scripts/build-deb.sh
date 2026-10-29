#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

set -e

REPO_HOME=$(readlink -f $(dirname $(readlink -f "$0"))/../)

VERSION="${1#v}"
RELEASE="${2//_/-}"
ARCH="${ARCH:-$(dpkg --print-architecture)}"

: "${VERSION:?Missing argument}"

# Debian package version must start with a digit
if [[ ! $VERSION =~ ^[0-9] ]]; then
  VERSION="0.$VERSION"
fi

echo "Setting Debian package version to $VERSION"

cp "$REPO_HOME/deb/control" "$REPO_HOME/deb/topograph/DEBIAN/control"

sed -i "s/__VERSION__/$VERSION/g" "$REPO_HOME/deb/topograph/DEBIAN/control"
sed -i "s/__ARCH__/$ARCH/g" "$REPO_HOME/deb/topograph/DEBIAN/control"
if [[ -n "${RELEASE}" ]]; then
    sed -i "s/__RELEASE__/-${RELEASE}/g" "$REPO_HOME/deb/topograph/DEBIAN/control"
else
    sed -i "s/__RELEASE__//g" "$REPO_HOME/deb/topograph/DEBIAN/control"
fi

# Copy binary
mkdir -p "$REPO_HOME/deb/topograph/usr/local/bin"
cp "$REPO_HOME/bin/topograph" "$REPO_HOME/deb/topograph/usr/local/bin/"

# Copy config file and helper scripts
mkdir -p "$REPO_HOME/deb/topograph/etc/topograph"
cp "$REPO_HOME/config/topograph-config.yaml" \
  "$REPO_HOME/scripts/configure-ssl.sh" \
  "$REPO_HOME/scripts/create-topology-update-script.sh" \
  "$REPO_HOME/deb/topograph/etc/topograph"

# Copy service file
mkdir -p "$REPO_HOME/deb/topograph/lib/systemd/system"
cp "$REPO_HOME/config/topograph.service" \
  "$REPO_HOME/deb/topograph/lib/systemd/system"

# Build Debian package
chmod -R 0755 "$REPO_HOME/deb/topograph/DEBIAN"

# Optional overrides for downstream packagers:
#   DEB_COMPRESSION  - passed to dpkg-deb -Z (default: dpkg-deb's xz)
#   DEB_OUTPUT_DIR   - directory the .deb is written to (default: ${REPO_HOME}/bin)
if [[ -n "${DEB_COMPRESSION}" ]]; then
  dpkg-deb --build -Z"${DEB_COMPRESSION}" "$REPO_HOME/deb/topograph"
else
  dpkg-deb --build "$REPO_HOME/deb/topograph"
fi
DEB_OUTPUT_DIR="${DEB_OUTPUT_DIR:-${REPO_HOME}/bin}"
DEB_FILE_PATH="${DEB_OUTPUT_DIR}/topograph-$1.${ARCH}.deb"

# Create package parent directory
mkdir -p "$(dirname "${DEB_FILE_PATH}")"
mv "$REPO_HOME/deb/topograph.deb" "${DEB_FILE_PATH}"
echo "Created ${DEB_FILE_PATH}"
