---
name: topograph-managing-openvex
description: |
  Triage findings from Topograph's Image Vulnerability Scan workflow
  (`.github/workflows/vuln-scan-images.yaml`) and maintain `.openvex.json`,
  the repository's OpenVEX v0.2.0 document. Use when asked to suppress or
  ignore a CVE or GHSA, mark a finding not affected, write, edit, or remove a
  VEX statement, review Grype code scanning alerts (categories
  `grype-linux-amd64` and `grype-linux-arm64`), audit `.openvex.json` for
  stale statements, decide whether something belongs in `.openvex.json` or
  `.grype.yaml`, or work out why a suppression did not take effect. Triggers
  on "VEX", "OpenVEX", ".openvex.json", ".grype.yaml", "Grype", "suppress
  CVE", "not affected", "image vulnerability scan", "code scanning alert".
  Reads `vex-state.yaml` beside this skill first and updates it last.
user-invocable: true
---

# Managing `.openvex.json`

Operating instructions for triaging container image vulnerability findings and
maintaining the repository's OpenVEX document.

## Security policy comes first

`SECURITY.md` routes suspected vulnerabilities in Topograph to NVIDIA PSIRT,
never to a GitHub issue or pull request. For this skill that means:

- **Raw scanner output is not what needs protecting.** Grype's findings also
  appear in the workflow's public job log, as they do in NVIDIA/aicr and
  NVIDIA/nodewright: they are published third-party CVEs in a public image,
  which anyone can reproduce by scanning it. What must stay private is triage,
  meaning any conclusion about whether Topograph is actually affected (next
  bullets).
- **Still keep scan results out of issues, PR descriptions, commit messages,
  and code comments.** Copying a finding into prose is one sentence away from
  stating a conclusion about it. Findings belong in Security > Code scanning,
  and a revision's PR description says what changed, not what the scan found.
- **A `not_affected` statement about a published third-party CVE is fine to
  commit.** That is what `.openvex.json` is for, and its `impact_statement`
  is public by design.
- **If triage concludes Topograph IS affected, or cannot rule it out** (the
  vulnerable code is reachable, and attacker-influenced input or the
  vulnerable configuration may reach it; Step 4), stop. Do not write an `affected`
  statement, a `vex-state.yaml` entry, an issue, or a PR description that says
  so. Surface it privately to a maintainer, who routes it through PSIRT. A
  plain dependency bump can still land as a routine bump; explaining why it
  is urgent is the part that must stay private.

## Files this skill owns

| Path | Role |
| --- | --- |
| `.openvex.json` | The OpenVEX v0.2.0 document and the **only** "not affected" mechanism. Starts with `statements: []`. |
| `.grype.yaml` | Scanner configuration. Its `ignore:` list is for accepted risk only, never for "not affected" (see Step 2). Starts empty. |
| `.github/workflows/vuln-scan-images.yaml` | Builds the image from source in the job (nothing pushed, no registry credentials) for `linux/amd64` and `linux/arm64`, scans each with Grype, and uploads SARIF under categories `grype-linux-amd64` and `grype-linux-arm64`. Non-blocking: `fail-build: false`. Its `validate-inputs` job rejects a malformed `.openvex.json` or `.grype.yaml`. |
| `.claude/skills/topograph-managing-openvex/vex-state.yaml` | Carried state between sessions: deferred checks, known negatives, deliberate exclusions. |

The workflow runs weekly (Wednesdays 06:00 UTC, on `main`), on
`workflow_dispatch`, and on pushes to `main` or `pull-request/N` that change
`Dockerfile`, `go.mod`, `go.sum`, `.openvex.json`, `.grype.yaml`, or the
workflow itself.

Publishing `.openvex.json` as a signed, per-platform attestation on released
images is future work in dsx-ai-factory/topograph#595. Nothing consumers can
fetch carries this document yet, but write every statement as if it will be
signed and published, because it will be.

## Step 1 (always first): read `vex-state.yaml`

Read `.claude/skills/topograph-managing-openvex/vex-state.yaml` before doing
anything else, and act on it:

1. Run each `deferred_verifications` check. Delete the entry if it passed. If
   it failed, that failure is the finding you are now triaging.
2. Before evaluating a bump or a statement, check `known_negatives`. If the
   work is recorded as already done and its `invalidated_by` has not fired, do
   not redo it.
