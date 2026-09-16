# Contribute to the NVIDIA `topograph` Project

Want to contribute to the NVIDIA `topograph` project? Awesome!
We only require you to sign your work as described in the following section.

For build, test, lint, and local-run commands, see the
[Development Guide](DEVELOPMENT.md).

Participation in this project is governed by our
[Code of Conduct](CODE_OF_CONDUCT.md). The
[Community standards](#community-standards) section below covers how to report
a concern, how quickly you can expect an answer, and what the process does not
cover.

## Open an issue first

Before opening a pull request, open an issue — this applies to bug fixes,
features, and any other change:

- **Bugs**: file a [bug report](https://github.com/dsx-ai-factory/topograph/issues/new?template=bug_report.yml)
  describing what happened and what you expected instead.
- **Features or enhancements**: file a [feature request](https://github.com/dsx-ai-factory/topograph/issues/new?template=feature_request.yml)
  describing the problem and your proposed solution.
- Search [existing issues](https://github.com/dsx-ai-factory/topograph/issues) first to
  avoid duplicates, and comment on the issue to claim it before starting work.

Opening the issue first gives maintainers a chance to weigh in on approach
before code is written, and keeps the change from crossing one of this
project's load-bearing boundaries by accident:

- Changes to `pkg/topology/` (`Graph`, the `Vertex` tree, topology constants)
  must be discussed in an issue first — every provider and engine depends on
  this shape.
- Adding a new engine implies a new output format that every provider's
  output must be translatable into — coordinate with maintainers before
  starting.

Trivial fixes (typos, broken links, small doc corrections) don't need an
issue first — a pull request is fine.

## Issue priority

Topograph doesn't run a formal `P0`/`P1`/`P2` label system or an automated
triage bot. Priority is set informally, from a few concrete inputs:

- **Your own stated priority.** The [feature request template](https://github.com/dsx-ai-factory/topograph/issues/new?template=feature_request.yml)
  asks you to pick "Nice to have", "Important (would improve my
  workflow)", or "Critical (blocking adoption or major use case)" — this
  is read during triage, so pick the one that actually reflects your
  situation rather than defaulting to "Critical."
- **Maintainer triage.** Per [GOVERNANCE.md](GOVERNANCE.md#roles-and-responsibilities),
  any Contributor, Reviewer, or Maintainer can triage an issue (label it,
  reproduce it, link duplicates); only a Maintainer decides whether
  something is worth working on next. There's no separate "triage
  meeting" — it happens asynchronously on the issue itself.
- **The Roadmap issue.** The pinned **Roadmap & Focus Areas** issue on the
  [issue tracker](https://github.com/dsx-ai-factory/topograph/issues) is the
  closest thing to a prioritized backlog — it lists the areas maintainers
  are actively steering the project toward. An issue that maps onto one of
  those areas is more likely to get picked up sooner than one that
  doesn't.
- **Load-bearing surfaces get attention regardless of stated priority.**
  Anything affecting `pkg/topology/`, the label/annotation contract in
  [`docs/reference/node-labels.md`](docs/reference/node-labels.md), or a
  provider/engine boundary violation tends to get maintainer eyes quickly
  because those changes ripple across every provider and engine — see
  [AGENTS.md § "Do not change without discussion"](AGENTS.md#do-not-change-without-discussion).

### How to influence it

- Say *why* it matters in the issue body — a concrete failure mode, a
  blocked use case, or a real deployment you're running, not just "this
  would be nice." Real usage examples move issues up faster than
  abstract requests.
- Comment (don't just react) if an existing issue affects you too, and say
  how. Duplicate-sounding comments with no new information don't change
  priority; a new failure mode, cluster size, or scheduler combination
  does.
- Offer to open the PR. Commenting that you're willing to implement a
  feature or fix — and then [claiming the issue](#open-an-issue-first) —
  is one of the strongest signals for priority, since it removes
  maintainer bandwidth as the blocker.
- If you believe an issue is being missed rather than deprioritized,
  follow the same [follow-up steps](#following-up) used for a stalled PR —
  a comment, then the community Slack channels, referencing the specific
  issue number.

## Adding a new provider or engine

Topograph has two backend extension points: **providers** (topology
sources — CSP APIs, NetQ, `ibnetdiscover`, DRA labels, ...) and **engines**
(scheduler output formats — Slurm, Kubernetes, NFD, Slinky). The invariant
that keeps them decoupled: providers discover, engines only translate. If
you find yourself reading the fabric inside an engine, or emitting
scheduler-specific output from a provider, stop and reconsider the design.

### Adding a provider

1. Create `pkg/providers/<name>/` with at minimum `provider.go` and
   `provider_test.go` — `pkg/providers/test/` is a minimal reference
   implementation to copy from.
2. Implement the `Provider` interface (`pkg/providers/providers.go`):
   return a `*topology.Graph` and a `*httperr.Error` (never a plain
   `error` — the API server needs it to propagate the right HTTP status).
3. Expose `func NamedLoader() (string, providers.Loader)` and register it
   in `pkg/registry/registry.go`; an unregistered provider is orphaned code
   that never loads.
4. If network-fabric and accelerator-domain discovery are separable
   concerns, compose them independently through `pkg/accelerator` instead
   of hand-rolling detection logic inside the provider.
5. Document it: add `docs/providers/<name>.md` (follow the shape of
   `aws.md` / `netq.md`), and add the provider to `docs/overview.md`'s
   supported-provider list and "Choosing a Provider" scenario table.
6. Optional: export a second `NamedLoaderSim` for a simulated/testable
   variant (see `aws`, `gcp`, `oci`, `lambdai`).

The full checklist and the `Provider` interface contract are the source of
truth in [AGENTS.md § Adding a new provider](AGENTS.md#adding-a-new-provider).

### Adding an engine

Engines are rare — only five exist today (`slurm`, `k8s`, `nfd`, `slinky`,
`graph`) — because adding one implies every provider's output must become
translatable into it. **Open an issue and get maintainer agreement before
writing code** (see "Open an issue first" above); this is one of the two
load-bearing boundaries in the project. Once agreed, follow the same
registry pattern, registered in `engines.NewRegistry(...)`. Full detail in
[AGENTS.md § Adding a new engine](AGENTS.md#adding-a-new-engine).

### Ground rules for both

- New provider or engine docs are a requirement of the PR, not optional
  follow-up — see the
  [Documentation Impact Evaluation table](AGENTS.md#documentation-impact-evaluation).
- Review the [anti-patterns table](AGENTS.md#anti-patterns) in AGENTS.md
  before opening the PR; most rejected provider/engine PRs trip one of
  those rows.

## Sign your work

The sign-off is a simple signature at the end of the description for the patch.
Your signature certifies that you wrote the patch or otherwise have the right
to pass it on as an open-source patch.

The rules are pretty simple, and sign-off means that you certify the DCO below
(from [developercertificate.org](http://developercertificate.org/)):

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.
1 Letterman Drive
Suite D4700
San Francisco, CA, 94129

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.

Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

To sign off, you just add the following line to every git commit message:

    Signed-off-by: Joe Smith <joe.smith@email.com>

You must use your real name (sorry, no pseudonyms or anonymous contributions).

If you set your `user.name` and `user.email` using git config, you can sign
your commit automatically with `git commit -s`.

### DCO bot enforcement

This repo runs the [`probot/dco`](https://probot.github.io/apps/dco/) GitHub
App on every pull request — it checks every commit in the PR for a
`Signed-off-by:` trailer and reports a `DCO` check in the PR's status
checks. There is no `.github/dco.yml` exemption configured: `dsx-ai-factory` org
membership does not bypass it, and neither does any other affiliation.

**If the `DCO` check fails**, rebase the branch to add sign-off to every
commit rather than opening a new PR. The [Development Guide](DEVELOPMENT.md#clone-and-build)'s
clone flow names the remote `origin`; if you're working from a fork with
`origin` pointing at your fork, substitute whatever remote tracks
`dsx-ai-factory/topograph`'s `main` (commonly added as `upstream`) instead:

```bash
# add sign-off to every commit on the branch since it diverged from main
git rebase --signoff origin/main
git push --force-with-lease
```

For just the most recent commit:

```bash
git commit --amend -s --no-edit
git push --force-with-lease
```

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```text
type(scope): short description

optional body

Signed-off-by: Your Name <you@example.com>
```

`type` must be one of: `feat`, `fix`, `docs`, `chore`, `refactor`, `style`,
`perf`, `test`, `build`, `ci`. `scope` is optional and usually names the
package or area touched (e.g. `feat(provider/crusoe): ...`,
`fix(engine/k8s): ...`, `docs(contributing): ...`). Use the imperative mood
("add support for X", not "added support for X") and keep the summary line
under about 70 characters — it's what shows up in `git log --oneline` and in
the PR list.

Examples:

```gitcommit
feat(provider/crusoe): add Crusoe Cloud provider

fix(engine/slurm): handle empty fabric tiers in topology.conf output

docs(providers/aws): document IAM permissions required for topology API
```

Branch names use a smaller prefix set than the full commit `type` list above
— `feat/`, `fix/`, `docs/`, `chore/`, `refactor/`, `test/` — e.g.
`feat/crusoe-provider`; there's no dedicated branch prefix for `style`,
`perf`, `build`, or `ci` commits, so use whichever of the six branch
prefixes best fits the change. Every commit still needs the DCO
`Signed-off-by:` trailer described above — `git commit -s` adds it for you.

## AI-assisted contributions

We welcome the use of AI tools (Claude Code, GitHub Copilot, ChatGPT, and
similar) to help write code, brainstorm designs, or refactor. This repo
even ships agent-facing guidance for that purpose —
[`AGENTS.md`](AGENTS.md) / [`.claude/CLAUDE.md`](.claude/CLAUDE.md) — but
using it comes with a strict human-in-the-loop policy:

- **Full accountability.** By opening a PR, you (the human author) accept
  full responsibility for the code: its correctness, security,
  maintainability, and license compliance. "The AI wrote it" is not an
  acceptable explanation for a bug or a security flaw, and doesn't change
  who a maintainer holds accountable in review.
- **Understand what you submit.** Don't submit AI-generated code you don't
  fully understand. Reviewers expect you to explain and defend every line
  of your PR, including design choices an assistant made on your behalf.
- **The provider/engine boundary still applies.** An assistant with broad
  repo access can just as easily read the fabric inside an engine or emit
  scheduler-specific output from a provider as a human can — review AI-
  generated changes against the [anti-patterns table](AGENTS.md#anti-patterns)
  and the invariants in [AGENTS.md § 1](AGENTS.md#1-project-overview-and-architecture)
  before opening the PR, not after a reviewer flags it.
- **DCO sign-off is still yours to give.** Signing off certifies *you* have
  the right to submit the contribution under this project's license (see
  [Sign your work](#sign-your-work) above) — an AI tool cannot certify that
  on your behalf, regardless of how much of the diff it authored.
- **Security-sensitive findings follow the normal path.** If an assistant
  surfaces anything that looks like a potential vulnerability — whether you
  stumbled on it or were intentionally testing for it — don't paste it into
  a public issue or PR description. Report it privately per
  [`SECURITY.md`](SECURITY.md) (the web form or `psirt@nvidia.com`), the
  same as you would for anything you found yourself.

## Review process

### Who reviews

Every pull request auto-requests review from the repository's
[CODEOWNERS](CODEOWNERS). Formal approval requires a **Reviewer** or
**Maintainer** (per [GOVERNANCE.md](GOVERNANCE.md#roles-and-responsibilities));
only a **Maintainer** can merge, and merging separately verifies DCO
sign-off, passing CI, and required approvals — it is not a second code
review. The current maintainer list, including any path-scoped maintainers,
is in [MAINTAINERS.md](MAINTAINERS.md).

### What's checked

- **Automated** — Go build/test/lint/`govulncheck` (`go.yml`), Helm chart
  lint/unittest (`chart-test.yaml`), Codecov patch/project coverage, and the
  DCO bot all run on every push to the PR.
- **Human** — adherence to the provider/engine boundary, test coverage on new
  code paths, and doc updates when a contract changes (see the
  [Documentation Impact Evaluation table](AGENTS.md#documentation-impact-evaluation)
  in `AGENTS.md`).

### Turnaround

A maintainer aims to give an initial response — a review, a request for
changes, or at minimum a triage comment — within **5 business days** of a PR
being opened or re-requested for review. This project currently has a small
maintainer team (see [MAINTAINERS.md](MAINTAINERS.md)), so treat 5 business
days as the target, not a guarantee, and expect full review-to-merge to take
longer for larger or more sensitive changes (anything touching
`pkg/topology/`, a new engine, or a new provider).

### Following up

If you haven't heard anything after 5 business days:

1. Leave a comment on the PR (`@`-mention the reviewer CODEOWNERS assigned,
   or a maintainer from [MAINTAINERS.md](MAINTAINERS.md) if none was
   assigned) asking for a status update.
2. Raise it in the [community Slack channels](README.md#community) —
   [#topology-aware-scheduling](https://kubernetes.slack.com/archives/C012XSGFZQE)
   or [#gpu-nvidia](https://kubernetes.slack.com/archives/C09N46EFJR0) — if
   the PR thread goes quiet.
3. For a stalled issue rather than a PR, check the pinned **Roadmap & Focus
   Areas** issue on the [issue tracker](https://github.com/dsx-ai-factory/topograph/issues)
   to see if it's already being tracked there.

Keep the PR in draft while you're still reshaping it — draft PRs don't page
reviewers, so that's the cheapest phase to rework history or force-push in.
Once a PR is open for review, prefer pushing new commits over amending or
rebasing, since a force-push invalidates existing inline review comments.

## Community

Community discussion happens on the [Kubernetes Slack](https://slack.k8s.io/):

- [#topology-aware-scheduling](https://kubernetes.slack.com/archives/C012XSGFZQE) — topology-aware scheduling across the ecosystem
- [#gpu-nvidia](https://kubernetes.slack.com/archives/C09N46EFJR0) — NVIDIA GPU support on Kubernetes

For the project's current direction and a list of areas where contributions are especially welcome, see the pinned **Roadmap & Focus Areas** issue on the [issue tracker](https://github.com/dsx-ai-factory/topograph/issues).

## Community standards

Everyone taking part in this project is expected to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). It applies in the Slack channels above,
in issues and pull requests, in review comments, and anywhere else someone is
representing Topograph, and it binds maintainers exactly as it binds a
first-time contributor.

### Reporting a concern

Report behavior that violates the Code of Conduct by emailing
GitHub_Conduct@nvidia.com, the contact named in
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md#enforcement). A useful report says what
happened, where it happened (link the issue, pull request, or Slack thread if
there is one), and who was involved. You don't have to be the person the
behavior was aimed at in order to report it.

That address reaches NVIDIA's open source conduct contact rather than this
repository's maintainers, so a report about a maintainer doesn't pass through
the person it concerns. The reporter's identity is held in confidence. Please
don't open a public issue about a conduct concern: doing so exposes everyone
involved before anything has been established.

### Response timeline

- **Acknowledgement within 3 business days.** You get a reply confirming the
  report arrived and naming who is handling it. That reply is a receipt, not a
  decision.
- **Resolution within 14 calendar days** of the acknowledgement for a typical
  report. Resolution means a decision has been reached, and that the reporter
  is told what it is and what action, if any, followed from it.
- **A status update every 14 days** for a report that needs longer than that,
  which happens when several people have to be contacted, when the behavior
  spans more than one project space, or when an NVIDIA legal or HR review is
  involved. Updates continue until the report is closed.

These targets reflect the size of the maintainer team (see
[MAINTAINERS.md](MAINTAINERS.md)), so read them as commitments about how
quickly you'll be kept informed rather than a guarantee of how fast an
investigation finishes. Anything involving a threat to someone's safety is
escalated immediately instead of waiting on this schedule.

### Out of scope

The Code of Conduct process does not cover the following. Sending them to
GitHub_Conduct@nvidia.com only adds delay, since they have to be redirected
before anyone can act on them:

- **Product security vulnerabilities.** Anything that looks like a
  vulnerability in Topograph or another NVIDIA product goes to NVIDIA PSIRT
  through the process in [SECURITY.md](SECURITY.md): the
  [security vulnerability submission form](https://www.nvidia.com/object/submit-security-vulnerability.html)
  or psirt@nvidia.com. Don't report it to the conduct address, in a public
  issue, or in a pull request description.
- **Technical disagreements.** A rejected design, a maintainer declining a
  feature, review feedback you disagree with, or a priority call is settled on
  the issue or pull request itself, and escalated through the
  [decision process](GOVERNANCE.md#decision-process) in `GOVERNANCE.md` rather
  than the conduct process. Which path applies depends on the decision. A pull
  request you believe was closed unfairly follows
  [PR rejection](GOVERNANCE.md#pr-rejection). A rejected design follows
  [Architectural decisions](GOVERNANCE.md#architectural-decisions). A declined
  feature or subsystem is a proposal-stage decision under
  [Significant changes](GOVERNANCE.md#significant-changes). A priority call is
  a maintainer judgment you influence through [Issue priority](#issue-priority)
  rather than appeal. A decision going against you is not a conduct violation.
  How someone argues for that decision can be, and that part is in scope.
- **Bug reports, feature requests, and support questions.** These belong on
  the [issue tracker](https://github.com/dsx-ai-factory/topograph/issues) or in the
  [community Slack channels](#community). A stalled issue or an unreviewed
  pull request is a [follow-up](#following-up) matter, not a conduct one.
- **Conduct with no connection to a project space.** The Code of Conduct
  covers project spaces and situations where someone is representing the
  project, as defined in [its Scope section](CODE_OF_CONDUCT.md#scope).
  Behavior on unrelated repositories, personal social media, or other
  communities is outside what this project's maintainers can act on, though
  NVIDIA employees remain subject to NVIDIA policy wherever they are.
