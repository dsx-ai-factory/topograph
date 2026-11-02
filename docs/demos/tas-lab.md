# TAS Lab: Topology-Aware Scheduling Demo

The TAS lab is a small [kind](https://kind.sigs.k8s.io/) cluster (one control plane, four
workers) that shows how schedulers use the node labels Topograph writes. It lives in
[`demos/tas-lab`](https://github.com/dsx-ai-factory/topograph/tree/main/demos/tas-lab).

## The idea in one minute

Topograph labels each node with where it sits in the network, for example
`fabric.topograph.run/tier-0` (rack or leaf switch), `fabric.topograph.run/tier-1` (spine) and
`accelerator.topograph.run/domain` (NVLink domain). A scheduler does not know what these mean. It
only needs to be told "keep these pods under the same value of this label".

Two schedulers can do that, and the lab exercises both:

| Scheduler | How you ask for locality | Scenarios |
|---|---|---|
| kube-scheduler (`GenericWorkload`, `TopologyAwareWorkloadScheduling`) | a `PodGroup` with one topology constraint key | 01 to 04 |
| [Kueue](https://kueue.sigs.k8s.io/) Topology Aware Scheduling | a `Topology` listing label keys as levels, and a pod annotation | 05 to 08 |

The test provider replays a model file from `tests/models/`, so no cloud account or real fabric is
needed. Four workers are enough to see every effect: two racks under one spine in the simple
model, four leaves under two spines plus accelerator domains in the multi-tier model.

## Run it

Prerequisites: `kind`, `kubectl`, `helm`, `curl`, `make`, `python3`, and a container
runtime.

```bash
cd demos/tas-lab
./setup/setup.sh                      # about 5 minutes the first time
./scenarios/run.sh                    # every scenario (about 20 minutes)

./scenarios/run.sh scenarios/01-gang-rack   # or one scenario at a time
./teardown/teardown.sh                # delete the cluster
```

Scenarios 05 to 08 need Kueue (`./setup/kueue.sh`), and 06 and 07 also need LeaderWorkerSet
(`./setup/lws.sh`). `setup.sh` installs both, and also the Chainsaw CLI (`./setup/chainsaw.sh`).

The scenarios are [Chainsaw](https://kyverno.github.io/chainsaw/) tests, one directory per scenario
under `scenarios/`, each with a README. Each case is one test with a Given/When/Then description.
A test makes sure the nodes carry the right labels, creates what it needs in a namespace of its own,
asserts the outcome and removes everything it created, even if it fails. Do not run two tests at the
same time, because they share the cluster and the node labels.

## How each scenario works

Scenarios that use the kube-scheduler also compare its placement metrics from before and after
the submission (a test takes a baseline, submits the workload, then checks the change):

- `scheduler_generated_placements_total`: candidate placements, one per distinct label value.
- `scheduler_placement_evaluations_total{result}`: how many candidates were feasible or not.

---

## Scenarios with the kube-scheduler

### 01 Gang in one rack

**Problem.** A distributed job needs all of its pods close together, or none of them should start.

**What the scenario does.** Submits a four-pod gang (`gang.minCount: 4`) with the constraint key
`fabric.topograph.run/tier-0`. In a second case, it adds an ordinary pod with no gang next to it.

**Expected result.** All four pods run on nodes with the same tier-0 value, so inside one rack. The
ordinary pod runs anywhere and does not change the placement count.

**How it is validated.**
- Four pods are Running, on exactly one tier-0 value.
- Two placements were generated, one for each rack, and both were feasible.
- In the second case five pods run, the gang is still in one rack, and the plain pod added no
  placement.

### 02 A node without the label

**Problem.** In a real cluster some nodes may have no value for a label, for instance a node whose
provider reports no accelerator domain. What does the scheduler do with it?

**What the scenario does.** Topograph labels the workers so that `tas-lab-worker3` has no
`accelerator.topograph.run/domain`. The other workers are cordoned, so that node is the only free
one. A one-pod gang constrained on the domain key is then submitted.

**Expected result.** The pod stays Pending even though a node is free. A node without the key
belongs to no placement, so the scheduler never considers it.

**How it is validated.**
- The pod is Pending, and it is not on `tas-lab-worker3`.
- Placements were generated, in pairs (`nvl-a` and `nvl-c` per attempt), so the unlabelled node
  added none.

### 03 Turning the feature on late

**Problem.** If the scheduling gate is enabled after a workload was created, does the workload
change?

**What the scenario does.** Submits a gang that only fits if the rack constraint is ignored (four
pods, one per node, but a rack has two nodes). It turns the scheduler's
`TopologyAwareWorkloadScheduling` gate off, submits the gang, turns the gate on, then deletes and
recreates the pods under the same PodGroup. It always leaves the gate on. This scenario takes about
two minutes because the scheduler restarts twice.

**Expected result.**
1. Gate off: the constraint is silently ignored and all four pods run, across both racks.
2. Gate on: the running pods are left alone.
3. Pods recreated: the constraint is now enforced and all four stay Pending.

The workload looks healthy until it is rescheduled.

**How it is validated.** Four pods running over two racks with the gate off, four still running
after the gate is on, four Pending after recreation, and the gate reads `true` again at the end.

### 04 Which labels can be a constraint key

**Problem.** The `PodGroup` takes one label key. Which Topograph labels work, and what happens when
the gang cannot fit?

**What the scenario does.** Switches the nodes to the multi-tier model and runs gangs with one pod
per node, each with a different key: `tier-1` (spine), `tier-0` (leaf), `accelerator.../domain`
and `accelerator.../sub-domain`. It also tries a constraint with two keys.

| Key | Gang | Expected |
|---|---|---|
| `tier-1` | 2 pods | Running in one spine |
| `tier-0` | 2 pods | Pending: a leaf has only one node |
| `domain` | 2 pods | Running in `nvl-a`, the only domain with two nodes |
| `tier-1` | 3 pods | Pending: a spine has two nodes |
| `domain` | 3 pods | Pending: no domain has three nodes |
| `sub-domain` | 2 pods / 3 pods | Running in one sub-domain / Pending |
| two keys | n/a | Rejected by the API (at most one item) |

**How it is validated.** For each case the test checks how many pods are Running or Pending,
how many label values they span, the `PodGroupInitiallyScheduled` condition, and the metrics: the
number of generated placements equals the number of distinct label values, and only the domain
that fits is feasible. For the two-key case it checks that the API rejected it with the item-limit
error, not with some other failure.

---

## Scenarios with Kueue

In these scenarios a pod asks for locality with an annotation:
`kueue.x-k8s.io/podset-required-topology` (must fit one domain of that level, otherwise wait) or
`podset-preferred-topology` (try, then fall back to a wider level). Each pod requests a little over
half of a worker's CPU, so one pod fills one node and the placement is easy to read.

### 05 Labels as Kueue Topology levels

**Problem.** How do the Topograph labels map to a Kueue `Topology`, and how do `required` and
`preferred` behave?

**What the scenario does.** Creates two Topologies, `tier-1 > tier-0 > hostname` and
`tier-1 > accelerator domain > tier-0 > hostname`, and submits Jobs with different annotations and
sizes.

**Expected result.**

| Annotation | Level | Pods | Result |
|---|---|---|---|
| required | `tier-1` | 2 | Admitted, in one spine |
| required | `tier-1` | 3 | Not admitted: only 2 of 3 fit a spine |
| preferred | `tier-1` | 3 | Admitted across both spines |
| required | `tier-0` | 2 | Not admitted (a leaf is one node) |
| required / preferred | domain | 2 / 3 | Admitted on `nvl-a` / admitted across both spines |
| required | `tier-1`, no hostname level | 2 | Admitted |

**How it is validated.** For each Job it reads Kueue's `Admitted` condition, checks that every pod
of an admitted Job is actually Running (not just assigned to a node), counts the distinct label
values of the nodes the pods ran on, and checks that each refusal message gives the expected reason, such as `fit only 2 out of 3`.

### 06 Spreading groups across spines

**Problem.** Two replicas of the same workload should each stay together but not share a spine, so
one spine failure does not take both down.

**What the scenario does.** Runs two LeaderWorkerSet groups (two pods each) that must each stay in
one spine. It runs once without and once with `podset-topology-spreading` on `tier-1`.

**Expected result.** Without spreading both groups land in the same spine. With it they land in
different spines and each group stays together.

**How it is validated.** In both runs both groups are admitted, each runs two pods and stays in
one spine. The baseline run checks that the groups share a spine, and the spread run checks they
are in different spines and all four pods run.

### 07 Rack and block aliases

**Problem.** Workload manifests should not need to know which `tier-N` number is the rack. That is
a decision of each site.

**What the scenario does.** Uses the Kubernetes engine's `fabricLabels` setting to publish the
fabric tiers under alias names (`...topology-rack`, `...topology-block`) instead of `tier-N`, and
submits Jobs and LeaderWorkerSets that name only the aliases. Mapping A makes rack the leaf
switch. Mapping B maps rack to the spine, on the same fabric, with the same manifest.

**Expected result.** In mapping A only the alias labels exist, a required-rack Job of two pods is
refused (a rack is one node) and a required-block Job is admitted. In mapping B the same
required-rack manifest is admitted.

**How it is validated.** The test checks that four nodes carry the rack alias and that no `tier-N`
labels remain. It checks the admitted state of each Job and that all pods of an admitted Job are
Running. For the LeaderWorkerSets, it checks that each group has two Running pods, that the rack
values are not empty, and that the groups share a rack without spreading and are in different racks
with it. Each test removes its alias labels and restores the default labels when it ends.

### 08 One Topology per label shape, and changing levels

**Problem.** Which Kueue `Topology` fits each shape of Topograph labels, and what happens when a
Topology's levels change while workloads are waiting or running?

**What the scenario does.** Tries a Kueue Topology for each of three
shapes: fabric tiers only, fabric plus accelerator domain, and fabric plus domain plus sub-domain.
It then covers a node that lacks a level label, a domain that crosses two spines (using
three-pod jobs), and a Topology whose levels are edited at run time.

**Expected result.**
- Each shape admits a Job that fits one domain of a level and refuses one that does not.
- A node missing a level label is ignored: a four-pod Job is refused and a three-pod Job runs on
  the other nodes.
- A domain that crosses spines is not found by a Topology that nests it under a spine, but it is
  found when the domain level comes first.
- A Job needing a label that is not yet a level waits. Adding the level admits it without
  recreating it. Dropping the widest level keeps an admitted Job running. Removing the hostname
  level is rejected.

**How it is validated.** The test checks the admitted state of each Job, that all pods of every
admitted Job are Running, and the number of tier, domain and sub-domain values the pods used. It
checks the three level-change outcomes and that the API rejects removing the hostname level.
Chainsaw removes the queues and Topologies a test created, and the test restores the default labels,
even if it fails part way.

---

## Troubleshooting

- **A scenario fails at topology generation (HTTP 400).** The Topograph image was built before a
  model file was added. Run `./setup/topograph.sh install` to rebuild it.
- **Do not run scenarios in parallel.** They share one cluster and one set of node labels.
- **Pick the container runtime.** If `kind` uses the wrong one, set `KIND_EXPERIMENTAL_PROVIDER`
  to `docker` or `podman`.

See the [Kubernetes engine](../engines/k8s.md) page for how Topograph labels nodes, and
[Node Labels and Annotations](../reference/node-labels.md) for every key. The Kueue `Topology`
manifests for each shape of labels are in the
[scenario 08 README](https://github.com/dsx-ai-factory/topograph/blob/main/demos/tas-lab/scenarios/08-kueue-topology-shapes/README.md#the-topology-for-each-shape).
