#!/usr/bin/env bash
# Copyright 2026 NVIDIA CORPORATION
# SPDX-License-Identifier: Apache-2.0

# bind-openvex.sh <source> <output> <product-name> <platform-digest>
#
# Projects the committed OpenVEX document onto one platform manifest, then
# validates what came out before cosign is allowed to sign it.
#
# The committed `.openvex.json` names the bare product `pkg:oci/<name>`, so one
# statement covers every build. A signed VEX attestation is only verifiable
# when its product names the manifest it is attached to, so every product in
# the projection becomes `pkg:oci/<name>@<platform-digest>`. Subcomponents,
# statuses, justifications and impact statements pass through unchanged. The
# document `@id` gains a `#<name>@<digest>` fragment, because a projection is a
# different document from its source. The committed file is never modified.
#
# Fails closed when:
#   - the source is not OpenVEX v0.2.0, or `statements` is not an array;
#   - a statement names a product other than the bare `pkg:oci/<name>`
#     (binding it would publish a signed claim about something never triaged
#     against this image);
#   - the projection lost a statement, or any product in it does not carry the
#     platform digest.
#
# An empty `statements` array is valid and is attested as is: it asserts that
# the maintainers claim no exceptions, not that the image has no
# vulnerabilities.

set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 <source> <output> <product-name> <platform-digest>" >&2
  exit 2
fi

source_file="$1"
output_file="$2"
name="$3"
digest="$4"

if [[ ! "${digest}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  echo "::error::platform digest must be sha256:<64 hex>, got '${digest}'" >&2
  exit 1
fi
if [[ ! "${name}" =~ ^[a-z0-9]+([._-][a-z0-9]+)*$ ]]; then
  echo "::error::product name '${name}' is not a valid OCI repository name" >&2
  exit 1
fi
if [[ ! -s "${source_file}" ]]; then
  echo "::error::OpenVEX source ${source_file} is missing or empty" >&2
  exit 1
fi

context="https://openvex.dev/ns/v0.2.0"
bare="pkg:oci/${name}"
bound="${bare}@${digest}"

# Syntax first, then shape, then content, so each failure says what is wrong.
if ! jq empty "${source_file}" 2>/dev/null; then
  echo "::error::OpenVEX source ${source_file} is not valid JSON" >&2
  exit 1
fi
if ! jq -s -e 'length == 1 and (.[0] | type) == "object" and (.[0].statements | type) == "array"' \
  "${source_file}" >/dev/null; then
  echo "::error::OpenVEX source ${source_file} must be a single JSON object whose statements field is an array" >&2
  exit 1
fi

# Content checks. Each problem is printed on its own line; no output means valid.
problems="$(jq -r --arg context "${context}" --arg bare "${bare}" '
    def products: if (.products | type) == "array" then .products else [] end;
    def ident: if (.identifiers | type) == "object" then .identifiers.purl else null end;
    def names_bare:
      type == "object"
      and (.["@id"] == null or .["@id"] == $bare)
      and (ident == null or ident == $bare)
      and (.["@id"] != null or ident != null);
    [ (if .["@context"] == $context then empty
       else "@context must be \($context), got \(.["@context"] | tojson)" end),
      (if (.["@id"] | type) == "string" and (.["@id"] | length) > 0 then empty
       else "@id must be a non-empty string" end),
      (.statements | to_entries[]
       | .key as $i | .value
       | if type != "object" then "statement \($i) is not an object"
         elif (products | length) == 0 then "statement \($i) lists no products"
         elif (products | all(names_bare)) then empty
         else "statement \($i) names a product other than \($bare)" end)
    ] | .[]' "${source_file}")"
if [[ -n "${problems}" ]]; then
  while IFS= read -r problem; do
    echo "::error::OpenVEX source ${source_file}: ${problem}" >&2
  done <<<"${problems}"
  exit 1
fi

jq --arg bound "${bound}" --arg suffix "#${name}@${digest}" '
  .["@id"] += $suffix
  | .statements |= map(
      .products |= map(
        .["@id"] = $bound
        | .identifiers = {purl: $bound}
        | del(.hashes)))
' "${source_file}" >"${output_file}"

# Projection checks: nothing was lost and every product carries the digest.
source_count="$(jq '.statements | length' "${source_file}")"
if ! jq -e --arg context "${context}" --arg bound "${bound}" --argjson n "${source_count}" '
    .["@context"] == $context
    and (.["@id"] | endswith("@" + ($bound | split("@")[1])))
    and (.statements | length) == $n
    and all(.statements[]; all(.products[]; .["@id"] == $bound and .identifiers.purl == $bound))
  ' "${output_file}" >/dev/null; then
  echo "::error::OpenVEX projection ${output_file} is not bound to ${bound}" >&2
  exit 1
fi

echo "Bound ${source_count} OpenVEX statement(s) to ${bound}"
