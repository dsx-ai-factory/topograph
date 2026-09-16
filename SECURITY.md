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

While NVIDIA currently does not have a bug bounty program, we do offer acknowledgement when an externally reported security issue is addressed under our coordinated vulnerability disclosure policy. Please visit our [Product Security Incident Response Team (PSIRT)](https://www.nvidia.com/en-us/security/psirt-policies/) policies page for more information.

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

Acknowledgement for an externally reported issue is offered under that same policy, as stated
above. Reporting privately and observing the embargo is what keeps that acknowledgement, and the
coordinated fix, possible.

## Verifying Release Artifacts

This section applies to releases published after `v1.0.0`, the first ones cut by the release
workflow that generates build provenance and a checksum file. `v1.0.0` and every earlier release
predate that workflow: they publish the container image and the chart package only, with no
attestation for either, no checksum file, and no assets on their GitHub release pages. That is
expected for those releases, not a supply chain problem.

An official release publishes a multi-architecture container image at
`ghcr.io/nvidia/topograph:vX.Y.Z`, a Helm chart package in the chart repository at
`https://nvidia.github.io/topograph`, and a SHA-256 checksum file beside the chart package. The
chart package and its checksum are also attached to the GitHub release.

The image and the chart package each carry SLSA build provenance produced by GitHub artifact
attestations, which are signed through Sigstore. Verify what you downloaded before installing it,
substituting the release you are checking for `vX.Y.Z` and `X.Y.Z`.

Container image:

```bash
gh attestation verify oci://ghcr.io/nvidia/topograph:vX.Y.Z \
  --repo NVIDIA/topograph \
  --signer-workflow NVIDIA/topograph/.github/workflows/docker.yml \
  --source-ref refs/tags/vX.Y.Z
```

Helm chart package and its checksum:

```bash
curl -fsSLO https://nvidia.github.io/topograph/topograph-X.Y.Z.tgz
curl -fsSLO https://nvidia.github.io/topograph/topograph-X.Y.Z.tgz.sha256
sha256sum --check topograph-X.Y.Z.tgz.sha256
gh attestation verify topograph-X.Y.Z.tgz \
  --repo NVIDIA/topograph \
  --signer-workflow NVIDIA/topograph/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z
```

For a release covered by this section, treat a failed verification, a missing attestation, or an
attestation naming a workflow or source reference other than the ones above as a potential supply
chain problem, and report it through the channel described above. A release published before that
point carries no attestation by design, so its absence is not something to report.

Topograph does not currently publish a detached cosign signature or a GPG-signed release manifest.
The Sigstore-backed provenance attestations and the chart checksum are the verification path
today. The full release and verification procedure is documented in [RELEASE.md](./RELEASE.md).

## NVIDIA Product Security

For all security-related concerns, please visit NVIDIA's Product Security portal at https://www.nvidia.com/en-us/security
