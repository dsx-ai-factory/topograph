## Security

NVIDIA is dedicated to the security and trust of our software products and services, including all source code repositories managed through our organization.

If you need to report a security issue, please use the appropriate contact points outlined below. **Please do not report security vulnerabilities through GitHub.** If a potential security issue is inadvertently reported via a public issue or pull request, NVIDIA maintainers may limit public discussion and redirect the reporter to the appropriate private disclosure channels.

## Supported Versions

Topograph uses semantic versions of the form `vMAJOR.MINOR.PATCH` and targets one minor release
per calendar quarter. Security fixes are delivered as a new patch or minor release on the latest
release line. They are not backported to earlier lines.

| Version | Status |
| --- | --- |
| Latest minor release line (currently `v1.0.x`) | Supported. Receives security fixes. |
| Any earlier minor release line, including all `v0.x` releases | Not supported. Upgrade to the latest release. |
| `main` | Development branch. Fixes land here first, but it is not a supported release. |

Operators are expected to track the latest release line. If you are running an older version,
upgrade first and confirm the issue is still present before reporting it. How releases are cut
and published is documented in [RELEASE.md](./RELEASE.md).

## Reporting Potential Security Vulnerability in an NVIDIA Product

To report a potential security vulnerability in any NVIDIA product:
- Web: [Security Vulnerability Submission Form](https://www.nvidia.com/object/submit-security-vulnerability.html)
- E-Mail: psirt@nvidia.com
    - We encourage you to use the following PGP key for secure email communication: [NVIDIA public PGP Key for communication](https://www.nvidia.com/en-us/security/pgp-key)
    - Please include the following information:
   	 - Product/Driver name and version/branch that contains the vulnerability
     - Type of vulnerability (code execution, denial of service, buffer overflow, etc.)
   	 - Instructions to reproduce the vulnerability
   	 - Proof-of-concept or exploit code
   	 - Potential impact of the vulnerability, including how an attacker could exploit the vulnerability

## Embargo and Coordinated Disclosure

Reports about Topograph are handled under NVIDIA's coordinated vulnerability disclosure policy,
which NVIDIA PSIRT operates. What to expect between your report and public disclosure:

1. **Acknowledgement and triage.** PSIRT confirms receipt, works to reproduce the issue, and
   assesses its severity and the affected versions. PSIRT remains the point of contact for the
   report; the Topograph maintainers develop the fix behind that channel.
2. **Embargo.** The report stays under embargo while the fix is prepared. Please do not disclose
   the issue publicly, including in a GitHub issue, pull request, or discussion, in a conference
   talk, or in a blog post, until NVIDIA has publicly released the update or mitigation
   information for the issue, or PSIRT tells you that earlier disclosure is authorized. PSIRT
   coordinates that timing case by case. The
   [PSIRT policies page](https://www.nvidia.com/en-us/security/psirt-policies/) states that
   response timelines depend on the severity, the product affected, the current development
   cycle, QA cycles, and whether the issue can only be updated in a major release. This project
   does not set a disclosure timeline of its own.
3. **Pre-disclosure.** If a fix affects downstream consumers, PSIRT decides who is notified ahead
   of publication and what they are told. Please do not share details with third parties yourself
   while the report is under embargo; ask PSIRT instead.
4. **CVE assignment.** A CVE identifier for a confirmed vulnerability is assigned through NVIDIA
   PSIRT, not through this repository.
5. **Publication.** The fix ships in a release on a supported version line, as described in
   [Supported Versions](#supported-versions), and the user-facing note is recorded under the
   `### Security` heading in [CHANGELOG.md](./CHANGELOG.md). PSIRT determines whether a separate
   NVIDIA security bulletin is published, and when.

## Verifying Release Artifacts

This section applies to releases published after `v1.0.0`, the first ones cut by the release
workflow that generates build provenance and a checksum file. `v1.0.0` and every earlier release
predate that workflow: they publish the container image and the chart package only, with no
attestation for either, no checksum file, and no assets on their GitHub release pages. That is
expected for those releases, not a supply chain problem.

An official release publishes a multi-architecture container image at
`ghcr.io/dsx-ai-factory/topograph:vX.Y.Z`, a Helm chart package in the chart repository at
`https://dsx-ai-factory.github.io/topograph`, and a SHA-256 checksum file beside the chart package. The
chart package and its checksum are also attached to the GitHub release.

The image and the chart package each carry SLSA build provenance produced by GitHub artifact
attestations, which are signed through Sigstore. Verify what you downloaded before installing it,
substituting the release you are checking for `vX.Y.Z` and `X.Y.Z`.

Container image:

```bash
gh attestation verify oci://ghcr.io/dsx-ai-factory/topograph:vX.Y.Z \
  --repo dsx-ai-factory/topograph \
  --signer-workflow dsx-ai-factory/topograph/.github/workflows/docker.yml \
  --source-ref refs/tags/vX.Y.Z
```

Helm chart package and its checksum:

```bash
curl -fsSLO https://dsx-ai-factory.github.io/topograph/topograph-X.Y.Z.tgz
curl -fsSLO https://dsx-ai-factory.github.io/topograph/topograph-X.Y.Z.tgz.sha256
sha256sum --check topograph-X.Y.Z.tgz.sha256
gh attestation verify topograph-X.Y.Z.tgz \
  --repo dsx-ai-factory/topograph \
  --signer-workflow dsx-ai-factory/topograph/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z
```

For a release covered by this section, treat a failed verification, a missing attestation, or an
attestation naming a workflow or source reference other than the ones above as a potential supply
chain problem, and report it through the channel described above. A release published before that
point carries no attestation by design, so its absence is not something to report.

### Container image signature, SBOM, and OpenVEX

Images published by the **Docker** workflow (`.github/workflows/docker.yml`) for an official
`vX.Y.Z` tag or a release candidate built from a `release-X.Y` branch are also signed with
[Sigstore cosign](https://docs.sigstore.dev/) in keyless mode, and carry a
[CycloneDX](https://cyclonedx.org/) 1.6 SBOM and an [OpenVEX](https://openvex.dev/) document as
signed attestations. The workflow verifies all of it before the run succeeds. Images built before
this was added (see [CHANGELOG.md](./CHANGELOG.md)) carry the provenance attestation only, and
`cosign verify` finds no signature on them. The OpenVEX attestation is published only by releases
built after `.openvex.json` was added to the repository.

The tag resolves to an image **index**, which lists one **platform manifest** per architecture
(`linux/amd64`, `linux/arm64`). Each piece of evidence is attached to the digest it is true about,
so no single digest carries all of it:

| Evidence | Digest | cosign command |
| --- | --- | --- |
| cosign signature | Index | `cosign verify` |
| SLSA build provenance (the attestation `gh attestation verify` reads) | Index | `cosign verify-attestation --type slsaprovenance1` |
| CycloneDX SBOM | Each platform manifest | `cosign verify-attestation --type cyclonedx` |
| OpenVEX | Each platform manifest | `cosign verify-attestation --type openvex` |

An SBOM or a VEX document describes one root filesystem, and the amd64 and arm64 images do not
share one, so they are attached to the platform manifests rather than the index. Verifying the
signature against a platform digest, or looking for the SBOM on the index digest, finds nothing.
That is expected.

The OpenVEX document binds each statement to `pkg:oci/topograph@<platform digest>`. It may contain
no statements at all: an empty document asserts that the maintainers claim no exceptions to what
a scanner reports, not that the image has no vulnerabilities.

The certificate identity is the Docker workflow run that signed the image:
`https://github.com/dsx-ai-factory/topograph/.github/workflows/docker.yml@<ref>`. When you
verify a specific release, pass the exact identity of that release's ref with
`--certificate-identity`. That binds the image you resolved from `:vX.Y.Z` to the run for the
`vX.Y.Z` tag; a pattern that accepts any release would also accept an image signed for a
different release and moved under that tag. Do not loosen the identity to the organization or to
any ref: that would accept a signature produced by another workflow or branch. These commands
assume cosign v3 and
[crane](https://github.com/google/go-containerregistry/blob/main/cmd/crane/README.md):

```bash
IMAGE=ghcr.io/dsx-ai-factory/topograph
ISSUER=https://token.actions.githubusercontent.com
VERSION=vX.Y.Z
IDENTITY="https://github.com/dsx-ai-factory/topograph/.github/workflows/docker.yml@refs/tags/$VERSION"
# For a release candidate, set VERSION to the short commit SHA tag the Docker workflow
# published, and use the release branch it was built from:
#   IDENTITY="https://github.com/dsx-ai-factory/topograph/.github/workflows/docker.yml@refs/heads/release-X.Y"

# Resolve the tag to an immutable index digest once, and verify by digest from here on.
INDEX=$(crane digest "$IMAGE:$VERSION")

# Signature and SLSA provenance: index digest.
cosign verify --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity "$IDENTITY" "$IMAGE@$INDEX"
cosign verify-attestation --type slsaprovenance1 --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity "$IDENTITY" "$IMAGE@$INDEX" > /dev/null

# SBOM and OpenVEX: each platform manifest, resolved from the verified index rather than the tag.
for PLATFORM in linux/amd64 linux/arm64; do
  DIGEST=$(crane digest --platform "$PLATFORM" "$IMAGE@$INDEX")
  cosign verify-attestation --type cyclonedx --certificate-oidc-issuer "$ISSUER" \
    --certificate-identity "$IDENTITY" "$IMAGE@$DIGEST" > /dev/null
  cosign verify-attestation --type openvex --certificate-oidc-issuer "$ISSUER" \
    --certificate-identity "$IDENTITY" "$IMAGE@$DIGEST" > /dev/null
done
```

`cosign verify-attestation` prints each verified attestation as a DSSE envelope. To read the SBOM
itself, decode the first payload:

```bash
cosign verify-attestation --type cyclonedx --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity "$IDENTITY" "$IMAGE@$DIGEST" \
  | jq -r .payload | head -n 1 | base64 -d | jq .predicate > topograph-sbom.cdx.json
```

Without crane, `docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$IMAGE:vX.Y.Z"`
prints the index digest, and `docker buildx imagetools inspect --raw "$IMAGE@$INDEX"` lists the
platform manifest digests under `manifests`. Deploy by the index digest you verified, so the image
you checked is the image that runs.

To enforce the signature at admission, the Sigstore
[policy-controller](https://docs.sigstore.dev/policy-controller/overview/) (v0.13.0 or later, for
`signatureFormat: bundle`) accepts a policy such as:

```yaml
apiVersion: policy.sigstore.dev/v1beta1
kind: ClusterImagePolicy
metadata:
  name: topograph-release
spec:
  images:
    - glob: "ghcr.io/dsx-ai-factory/topograph**"
  authorities:
    - name: topograph-docker-workflow
      signatureFormat: bundle
      keyless:
        url: https://fulcio.sigstore.dev
        identities:
          - issuer: https://token.actions.githubusercontent.com
            subjectRegExp: '^https://github\.com/dsx-ai-factory/topograph/\.github/workflows/docker\.yml@refs/(tags/v[0-9]+\.[0-9]+\.[0-9]+|heads/release-[0-9]+\.[0-9]+)$'
      ctlog:
        url: https://rekor.sigstore.dev
```

The policy applies to namespaces labeled `policy.sigstore.dev/include=true`. It uses a regular
expression rather than an exact identity because admission has no single release to pin: any
Topograph release or release candidate is acceptable there, but nothing signed by another
workflow, branch, or repository is. The Docker workflow verifies its own output against the same
expression. It checks the signature on the index, which is the digest a pod that references the
image resolves to. The SBOM and OpenVEX attestations hang on the platform manifests, so check them
with the cosign commands above rather than at admission.

As with the provenance above, treat a failed verification, a missing signature or attestation on
an image published after this was added, or an identity other than the ones above as a potential
supply chain problem, and report it through the channel described above.

Topograph does not publish a GPG-signed release manifest. The full release and verification
procedure is documented in [RELEASE.md](./RELEASE.md).

## NVIDIA Product Security

For all security-related concerns, please visit NVIDIA's Product Security portal at https://www.nvidia.com/en-us/security
