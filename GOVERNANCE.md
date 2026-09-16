# Topograph Governance

## Scope

This document covers how decisions are made, who holds which roles, and how contributors advance in Topograph. It applies to the upstream repository at https://github.com/dsx-ai-factory/topograph.

---

## Roles and responsibilities

The first three columns are contributor ladder roles — earned through the criteria in the sections below. The diagram below shows how contributors advance through the ladder and how Project Leadership relates to it.

```mermaid
flowchart LR
    start([First merged PR<br/>automatic]) --> C[Contributor]

    C -->|"nominated<br/>1 second<br/>no objection in 5 business days"| R[Reviewer]
    R -->|"nominated<br/>2 seconds<br/>no objection in 5 business days"| M[Maintainer]
    C -. "Area Bootstrap" .-> M

    M -->|inactivity or voluntary| E([Emeritus])
    E -. reinstatement .-> M

    subgraph appt ["NVIDIA Appointment"]
        PL[/Project Leader/]
    end
    NVIDIA([NVIDIA]) --> PL
```

| | Contributor | Reviewer | Maintainer | Project Leader* |
| --- | --- | --- | --- | --- |
| Open issues and PRs | ✅ | ✅ | ✅ | ✅ |
| Comment on PRs | ✅ | ✅ | ✅ | ✅ |
| Formally approve a PR | — | ✅ | ✅ | ✅ |
| Merge a PR | — | — | ✅ | ✅ |
| Triage issues (label, reproduce, link duplicates) | ✅ | ✅ | ✅ | ✅ |
| Close issues | ✅ (own only) | ✅ (routine only) | ✅ | ✅ |
| Propose significant or architectural changes | ✅ | ✅ | ✅ | ✅ |
| Vote on significant or architectural changes | — | — | ✅ | tie-break only |
| Nominate reviewers or maintainers | — | ✅ | ✅ | ✅ |
| Vote on governance decisions | — | — | ✅ | tie-break only |
| Cast deciding vote on ties | — | — | — | ✅ |
| Final authority on decisions that affect IP | — | — | — | ✅ (for NVIDIA) |
| Cut releases | — | — | ✅ | ✅ |

_* Project Leadership is an NVIDIA appointment, not a contributor ladder role. Current Project Leaders are listed in [MAINTAINERS.md](./MAINTAINERS.md)._

A reviewer approval means technical acceptance. The code is correct, the approach is sound, and it meets the quality standards of the project. The maintainer who merges the PR verifies that every commit has DCO sign-off, required CI checks are passing, required approvals are present, and branch protection permits the merge. The maintainer does not review the code again. The two acts answer different questions, and two different people perform them.

A reviewer can close a routine issue, which means a duplicate, an issue that is already fixed, an answered question, or a stale issue. A reviewer must not close an issue that reports an open bug or an unresolved feature request. Anyone can close an issue that they opened.

Architectural changes follow the significant-change process. Any contributor can open a proposal, but only maintainers vote. If a blocking objection stays unresolved, the maintainers vote on it, and the Project Leaders break a tie.

The active maintainer list is in [MAINTAINERS.md](./MAINTAINERS.md). Each entry includes the individual's name, GitHub handle, organizational affiliation, and the areas they own.

**NVIDIA engineers** hold maintainership in their capacity as NVIDIA employees. If an NVIDIA engineer leaves NVIDIA, their maintainer rights end with their employment unless they are re-nominated and approved as an external contributor.

**External contributors** hold maintainership as individuals. Employer clearance is required at appointment. If a maintainer changes employers after appointment, their role is unaffected — any conflict with their new employer's policies is the individual's responsibility to manage.

No single external organization may hold more than 2 external maintainer seats. The maintainers check the cap at appointment only. If a job change puts an organization above 2 seats, no maintainer loses their seat. The maintainers must not appoint another maintainer from that organization until the count returns to 2 or fewer.

All maintainers and Project Leaders must enable two-factor authentication on their GitHub account.

### Scoped maintainers

Projects with distinct subsystems or modules may define scoped maintainers in [CODEOWNERS](./CODEOWNERS). A scoped maintainer has review and merge authority over the paths they own and is expected to review all PRs touching those areas. A project-wide maintainer (listed without a path scope) can merge anywhere.

