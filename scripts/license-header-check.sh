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

# Checks the form of license headers, for `make license-header-check`. Reads
# NUL-separated paths on stdin. addlicense checks that a header exists; this
# script checks what addlicense cannot:
#
# - Helm .tpl files and NOTES.txt, which addlicense does not read, have one.
# - An NVIDIA header is scripts/license-header.tmpl, line for line, with any
#   year or year range and the file's comment prefix (`//`, `#` or none).
# - No NVIDIA header uses an older form.
#
# Headers of other copyright holders are not checked. The skipped paths match
# LICENSE_HEADER_IGNORE in the Makefile.

set -euo pipefail

tmpl=scripts/license-header.tmpl
lines=$(wc -l <"$tmpl")
want=$(cat "$tmpl")
spdx='SPDX-FileCopyrightText: Copyright \(c\) [0-9]{4}(-[0-9]{4})? NVIDIA CORPORATION'
old='Copyright (\(c\) )?[0-9]{4}(-[0-9]{4})?,? NVIDIA CORPORATION([.]|$)'
status=0

report() {
	echo "$1: $2"
	status=1
}

while IFS= read -r -d '' f; do
	case "$f" in
	vendor/* | testdata/* | */testdata/* | */__snapshot__/*) continue ;;
	esac
	# Skip directories (nested repositories), binary and empty files.
	if [ ! -f "$f" ] || ! grep -qI . "$f"; then
		continue
	fi
	head=$(head -n 40 "$f")

	case "$f" in
	charts/*.tpl | charts/*.txt)
		if ! grep -q -E 'Copyright|SPDX-License-Identifier' <<<"$head"; then
			report "$f" "missing license header"
			continue
		fi
		;;
	esac

	if grep -q -E "$old" <<<"$head"; then
		report "$f" "NVIDIA license header in an older form"
		continue
	fi

	start=$(grep -n -m1 -E "$spdx" <<<"$head" | cut -d: -f1) || continue
	got=$(awk -v s="$start" -v n="$lines" 'NR >= s && NR < s + n' "$f" |
		sed -E 's#^(//|\#) ?##; s/Copyright \(c\) [0-9]{4}(-[0-9]{4})? NVIDIA/Copyright (c) {{.Year}} NVIDIA/')
	if [ "$got" != "$want" ]; then
		report "$f" "NVIDIA license header differs from $tmpl"
	fi
done

if [ "$status" -ne 0 ]; then
	echo "Replace these headers with $tmpl in the file's comment syntax, keeping the year."
fi
exit "$status"
