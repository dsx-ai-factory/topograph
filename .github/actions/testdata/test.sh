#!/usr/bin/env bash
# Copyright 2026 NVIDIA CORPORATION
# SPDX-License-Identifier: Apache-2.0

# Offline tests for the scripts the release signing actions run:
#
#   .github/actions/oci-platform-digests.sh                       (platform resolution)
#   .github/actions/cosign-attest-multiplatform/bind-openvex.sh   (VEX binding)
#   .github/actions/cosign-verify-image/check-identity-regexp.sh  (identity anchoring)
#
# Nothing touches a registry. `docker` is replaced by a PATH shim that prints
# a committed fixture from index/, and each case asserts the exit status and a
# line of the output. Run from anywhere: ./.github/actions/testdata/test.sh

set -euo pipefail

testdata="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
actions="$(dirname "${testdata}")"
work="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/release-actions-test.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

# PATH shim: `docker buildx imagetools inspect --raw <ref>` prints $FIXTURE.
# Any other invocation fails, so a code change that starts calling docker
# differently is caught here rather than on a release.
mkdir -p "${work}/bin"
cat >"${work}/bin/docker" <<'EOF'
#!/usr/bin/env bash
if [[ "$*" != "buildx imagetools inspect --raw "* ]]; then
  echo "docker shim: unexpected arguments: $*" >&2
  exit 2
fi
exec cat "${FIXTURE:?}"
EOF
chmod +x "${work}/bin/docker"
# The resolver bounds its registry call with coreutils `timeout`, which macOS
# lacks; shim it there so the tests also run on a laptop.
if ! command -v timeout >/dev/null; then
  cat >"${work}/bin/timeout" <<'EOF'
#!/usr/bin/env bash
while [[ "$1" == --* ]]; do shift; done
shift
exec "$@"
EOF
  chmod +x "${work}/bin/timeout"
fi
export PATH="${work}/bin:${PATH}"

IMAGE=ghcr.io/dsx-ai-factory/topograph
INDEX="sha256:$(printf 'a%.0s' {1..64})"
AMD64="sha256:$(printf '1%.0s' {1..64})"
ARM64="sha256:$(printf '2%.0s' {1..64})"
PLATFORMS="linux/amd64,linux/arm64"

failures=0
cases=0

# expect <name> <want-status> <want-output-substring> -- <command...>
expect() {
  local name="$1" want_status="$2" want_text="$3" status=0 output
  shift 4
  cases=$((cases + 1))
  output="$("$@" 2>&1)" || status=$?
  if [[ "${status}" -ne "${want_status}" ]]; then
    echo "FAIL ${name}: exit ${status}, want ${want_status}"
    printf '%s\n' "${output}" | sed 's/^/    /'
    failures=$((failures + 1))
  elif [[ "${output}" != *"${want_text}"* ]]; then
    echo "FAIL ${name}: output lacks '${want_text}'"
    printf '%s\n' "${output}" | sed 's/^/    /'
    failures=$((failures + 1))
  else
    echo "ok   ${name}"
  fi
}

resolve() {
  FIXTURE="$1" bash -c '
    set -euo pipefail
    source "$0"
    resolve_platform_digests "$1" "$2" "$3"
  ' "${actions}/oci-platform-digests.sh" "${IMAGE}" "${INDEX}" "${PLATFORMS}"
}

both="linux/amd64 ${AMD64}"$'\n'"linux/arm64 ${ARM64}"

echo "== oci-platform-digests.sh"
expect "OCI index" 0 "${both}" -- resolve "${testdata}/index/oci-index.json"
expect "Docker manifest list" 0 "${both}" -- resolve "${testdata}/index/docker-manifest-list.json"
expect "BuildKit attestation entries are skipped with a warning" 0 \
  "::warning::${IMAGE}@${INDEX} carries a BuildKit attestation manifest" \
  -- resolve "${testdata}/index/buildkit-attestation.json"
expect "BuildKit attestation entries do not change the result" 0 "${both}" \
  -- resolve "${testdata}/index/buildkit-attestation.json"
expect "bare unknown/unknown entry is an uncovered platform" 1 "ships unknown/unknown" \
  -- resolve "${testdata}/index/unknown-platform.json"
expect "extra platform" 1 "ships linux/s390x" -- resolve "${testdata}/index/extra-platform.json"
expect "missing platform" 1 "lists linux/arm64 0 times" -- resolve "${testdata}/index/missing-platform.json"
expect "two platforms share a digest" 1 "shares manifest ${AMD64}" \
  -- resolve "${testdata}/index/duplicate-digest.json"
expect "child digest equals the index digest" 1 "resolved to the index digest itself" \
  -- resolve "${testdata}/index/self-digest.json"
expect "single manifest" 1 "is not a multi-platform image index" \
  -- resolve "${testdata}/index/single-manifest.json"