A scoped maintainer can block a PR that touches the paths they own. A project-wide maintainer must not merge the PR while the objection stands. If the two maintainers cannot agree, either one can raise the question as a significant change, and the decision process below applies.

### Project Leadership

NVIDIA may appoint one or more Project Leaders. The Project Leaders hold the tie-break vote on any deadlocked maintainer decision. NVIDIA holds the authority over decisions that affect IP, which are license changes, project deprecation, and the transfer of the project to a foundation. The Project Leaders use that authority for NVIDIA.

A Project Leader works in the maintainer group with full review and merge rights, but holds the role as part of their work as an NVIDIA engineer, not through the contributor ladder. A Project Leader who leaves NVIDIA also leaves the role. The current Project Leaders are listed in [MAINTAINERS.md](./MAINTAINERS.md).

A Project Leader is an active maintainer, but does not vote in the normal count. The list of eligible voters does not include Project Leaders, so no person votes twice on the same decision. A Project Leader states their position in the discussion before the vote opens, in the same way as any other maintainer.

### Emeritus

A maintainer who steps down or is removed for inactivity is recognized as an Emeritus Maintainer and listed permanently in a dedicated Emeritus section of MAINTAINERS.md.

An emeritus maintainer can ask to return to active status after they resume regular contributions. They post the request as a public GitHub issue or PR. A PR that moves their entry out of the Emeritus section of MAINTAINERS.md counts as the request. The request follows the same rule as a maintainer nomination. It needs **two seconds** from existing maintainers, and no blocking objection within 5 business days. A maintainer who was removed for cause cannot return.

---

## Becoming a contributor

Contributor status is automatic on the first merged pull request. No nomination is required.

---

## Becoming a reviewer

Any contributor may be nominated for reviewer by an existing reviewer or maintainer after demonstrating:

- At least **3 months** of regular participation in the project
- At least **5 merged pull requests** that required real judgment (not typo fixes or automated dependency bumps)
- At least **5 substantive review comments** on other contributors' PRs

The nomination is posted as a public GitHub issue or PR. It requires **one second** from a maintainer. If there are no blocking objections from maintainers within 5 business days, the nomination is accepted.

---

## Becoming a maintainer

Any reviewer or maintainer may nominate a reviewer (or, in exceptional cases, a contributor) for maintainer after they have demonstrated:

- At least **3 months** of sustained contribution since becoming a reviewer, or **6 months** from first contribution for a direct nomination
- At least **10 merged pull requests** of non-trivial scope, of which at least 5 came after becoming a reviewer
- At least **10 substantive code reviews** that improved the quality of the merged PRs, of which at least 5 came after becoming a reviewer
- Familiarity with the project's contribution process, testing requirements, and coding standards
- Reliability: following through on review commitments and responding within expected timeframes

Documentation, issue triage, and community participation count toward the overall picture but do not substitute for code contribution and review.

The nomination is posted as a public GitHub issue or PR. It requires **two seconds** from existing maintainers. If there are no blocking objections from maintainers within 5 business days, the nomination is accepted.

### Area bootstrap

When a new module or subsystem is added to the project and there is no existing contributor base to draw from, the standard tenure and contribution thresholds may be waived for the individual who designed or built that area. An area bootstrap nomination must state explicitly that the standard criteria are not met and why the nominee's domain ownership of the area justifies the exception. It requires **three seconds** from existing maintainers rather than two, and is subject to the same 5-business-day objection window. A bootstrap maintainer must meet the standard criteria within 6 months of their appointment. If they do not, the maintainers review the appointment. The maintainers can extend the period once by 6 months, or move the bootstrap maintainer to Emeritus status.

---

## Inactivity and removal

A maintainer is considered **inactive** if, over any **6 consecutive months**, they have not done any of the following:

- Merged a pull request
- Submitted a substantive review comment on a PR
- Triaged or commented on an issue
- Participated in a governance discussion or vote

Before removal, another maintainer notifies the inactive maintainer privately and allows a **2-week response window**. If there is no response, the maintainer moves to Emeritus status. If they respond and commit to resuming, the current maintainers may agree to extend the window once.