3. Before adding or deleting a statement, check `deliberate_exclusions`. A
   recorded decision stays in force until its `invalidated_by` condition fires.

## Step 2: suppress in `.openvex.json`, never in the Security tab

Do **not** dismiss a code scanning alert to make a finding go away. Grype
applies `.openvex.json` at scan time through the workflow's `vex:` input, so a
suppressed finding never becomes an alert. A UI dismissal is invisible to the
document, so the two drift apart, and once #595 ships the signed document is
what consumers read.

`.grype.yaml`'s `ignore:` list is not the alternative. It is for the opposite
case: a finding that **does** affect Topograph and is tolerated for now, for
example because no fixed version exists upstream. Its header lists what every
entry must record. Because the workflow scans with `only-fixed`, a finding with
no available fix is not reported there at all, so an `ignore` entry is rarely
needed. Remember the security policy above before writing one.

## Step 3: prefer a fix to a statement

The workflow builds the image from the commit it runs on, so a finding is
present in what that commit builds: `main` for scheduled runs, the pushed branch
otherwise. Published images can lag `main`, but this scan never looks at them.

VEX is for findings that cannot be fixed by an available upgrade. If a fixed
version is reachable, take it instead. Where the fix comes from depends on the
artifact type (`.matches[].artifact.type` in Grype's JSON) and its location:

- **Go module** (`go-module`, located in one of the binaries in
  `/usr/local/bin`: `topograph`, `node-observer`, `node-data-broker`,
  `kwok-nodes`, the Makefile's `TARGETS`). The version comes from `go.mod`.
  Check what the build uses with `go list -m <module>`, look for an open
  Dependabot PR (`gh pr list -R dsx-ai-factory/topograph --search <module>`),
  and bump with `go get <module>@<fixed> && go mod tidy`.
- **Go standard library** (artifact name `stdlib`). Fixed by a Go toolchain
  bump: the `golang` builder image in `Dockerfile`, `go.mod`, the
  `go-version` pins in `.github/workflows/go.yml` and `k8s-test.yaml`, and
  `DEVELOPMENT.md`. A previous `upgrade Go to` commit shows the full set.
- **Alpine package** (`apk`). The runtime stage is the digest-pinned `alpine`
  image plus `apk add --no-cache rdma-core`. Packages that `apk add` pulls in
  resolve at build time, so a rebuild (the weekly run is one) picks up a fix
  as soon as Alpine publishes it. Packages already in the base image generally
  stay at the pinned digest's versions; the fix is a base digest bump, which
  Dependabot's docker ecosystem proposes. To check, scan the newest
  `alpine:3.24` and see whether the finding clears.

If a fix is available, stop here and take it. No statement.

## Step 4: gather reachability evidence

A `not_affected` statement is a claim about Topograph's code, so it needs
evidence a reviewer can rerun.

**Go modules and stdlib: use govulncheck.** `.github/workflows/go.yml` already
runs it on every push, and it analyses at the symbol level, which is exactly the
reachability question. Use the same version CI pins:

```bash
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 -show verbose ./...
```

Its verbose output separates three cases:

| govulncheck says | Next step |
| --- | --- |
| Module required, vulnerable package never imported | `not_affected`, justification `vulnerable_code_not_present` |
| Package imported, vulnerable symbol never called | `not_affected`, justification `vulnerable_code_not_in_execute_path` |
| Vulnerable symbol called | No verdict yet. Assess the input and the configuration first (below). |

A call that is reachable is not yet a vulnerability: the advisory's trigger has
to hold too. That usually means attacker-influenced input reaching the
vulnerable symbol, or Topograph running with the configuration the advisory
requires. Print the call paths and follow each one back to where its input
comes from:

```bash
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 -show traces ./...
```

Then decide, conservatively:

- **Attacker-influenced input or the vulnerable configuration can reach it, or
  you cannot rule that out:** treat it as potentially affected. Stop and route
  it privately (security policy, above). No statement of any kind.
- **Every path's input is demonstrably outside an attacker's control** (for
  example, it is only ever a constant, or the operator's own configuration
  file): a `not_affected` candidate with justification
  `vulnerable_code_cannot_be_controlled_by_adversary`. The `impact_statement`
  names each call path, where its input comes from, and why an attacker cannot
  influence it.
- **The advisory needs a configuration Topograph never uses** (a feature,
  mode, or option it never enables): a `not_affected` candidate. Use
  `vulnerable_code_not_in_execute_path` when that configuration is what keeps
  the code from running, or `inline_mitigations_already_exist` when a control
  in Topograph blocks the trigger. Cite the code that fixes the configuration.

"Probably not exploitable" is the first case, not the second or third.

To check the shipped binaries rather than the source tree, build them and use
binary mode on each of the four:

```bash
make build-linux-amd64
for b in topograph node-observer node-data-broker kwok-nodes; do
  go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 -mode=binary "bin/$b"
done
```

govulncheck reports Go vulnerability database IDs (`GO-YYYY-NNNN`) with CVE and
GHSA aliases. The statement must use Grype's ID instead (Step 5).

**Alpine packages: say which executable loads the code.** The Go binaries are
built with `CGO_ENABLED=0` and statically linked, so they never load an Alpine
shared library. A vulnerable library only matters through an executable that
links it. Topograph does run external tools. The InfiniBand provider runs
`ibnetdiscover` two ways: on Kubernetes, inside the node-data-broker pod
through `internal/k8s.ExecInPod` (`pkg/providers/infiniband/k8s.go`), which
runs whatever the pod's image provides; on bare metal, as `sudo ibnetdiscover`
on the target hosts through `internal/exec.Pdsh`
(`pkg/providers/infiniband/bm.go`), which runs the host's copy, not the
image's. Name the executables that link the library, say whether Topograph
ever runs them from this image, and say with what input.

Every `impact_statement` cites something concrete: the govulncheck result, a
`grep` that returns nothing (with the pattern), a file path, or upstream
advisory text limiting the trigger to a configuration Topograph does not use.
"Not exploitable" or "low risk" is not evidence.

## Step 5: get the vulnerability ID right

`vulnerability.name` must equal Grype's **primary** ID,
`.matches[].vulnerability.id`. For Go advisories that is often the **GHSA**,
with the CVE only under `relatedVulnerabilities`. Matching is by exact string,
so a statement naming the CVE will not match a GHSA-primary finding. It looks
correct and suppresses nothing.

```bash
jq -r '.matches[]
  | select(.vulnerability.severity | ascii_downcase | IN("high","critical"))
  | "\(.vulnerability.id)\t\(.artifact.name)@\(.artifact.version)\t\(.artifact.purl)\taliases=\([.relatedVulnerabilities[]?.id] | join(","))"' scan.json
```

The first column goes in `vulnerability.name`.

**`aliases` is also a match key.** Adding the CVE beside a GHSA widens the
statement to any other finding in the image whose primary ID is that CVE, in a
package you never assessed. Alpine findings are CVE-primary while Go findings
are usually GHSA-primary, so this is live in this image. List an alias only
when you mean the statement to cover it.

## Step 6: get the product and subcomponents right

**Product.** Grype derives the image's product PURL from the repository name of
the scanned image: the workflow exports the build as `topograph:scan`, which
gives `pkg:oci/topograph`. Use that bare value, with no tag, digest, or
qualifiers, in both `@id` and `identifiers.purl`. It has been measured: on a
`docker-archive` scan like CI's, a statement with this product suppressed its
finding and the same statement with a different product did not. Renaming the
image in the workflow's build step would silently disable every statement.

**Subcomponents are required, with a version.** A product with no
`subcomponents` asserts that the whole image is unaffected, which is never what
the assessment covered. The `validate-inputs` job rejects a product without
subcomponents, and a subcomponent purl that has no version or carries
qualifiers.

The version makes a statement expire by itself: once the package is upgraded,
the versioned purl stops matching, the finding is re-reported, and someone
reassesses it. A version-less purl would keep suppressing whatever ships.

**Drop purl qualifiers.** Take the purl from Grype's output (`.artifact.purl`),
then delete everything from `?` on. Alpine purls carry `?arch=...&distro=...`,
and measured against the pinned Grype, a statement whose subcomponent says
`arch=aarch64` suppresses only the arm64 scan, never amd64. Without qualifiers,
one statement covers both platforms. Keep the version: a purl with a different
version does not match. The version sits before the qualifiers, so
`pkg:apk/alpine/<name>@<version>?arch=...` becomes
`pkg:apk/alpine/<name>@<version>`.

If the package version differs between the two platforms, list one subcomponent
per version.

## Step 7: write the statement to the v0.2.0 contract

```json
{
  "vulnerability": {
    "name": "GHSA-xxxx-xxxx-xxxx"
  },
  "products": [
    {
      "@id": "pkg:oci/topograph",
      "identifiers": { "purl": "pkg:oci/topograph" },
      "subcomponents": [
        { "@id": "pkg:golang/example.com/module@v1.2.3" }
      ]
    }
  ],
  "status": "not_affected",
  "justification": "vulnerable_code_not_in_execute_path",
  "impact_statement": "The specific evidence from Step 4."
}
```

- `status` is one of `not_affected`, `affected`, `fixed`,
  `under_investigation`. A typo such as `not-affected` makes Grype suppress
  nothing with no warning; the validator catches it, but measure anyway.
- `not_affected` needs a `justification` or an `impact_statement`. In this
  repository, always give both.
- `justification` is one of exactly `component_not_present`,
  `vulnerable_code_not_present`, `vulnerable_code_not_in_execute_path`,
  `vulnerable_code_cannot_be_controlled_by_adversary`,
  `inline_mitigations_already_exist`. If none is honestly true, do not pick the
  nearest-sounding one; that is how a signed document ends up asserting
  something false.
- `affected` needs an `action_statement`, but see the security policy before
  writing one.

On every change to the document, bump `version` (integer, monotonic) and set
`timestamp` to the current UTC time in RFC 3339. Leave `@id`, `author`,
`role`, and `tooling` alone.

## Step 8: verify by measuring, never by reading

A wrong ID, a wrong product, a qualifier for one platform, or a status typo all
produce a document that reads correctly and suppresses nothing. The only check
is to scan the same images with the document as it was before your edit and as
it is after, and compare what each one suppressed. Comparing against a scan
with no document at all would also count every older statement, so the
difference would not isolate your change.

Build both platforms the way CI does, named `topograph` so the product matches.
CI builds each platform natively on its own runner. Locally, building the
non-native platform needs buildx with QEMU (binfmt); the legacy builder cannot
build for a platform other than the host's:

```bash
for arch in amd64 arm64; do
  docker build --platform "linux/$arch" \
    --build-arg TARGETOS=linux --build-arg TARGETARCH="$arch" \
    --tag "topograph:local-$arch" .
  docker save "topograph:local-$arch" -o "topograph-$arch.tar"
done

# Use the grype-version pinned in the workflow's scan step.
grype --version

# The document before your edit: the PR base. Before you have committed the
# edit, `git show HEAD:.openvex.json` is the same thing.
git show "$(git merge-base HEAD origin/main)":.openvex.json > openvex-base.json

scan() {  # scan <arch> <vex document> <output>
  grype "docker-archive:topograph-$1.tar" -c .grype.yaml --only-fixed \
    --vex "$2" -o json > "$3"
}
for arch in amd64 arm64; do
  scan "$arch" openvex-base.json "before-$arch.json"
  scan "$arch" .openvex.json "after-$arch.json"
done

# Rows each document suppressed, as "<id> <purl without qualifiers>".
VEXED='[.ignoredMatches[]?
        | select((.appliedIgnoreRules // []) | any(.namespace == "vex"))
        | "\(.vulnerability.id) \(.artifact.purl | sub("[?#].*$"; ""))"]
       | unique[]'
for arch in amd64 arm64; do
  echo "== $arch"
  diff <(jq -r "$VEXED" "before-$arch.json") <(jq -r "$VEXED" "after-$arch.json")
done

# What the edited statement must do on each platform. Several statements can
# share an ID, so select the edited one by its zero-based index in
# `statements`. List them to find it:
jq -r '.statements | to_entries[]
  | "\(.key) \(.value.vulnerability.name) \(.value.status)"' .openvex.json
IDX='<index of the edited statement>'
ID=$(jq -r --argjson idx "$IDX" '.statements[$idx].vulnerability.name' .openvex.json)
jq -r --argjson idx "$IDX" '.statements[$idx]
  | .products[].subcomponents[]
  | (.["@id"] // .identifiers.purl)' .openvex.json | sort -u > scoped.txt
# A statement may list one subcomponent per platform-specific version
# (Step 6), so the expected rows come from what each platform's base scan
# still reported for this ID, not from the statement alone.
for arch in amd64 arm64; do
  echo "== $arch"
  # Subcomponents present on this platform: reported for this ID before the edit.
  jq -r --arg id "$ID" '.matches[] | select(.vulnerability.id == $id)
    | .artifact.purl | sub("[?#].*$"; "")' "before-$arch.json" \
    | sort -u | comm -12 scoped.txt - > "expected-$arch.txt"
  # Rows of this ID that the edit newly suppressed.
  comm -13 <(jq -r "$VEXED" "before-$arch.json") <(jq -r "$VEXED" "after-$arch.json") \
    | awk -v id="$ID" '$1 == id { print $2 }' | sort -u > "added-$arch.txt"
  echo "expected:";                  sed 's/^/  /' "expected-$arch.txt"
  echo "missing (must be empty):";    comm -23 "expected-$arch.txt" "added-$arch.txt"
  echo "unexpected (must be empty):"; comm -13 "expected-$arch.txt" "added-$arch.txt"
  echo "still reported (must be empty):"
  jq -r --arg id "$ID" '.matches[] | select(.vulnerability.id == $id)
    | .artifact.purl | sub("[?#].*$"; "")' "after-$arch.json" \
    | sort -u | comm -12 "expected-$arch.txt" -
done
```

Read the result, per platform:

- `diff` prints a `>` line for every row the edit newly suppresses and a `<`
  line for every row it stopped suppressing. Every `>` line should carry the new
  statement's ID; the second loop checks which ones. A `<` line is expected
  only for a statement you deliberately removed or whose subcomponent version
  you changed; anything else means the edit broke an existing statement.
- `expected` lists the statement's subcomponents that this platform's image
  actually contains and still reported for the ID before the edit. It can
  differ between platforms: a statement with one subcomponent per version
  expects only the matching version on each platform, and nothing for a
  version that platform does not ship. One statement can also cover several
  rows, for example the same CVE in two Alpine packages.
- `missing`, `unexpected`, and `still reported` must all be empty on both
  platforms. The checks cover only the packages the selected statement names:
  the same ID in a package it does not name stays in `.matches[]` on purpose,
  as a finding to triage on its own, and another statement with the same ID is
  checked by running the block again with that statement's index. If one edit
  adds two statements with the same ID, each one's rows appear under the
  other's `unexpected`; confirm them with the other index.

If `expected` is empty on both platforms, the statement names no package and
version either scan reports for this ID: suspect the subcomponent version, then
the ID. If `missing` is not empty, the statement did not apply: suspect the ID,
then the subcomponent purl, then the product. Do not merge a statement until
every platform's checks come back empty.

`GRYPE_DB_VALIDATE_AGE=false` lets Grype run against an old local database
when its update host is unreachable, for example in a sandbox. It is local only
and must never appear in the workflow: in CI a stale database would silently
under-report.

## Step 9: stale audit on every edit

Statements stop applying silently: a package is upgraded (the versioned
subcomponent no longer matches), an advisory is withdrawn, a component leaves
the image. Every edit to `.openvex.json` includes this audit. It works per
statement and per subcomponent, not per ID, because two statements can share
an ID (one live, one stale), and it reads the VEX-applied rows of both platform
scans from Step 8 before calling anything stale:

```bash
jq -r -n \
  --slurpfile amd64 after-amd64.json \
  --slurpfile arm64 after-arm64.json \
  --slurpfile vex .openvex.json '
  # "<id> <purl without qualifiers>" for every row a VEX statement suppressed,
  # keyed by the primary ID and by each related ID.
  def applied($scan):
    [$scan[0].ignoredMatches[]?
     | select((.appliedIgnoreRules // []) | any(.namespace == "vex"))
     | (.artifact.purl | sub("[?#].*$"; "")) as $purl
     | (.vulnerability.id, .relatedVulnerabilities[]?.id)
     | "\(.) \($purl)"]
    | unique;
  applied($amd64) as $a | applied($arm64) as $b
  | $vex[0].statements | to_entries[]
  | .key as $i | .value.vulnerability.name as $id
  | .value.products[].subcomponents[]
  | (.["@id"] // .identifiers.purl) as $purl
  | "\($id) \($purl)" as $k
  | [ (if any($a[]; . == $k) then "amd64" else empty end),
      (if any($b[]; . == $k) then "arm64" else empty end) ] as $on
  | "\(if $on == [] then "STALE" else "live" end)\tstatement \($i)\t\($id)\t\($purl)\t\($on | join(","))"
'
```

Each output line is one subcomponent of one statement, with the platforms on
which it suppressed a row. A `live` line on only one platform is expected when
the package exists on only that platform. Classify every `STALE` line, using
both platforms' scans:

1. **Gone** (no hit for the ID or its aliases anywhere in either scan): the
   package moved past the fix or the advisory was withdrawn. Delete the
   subcomponent, and the statement once it has none left.
2. **Present under a newer version** (in `.matches[]` again): the versioned
   subcomponent expired as designed. Reassess from Step 3; do not just bump the
   version in the purl.
3. **Present but hidden by `--only-fixed`** (in `.ignoredMatches[]` with no
   `vex` rule): the subcomponent applies to nothing today. Delete it; if a fix
   appears, the finding should resurface and be fixed, not pre-suppressed.
4. **Present under a different primary ID**: not stale, a name mismatch. Fix
   `vulnerability.name` (Step 5).

Deleting a stale subcomponent or statement must leave every `live` line
unchanged.

## Step 10: keep document-level fields as identifiers

`@id`, `author`, `role`, `timestamp`, `version`, and `tooling` identify the
document. They are not prose. A sibling NVIDIA project let `tooling` grow into
an 8,010-byte changelog that then shipped, signed, on seven release images
(NVIDIA/aicr#2706). Ours reads
`manual curation (.claude/skills/topograph-managing-openvex)` and stays that way.

| Kind of rationale | Where it goes |
| --- | --- |
| Why one CVE does not affect Topograph, with evidence | that statement's `impact_statement` |
| What a revision changed and how it was verified | the PR description (no scan output, per the security policy) |
| A check that needs a later run, a negative result, a decision not to act | `vex-state.yaml` |
| Anything else | nowhere |

## Step 11: triage by severity, not by alert count

The uploaded SARIF holds **every finding with an available fix**, not only HIGH
and above. `severity-cutoff: high` sets Grype's `--fail-on high`: while
`fail-build` is false, a HIGH or CRITICAL finding adds a warning annotation to
the run instead of failing it, and `fail-build: true` would make the same
threshold a gate. It does not filter the upload, and each SARIF result is
graded by the finding's own severity. Grype sets a `security-severity` on each rule, so the Security tab can
filter by severity. **Triage HIGH and CRITICAL.** Low and Medium findings do not
need a statement; writing one per alert is how the document stops being
reviewable.

```bash
gh api -X GET repos/dsx-ai-factory/topograph/code-scanning/alerts \
  -f state=open -f tool_name=Grype --paginate \
  | jq -r '.[]
      | select(.rule.security_severity_level | IN("high","critical"))
      | "\(.most_recent_instance.category)\t\(.rule.security_severity_level)\t\(.rule.id)"' \
  | sort | uniq -c
```

Keep that output out of anything public (security policy, above). If the tool
name filter returns nothing, list one alert and read its `tool.name`.

## Step 12 (always last): update `vex-state.yaml`

Before finishing, write back to
`.claude/skills/topograph-managing-openvex/vex-state.yaml`:

- **Delete** each `deferred_verifications` entry whose check has run and
  passed.
- **Delete** each `known_negatives` or `deliberate_exclusions` entry whose
  `invalidated_by` condition has fired. Do not rewrite it as history.
- **Add** an entry for anything this session could not finish, already ruled
  out, or deliberately chose not to do, for example "statement merged; confirm
  the next CI run shows it applied on both platforms".

Every entry states what would invalidate it. The file's header has the full
rules. Never record an affected finding there (security policy, above).

## Quick reference

Run the scan on demand instead of waiting for Wednesday:

```bash
gh workflow run "Image Vulnerability Scan" --repo dsx-ai-factory/topograph --ref main
gh run list --workflow "Image Vulnerability Scan" --repo dsx-ai-factory/topograph --limit 3
```

- Workflow: `.github/workflows/vuln-scan-images.yaml` (Grype version pinned by
  the scan step's `grype-version`)
- VEX document: `.openvex.json`; accepted risk: `.grype.yaml`
- Image source: `Dockerfile` (golang builder, then `alpine` plus `rdma-core`)
- Go reachability: govulncheck, as run by `.github/workflows/go.yml`
- Carried state: `.claude/skills/topograph-managing-openvex/vex-state.yaml`
- Future publication as a signed attestation: dsx-ai-factory/topograph#595
