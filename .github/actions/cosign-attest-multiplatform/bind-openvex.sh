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
#   - a statement lacks `vulnerability.name` or a valid `status`, or breaks
#     the OpenVEX v0.2.0 rules for `not_affected` (justification or impact
#     statement) and `affected` (action statement);
#   - a product names anything other than the bare `pkg:oci/<name>`, through
#     `@id`, `identifiers.purl`, or any other identifier (binding it would
#     publish a signed claim about something never triaged against this
#     image);
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

# Content checks, matching the OpenVEX v0.2.0 rules the repository's
# .openvex.json validator enforces. Each problem is printed on its own line; no
# output means valid. An empty `statements` array is valid.
problems="$(jq -r --arg context "${context}" --arg bare "${bare}" '
    def statuses: ["not_affected", "affected", "fixed", "under_investigation"];
    def justifications: [
      "component_not_present",
      "vulnerable_code_not_present",
      "vulnerable_code_not_in_execute_path",
      "vulnerable_code_cannot_be_controlled_by_adversary",
      "inline_mitigations_already_exist"
    ];
    def filled: type == "string" and test("[^[:space:]]");
    def one_of($allowed): type == "string" and IN($allowed[]);
    # Problems with one product. The projection replaces `@id` and
    # `identifiers` wholesale, so anything there that does not name the bare
    # image would be silently rewritten into a signed claim: reject it.
    def product_problems($i; $j):
      if type != "object" then "statement \($i) product \($j) is not an object"
      else
        (if .["@id"] == null or .["@id"] == $bare then empty
         else "statement \($i) product \($j) @id \(.["@id"] | tojson) is not \($bare)" end),
        (if .identifiers == null then empty
         elif (.identifiers | type) != "object" then "statement \($i) product \($j) identifiers is not an object"
         else
           (if (.identifiers | keys - ["purl"]) == [] then empty
            else "statement \($i) product \($j) identifiers has keys other than purl: \(.identifiers | keys - ["purl"] | join(", "))" end),
           (if .identifiers.purl == null or .identifiers.purl == $bare then empty
            else "statement \($i) product \($j) identifiers.purl \(.identifiers.purl | tojson) is not \($bare)" end)
         end),
        (if .["@id"] != null or (.identifiers | type) == "object" and .identifiers.purl != null then empty
         else "statement \($i) product \($j) has neither @id nor identifiers.purl" end)
      end;
    def statement_problems($i):
      if type != "object" then "statement \($i) is not an object"
      else
        .status as $status | .justification as $justification
        | (if (.vulnerability | type) == "object" and (.vulnerability.name | filled) then empty
           else "statement \($i) must set vulnerability.name to a non-empty string" end),
          (if $status | one_of(statuses) then empty
           else "statement \($i) status \($status | tojson) is not one of \(statuses | join(", "))" end),
          (if $justification == null or ($justification | one_of(justifications)) then empty
           else "statement \($i) justification \($justification | tojson) is not one of \(justifications | join(", "))" end),
          (if $status == "not_affected"
              and (($justification | one_of(justifications)) | not)
              and ((.impact_statement | filled) | not)
           then "statement \($i) is not_affected and needs a justification or an impact_statement"
           else empty end),
          (if $status == "affected" and ((.action_statement | filled) | not)
           then "statement \($i) is affected and needs an action_statement"
           else empty end),
          (if (.products | type) == "array" and (.products | length) > 0
           then (.products | to_entries[] | .key as $j | .value | product_problems($i; $j))
           else "statement \($i) lists no products" end)
      end;
    [ (if .["@context"] == $context then empty
       else "@context must be \($context), got \(.["@context"] | tojson)" end),
      (if (.["@id"] | type) == "string" and (.["@id"] | length) > 0 then empty
       else "@id must be a non-empty string" end),
      (.statements | to_entries[] | .key as $i | .value | statement_problems($i))
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