expect "index with no manifests" 1 "lists linux/amd64 0 times" -- resolve "${testdata}/index/empty-index.json"
expect "empty registry response" 1 "returned an empty manifest" -- resolve "${testdata}/index/empty.json"
expect "registry error" 1 "could not read the manifest" -- resolve "${testdata}/index/does-not-exist.json"

echo "== bind-openvex.sh"
bind() {
  "${actions}/cosign-attest-multiplatform/bind-openvex.sh" "$1" "${work}/projection.json" topograph "${2:-${ARM64}}"
}
bound="pkg:oci/topograph@${ARM64}"

expect "statement bound to the platform digest" 0 "Bound 1 OpenVEX statement(s) to ${bound}" \
  -- bind "${testdata}/openvex/good.json"
# shellcheck disable=SC2016 # $bound is a jq variable, not a shell one.
expect "projection products, subcomponents and @id" 0 "true" -- jq -e --arg bound "${bound}" '
    .statements[0].products == [{
      "@id": $bound,
      "identifiers": {"purl": $bound},
      "subcomponents": [{"@id": "pkg:golang/example.com/module@v1.2.3"}]
    }]
    and (.["@id"] | endswith("#topograph@" + ($bound | split("@")[1])))
    and .statements[0].status == "not_affected"' "${work}/projection.json"
expect "empty statements" 0 "Bound 0 OpenVEX statement(s)" -- bind "${testdata}/openvex/empty-statements.json"
expect "empty statements stay empty" 0 "true" -- jq -e '.statements == []' "${work}/projection.json"
expect "foreign product" 1 'statement 0 product 0 @id "pkg:oci/other-image" is not pkg:oci/topograph' \
  -- bind "${testdata}/openvex/foreign-product.json"
expect "qualified purl" 1 'statement 0 product 0 @id "pkg:oci/topograph?arch=amd64" is not pkg:oci/topograph' \
  -- bind "${testdata}/openvex/qualified-purl.json"
expect "identifier other than purl" 1 "statement 0 product 0 identifiers has keys other than purl: cpe23" \
  -- bind "${testdata}/openvex/extra-identifier.json"
expect "identifiers.purl differs from @id" 1 \
  'statement 0 product 0 identifiers.purl "pkg:oci/other-image" is not pkg:oci/topograph' \
  -- bind "${testdata}/openvex/mismatched-purl.json"
expect "missing vulnerability" 1 "statement 0 must set vulnerability.name to a non-empty string" \
  -- bind "${testdata}/openvex/missing-vulnerability.json"
expect "missing status" 1 "statement 0 status null is not one of not_affected, affected, fixed, under_investigation" \
  -- bind "${testdata}/openvex/missing-status.json"
expect "unknown status" 1 'statement 0 status "not_vulnerable" is not one of' \
  -- bind "${testdata}/openvex/unknown-status.json"
expect "not_affected without justification or impact statement" 1 \
  "statement 0 is not_affected and needs a justification or an impact_statement" \
  -- bind "${testdata}/openvex/not-affected-unjustified.json"
expect "affected without action statement" 1 "statement 0 is affected and needs an action_statement" \
  -- bind "${testdata}/openvex/affected-without-action.json"
expect "valid JSON, wrong shape" 1 "must be a single JSON object whose statements field is an array" \
  -- bind "${testdata}/openvex/wrong-shape.json"
expect "invalid JSON" 1 "is not valid JSON" -- bind "${testdata}/openvex/not-json.json"
expect "malformed digest" 1 "platform digest must be sha256:<64 hex>" \
  -- bind "${testdata}/openvex/good.json" sha256:abc

echo "== check-identity-regexp.sh"
check_re() { "${actions}/cosign-verify-image/check-identity-regexp.sh" "$1"; }
docker_identity="$(sed -n "s/^ *certificate-identity-regexp: '\(.*\)'\$/\1/p" "${actions}/../workflows/docker.yml")"
expect "docker.yml identity regexp is read" 0 "docker" -- printf '%s' "${docker_identity}"
expect "docker.yml identity regexp passes" 0 "" -- check_re "${docker_identity}"
expect "alternation inside a group passes" 0 "" -- check_re '^https://example\.com/(a|b)$'
expect "unanchored fails" 1 "must start with ^" -- check_re 'https://github\.com/dsx-ai-factory/topograph/'
expect "top-level alternation fails" 1 "has a top-level |" -- check_re '^a|b$'
expect "missing \$ fails" 1 'must end with $' -- check_re '^https://github\.com/dsx-ai-factory/topograph/.*'
expect "escaped final \$ fails" 1 'must end with an unescaped $' -- check_re '^https://example\.com/a\$'
expect "unbalanced group fails" 1 "has an unbalanced (" -- check_re '^a(b|c$'
expect "quoted parentheses fail" 1 'must not use \Q...\E literal quoting' -- check_re '^trusted\Q(\E|.*\Q)\E$'

echo "${cases} cases, ${failures} failed"
[[ "${failures}" -eq 0 ]]
