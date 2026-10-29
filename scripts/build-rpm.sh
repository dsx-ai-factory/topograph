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
RELEASE="${2-1}"
ARCH="${ARCH:-$(uname -m)}"

: "${VERSION:?Missing argument}"

# RPM package version must not have '-'
VERSION=$(echo "$VERSION" | sed 's/-/_/g')

echo "Setting RPM package version to ${VERSION}-${RELEASE}"

mkdir -p rpmbuild/{BUILD,RPMS,SOURCES,SPECS,SRPMS}

cp $REPO_HOME/rpmbuild/topograph.spec $REPO_HOME/rpmbuild/SPECS/topograph.spec

sed -i "s/__VERSION__/$VERSION/g" $REPO_HOME/rpmbuild/SPECS/topograph.spec
sed -i "s/__RELEASE__/${RELEASE}/g" $REPO_HOME/rpmbuild/SPECS/topograph.spec
sed -i "s/__ARCH__/${ARCH}/g" $REPO_HOME/rpmbuild/SPECS/topograph.spec

cp $REPO_HOME/bin/topograph \
  $REPO_HOME/config/topograph-config.yaml \
  $REPO_HOME/config/topograph.service \
  $REPO_HOME/scripts/configure-ssl.sh \
  $REPO_HOME/scripts/create-topology-update-script.sh \
  $REPO_HOME/rpmbuild/SOURCES/

rpmbuild --define "_topdir $REPO_HOME/rpmbuild" -bb --clean $REPO_HOME/rpmbuild/SPECS/topograph.spec

# Optional override for downstream packagers:
#   RPM_OUTPUT_DIR - directory the .rpm is written to (default: ${REPO_HOME}/bin)
RPM_OUTPUT_DIR="${RPM_OUTPUT_DIR:-${REPO_HOME}/bin}"
mkdir -p "${RPM_OUTPUT_DIR}"
RPM_OUTPUT_FILE="${RPM_OUTPUT_DIR}/topograph-$1.${ARCH}.rpm"
mv $REPO_HOME/rpmbuild/RPMS/${ARCH}/topograph-${VERSION}-${RELEASE}.${ARCH}.rpm "${RPM_OUTPUT_FILE}"
echo "Created ${RPM_OUTPUT_FILE}"