The maintainers can also remove a maintainer for cause, for example a breach of the [Code of Conduct](./CODE_OF_CONDUCT.md). Removal for cause needs a two-thirds supermajority vote of the eligible voters. It does not depend on the inactivity process.

---

## Decision process

### Routine decisions

Routine decisions — bug fixes, minor features, documentation, dependency updates — proceed via PR. At least one reviewer or maintainer approval is required for technical acceptance; a maintainer then merges after verifying project requirements are met. The author cannot self-merge.

### Significant changes

Significant changes require a prior proposal before implementation work begins. A change is significant if it involves:

- New features or subsystems
- Breaking changes to APIs or behavior
- Changes to the contribution model, release cadence, or dependencies
- Changes to this governance document

A proposal is a GitHub Discussion, design doc, or RFC that explains: what is changing, why, what alternatives were considered, and the impact on existing contributors and users.

**Lazy consensus** governs significant decisions: a proposal open for **5 business days** with no blocking objection from a maintainer is accepted. Silence is consent. A blocking objection must be stated in writing with a specific reason — "I disagree" is not a blocking objection.

If a blocking objection is raised, maintainers discuss until the objection is resolved or withdrawn. If it cannot be resolved, any maintainer may call a vote.

**Voting mechanics.** A maintainer opens the vote as a public issue. Each voter posts their vote in the issue thread, so every vote stays on the public record with the outcome. The voting period is **5 business days**.

**Active maintainers** are all maintainers who are not in Emeritus status. Project Leaders are active maintainers.

**Eligible voters** are the active maintainers who are not Project Leaders, and who did at least one of the actions listed under [Inactivity and removal](#inactivity-and-removal) in the **90 days** before the vote opened. The list of eligible voters is fixed when the vote opens.

Every vote threshold in this document counts against the list of eligible voters. A maintainer who does not vote counts as a vote against, so the project needs no separate quorum rule. The 90-day window keeps that rule fair, because a maintainer who stopped work on the project cannot vote against every proposal by doing nothing.

Silence means consent at the proposal stage, because no vote is open and no list of voters exists. Silence means no once a vote is open, because a maintainer asked for the vote and the list of voters is fixed.

A **simple majority** of eligible voters decides. If the two counts are equal, every Project Leader posts a tie-break vote, and a majority of the Project Leaders decides. If no Project Leader is designated, or if the Project Leaders are equally split, the proposal does not pass.

License changes, project deprecation, and contributing the project to a foundation are decided by NVIDIA regardless of any vote, as these involve IP rights NVIDIA holds as upstream owner. All other decisions — roadmap, architecture, contribution model, release cadence — are governed by the maintainer group.

### Architectural decisions

Architectural changes follow this sequence:

1. **Propose** — anyone opens an RFC as a GitHub Discussion or design doc covering what is changing, why, and what alternatives were considered.
2. **Discuss** — open to all; all input is visible and on the record.
3. **Technical review** — reviewers are expected to weigh in with substantive feedback before the decision window closes, not just permitted to.
4. **Decide** — maintainers apply lazy consensus (5 business days). If a blocking objection is raised and cannot be resolved, maintainers vote; simple majority decides with the Project Leaders breaking a tie.
5. **Record** — the outcome and the key reasoning are posted to the RFC thread or the merging PR. A decision that cannot be reconstructed later from public record is not complete.

### Governance changes

A change to this document uses the proposal process for significant changes above. Lazy consensus does not apply. The maintainers always hold a vote, and the change needs a **two-thirds supermajority** of the eligible voters to pass. If the maintainers cannot reach a supermajority, NVIDIA can change this document without a vote.

### PR rejection

A maintainer who closes a PR must state in writing, in the PR thread, the specific reason. "Not a fit" is not sufficient. The explanation must be specific enough that the contributor could address it and resubmit.

A contributor who believes their PR was rejected unfairly may request a second review from any other maintainer, then escalate to the full maintainer group by opening a discussion tagging all maintainers (allow 10 business days for a response). If still unresolved, a simple majority vote of the eligible voters decides.
