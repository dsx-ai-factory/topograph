#!/usr/bin/env bash
# Copyright 2026 NVIDIA CORPORATION
# SPDX-License-Identifier: Apache-2.0

# Shared by the cosign-attest-multiplatform and cosign-verify-image actions.
# Sourced, not executed: it defines one function and changes no shell options.
#
# resolve_platform_digests <image-name> <index-digest> <platforms>
#
# Reads the image index at <image-name>@<index-digest> and prints one
# "<os>/<arch>[/<variant>] <digest>" line per platform listed in <platforms>
# (comma separated, for example "linux/amd64,linux/arm64"), in that order.
#
# It fails closed, because each failure below would otherwise let a release
# publish a platform with no SBOM or VEX while every later check still passed:
#
#   - the reference is not a multi-platform index (a single-platform push);
#   - the index ships a platform that <platforms> does not list;
#   - a listed platform is missing, or appears more than once;
#   - a child digest equals the index digest, is malformed, or is shared by
#     two platforms.
#
# BuildKit attestation manifests (platform unknown/unknown, annotated
# vnd.docker.reference.type=attestation-manifest) are not platforms and are
# skipped with a warning. docker.yml builds with `provenance: false`, so a
# released index should not contain any.
resolve_platform_digests() {
  local image="$1" index_digest="$2" platforms_csv="$3"
  local raw entries covered platform digest line found count result=""
  local -a wanted=()

  if [[ ! "${index_digest}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    echo "::error::index digest must be sha256:<64 hex>, got '${index_digest}'" >&2
    return 1
  fi

  IFS=',' read -r -a wanted <<<"${platforms_csv// /}"
  if [[ "${#wanted[@]}" -eq 0 ]]; then
    echo "::error::no platforms given" >&2
    return 1
  fi
  covered=" ${wanted[*]} "

  if ! raw="$(timeout --foreground 120s docker buildx imagetools inspect --raw "${image}@${index_digest}")"; then
    echo "::error::could not read the manifest of ${image}@${index_digest}" >&2
    return 1
  fi
  if [[ -z "${raw//[[:space:]]/}" ]]; then
    echo "::error::the registry returned an empty manifest for ${image}@${index_digest}" >&2
    return 1
  fi

  if ! entries="$(jq -r '
      if ((.mediaType == "application/vnd.oci.image.index.v1+json")
          or (.mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"))
         and ((.manifests | type) == "array")
      then
        .manifests[]
        | if (.annotations // {})["vnd.docker.reference.type"] == "attestation-manifest"
          then "attestation \(.digest)"
          else "\(.platform.os // "unknown")/\(.platform.architecture // "unknown")\(if .platform.variant then "/" + .platform.variant else "" end) \(.digest)"
          end
      else
        error("not a multi-platform image index (mediaType \(.mediaType // "absent"))")
      end' <<<"${raw}")"; then
    echo "::error::${image}@${index_digest} is not a multi-platform image index" >&2
    return 1
  fi

  # Every child must be a platform this caller covers, with a sane digest.
  while read -r platform digest; do
    [[ -z "${platform}" ]] && continue
    if [[ "${platform}" == "attestation" ]]; then
      echo "::warning::${image}@${index_digest} carries a BuildKit attestation manifest (${digest}); it is unsigned and is not attested here" >&2
      continue
    fi
    case "${covered}" in
      *" ${platform} "*) ;;
      *)
        echo "::error::${image}@${index_digest} ships ${platform}, which is not in the covered platforms (${platforms_csv}); it would be released with no SBOM or VEX" >&2
        return 1
        ;;
    esac
    if [[ ! "${digest}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
      echo "::error::${platform} child digest '${digest}' is not sha256:<64 hex>" >&2
      return 1
    fi
    if [[ "${digest}" == "${index_digest}" ]]; then
      echo "::error::${platform} resolved to the index digest itself" >&2
      return 1
    fi
  done <<<"${entries}"

  # Every covered platform must be present exactly once, on its own manifest.
  local seen_digests=" "
  for platform in "${wanted[@]}"; do
    count=0
    found=""
    while read -r line; do
      if [[ "${line%% *}" == "${platform}" ]]; then
        count=$((count + 1))
        found="${line#* }"
      fi
    done <<<"${entries}"
    if [[ "${count}" -ne 1 ]]; then
      echo "::error::${image}@${index_digest} lists ${platform} ${count} times; expected exactly once" >&2
      return 1
    fi
    case "${seen_digests}" in
      *" ${found} "*)
        echo "::error::${platform} shares manifest ${found} with another platform" >&2
        return 1
        ;;
    esac
    seen_digests+="${found} "
    result+="${platform} ${found}"$'\n'
  done

  # Printed only once every platform has passed, so a caller never sees a
  # partial list.
  printf '%s' "${result}"
}
