#!/usr/bin/env bash
# Copyright 2026 NVIDIA CORPORATION
# SPDX-License-Identifier: Apache-2.0

# check-identity-regexp.sh <regexp>
#
# Fails unless <regexp> is anchored over the whole certificate identity. cosign
# matches --certificate-identity-regexp as an unanchored search, so a pattern
# without `^` and `$` accepts any identity that merely contains it, and a
# top-level alternation such as `^a|b$` anchors only its outer branches: `b$`
# accepts any identity ending in b.
#
# The rule is deliberately conservative:
#   - the pattern starts with `^` and ends with an unescaped `$`;
#   - every `|` sits inside a parenthesised group, so the anchors apply to all
#     alternatives, as in `^https://...@refs/(tags/v...|heads/release-...)$`.
# Escaped characters and bracket expressions are skipped when looking for `|`.

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <regexp>" >&2
  exit 2
fi

re="$1"

fail() {
  echo "::error::certificate-identity-regexp '${re}' $1" >&2
  exit 1
}

[[ "${re}" == ^* ]] || fail "must start with ^"
[[ "${re}" == *'$' ]] || fail "must end with \$"

depth=0
in_class=false
escaped=false
for ((i = 0; i < ${#re}; i++)); do
  c="${re:i:1}"
  if [[ "${escaped}" == true ]]; then
    escaped=false
    # The final `$` must be an anchor, not an escaped literal dollar.
    if (( i == ${#re} - 1 )); then
      fail "must end with an unescaped \$"
    fi
    continue
  fi
  case "${c}" in
    \\) escaped=true ;;
    \[) in_class=true ;;
    \]) in_class=false ;;
    \() [[ "${in_class}" == true ]] || depth=$((depth + 1)) ;;
    \)) [[ "${in_class}" == true ]] || depth=$((depth - 1)) ;;
    \|)
      if [[ "${in_class}" == false && "${depth}" -eq 0 ]]; then
        fail "has a top-level |; put alternatives inside a group, as in ^prefix(a|b)\$"
      fi
      ;;
  esac
  if (( depth < 0 )); then
    fail "has an unbalanced )"
  fi
done
if (( depth != 0 )); then
  fail "has an unbalanced ("
fi
