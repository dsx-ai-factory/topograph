# TAS lab

A kind cluster for trying topology-aware workload scheduling with the node labels that
[Topograph](../../README.md) writes. Two consumers read the same labels:

- the **kube-scheduler** (`GenericWorkload` + `TopologyAwareWorkloadScheduling`), and
- **Kueue** with Topology Aware Scheduling and topology spreading (plus LeaderWorkerSet).

```
tests/models/tas-lab.yaml ──> Topograph (test provider, k8s engine)
                                │  fabric.topograph.run/tier-0 = rack-a | rack-b
                                │  fabric.topograph.run/tier-1 = spine
                                ▼
                 four kind workers (nothing else labels them)
                        ┌───────┴────────┐
          kube-scheduler PodGroup     Kueue Topology
          constraint: tier-0          levels: tier-1 > tier-0 > hostname
```

The kind, Kubernetes and Kueue versions are set in one place, [`versions.env`](versions.env)
(see [Versions](#versions)). Every scenario below passes from a clean setup with the versions
listed there.

## Layout

```
demos/tas-lab/
├── setup/                       prepare the lab (run once, in order)
│   ├── setup.sh                 runs the five steps below
│   ├── cluster.sh               1. kind cluster, scheduler-metrics RBAC
│   ├── topograph.sh             2. build + install Topograph, which labels the nodes
│   ├── kueue.sh                 3. install Kueue
│   ├── lws.sh                   4. install LeaderWorkerSet
│   ├── chainsaw.sh              5. install the Chainsaw CLI into .bin/
│   └── kind-config.yaml, values.topograph.yaml, metrics-rbac.yaml
├── scenarios/                   Chainsaw tests, one directory per scenario (a README and one test per case)
│   ├── 01-gang-rack/                          kube-scheduler
│   ├── 02-missing-label/                      kube-scheduler
│   ├── 03-gate-enabled-after/                 kube-scheduler
│   ├── 04-scheduler-label-keys/               kube-scheduler
│   ├── 05-kueue-label-keys/                   Kueue
│   ├── 06-kueue-topology-spreading/           Kueue
│   ├── 07-kueue-label-aliases/                Kueue
│   ├── 08-kueue-topology-shapes/              Kueue
│   ├── templates/               steps the tests share (Kueue queues, a Job, Topograph labels, metrics)
│   ├── chainsaw-config.yaml     Chainsaw settings (one test at a time)
│   └── run.sh                   runs the tests against the lab cluster
├── teardown/teardown.sh         delete the cluster
├── versions.env                 kind, Kubernetes, Kueue, LWS and Chainsaw versions (one place)
└── lib/                         helpers shared by the scripts (common.sh, capture-metrics.sh)
```

The topologies Topograph replays live outside this directory, in
[`tests/models/tas-lab.yaml`](../../tests/models/tas-lab.yaml) (two racks under one spine; scenarios
01 and 03), [`tas-lab-multitier.yaml`](../../tests/models/tas-lab-multitier.yaml) (four leaves, two
spines and three NVLink domains; scenarios 04-08),
[`tas-lab-subdomain.yaml`](../../tests/models/tas-lab-subdomain.yaml) (an accelerator domain with
sub-domains; scenarios 04 and 08), [`tas-lab-nodomain.yaml`](../../tests/models/tas-lab-nodomain.yaml)
(multitier with no domain on one node; scenarios 02 and 08) and
[`tas-lab-crossing.yaml`](../../tests/models/tas-lab-crossing.yaml) (a domain that spans two
spines; scenario 08). Scenarios switch between them themselves. The test provider only reads
models embedded in the Topograph binary, which is why `setup/topograph.sh` builds the image from
this checkout.

Each case is a [Chainsaw](https://kyverno.github.io/chainsaw/) test: it makes sure Topograph's labels
are in place, creates what it needs in a namespace of its own, asserts the outcome and removes
everything it created. You can run any scenario at any time after setup.

## Setup

Prerequisites: `kind`, `kubectl`, `helm`, `curl`, `make`, `python3`, and a
container engine reachable as `docker` (podman's Docker shim works). The container engine needs a few GB free: the Topograph
build pulls a Go image and every kind node imports the Topograph image.

```bash
cd demos/tas-lab
./setup/setup.sh          # all five steps, about 5 minutes the first time
```

Or step by step:

| Step | Command | What it does |
|---|---|---|
| 1 | `./setup/cluster.sh` | Creates `kind-tas-lab` (1 control plane, 4 workers) with `GenericWorkload` and `TopologyAwareWorkloadScheduling` on the kube-apiserver and kube-scheduler, and enables `scheduling.k8s.io/v1beta1`. The nodes carry no topology labels yet. |
| 2 | `./setup/topograph.sh install` | Builds the Topograph image from this checkout, loads it into kind and installs the chart. Topograph labels the workers from `tas-lab.yaml`. |
| 3 | `./setup/kueue.sh` | Installs Kueue, turns on `TopologyAwareScheduling` and `TASTopologySpreading`, and sets `integrations.labelKeysToCopy: [app]` so spreading can select workloads by label. |
| 4 | `./setup/lws.sh` | Installs LeaderWorkerSet; Kueue's LeaderWorkerSet integration is on by default. |
| 5 | `./setup/chainsaw.sh` | Installs the [Chainsaw](https://kyverno.github.io/chainsaw/) CLI (`CHAINSAW_VERSION` in `versions.env`) into `.bin/`, after checking its checksum. A matching `chainsaw` already on `PATH` is reused. It is only needed for the Chainsaw scenarios and is not part of the cluster. |

Check the result:

```bash
./setup/topograph.sh labels
```

```
NAME                    STATUS   ROLES           TIER-0   TIER-1   DOMAIN   SUB-DOMAIN
tas-lab-control-plane   Ready    control-plane
tas-lab-worker          Ready    <none>          rack-a   spine
tas-lab-worker2         Ready    <none>          rack-a   spine
tas-lab-worker3         Ready    <none>          rack-b   spine
tas-lab-worker4         Ready    <none>          rack-b   spine
```

If a label is later changed by hand, `./setup/topograph.sh generate` makes Topograph restore it.

### Versions

[`versions.env`](versions.env) holds `KIND_VERSION` (the kind CLI the lab was tested with;
`setup/cluster.sh` warns on a mismatch), `K8S_VERSION` (every node), `KIND_NODE_IMAGE`
(defaults to `kindest/node:$K8S_VERSION`), `KUEUE_VERSION` and `LWS_VERSION`. To upgrade, edit that file, or
override a value for one run:

```bash
K8S_VERSION=v1.37.0 ./setup/setup.sh      # any version that has a kindest/node image
```

The scripts are version-agnostic, but some manifests are not: the `scheduling.k8s.io` API
version and feature-gate names in `setup/kind-config.yaml` and the scheduler scenarios (01-04) follow the
Kubernetes version, and the `kueue.x-k8s.io` API version and Kueue gate names follow the Kueue
version. `versions.env` lists what to re-check after a bump.

## Scenarios

A short, plain-language description of each scenario (the problem, what it does, how it is
validated) is in [docs/demos/tas-lab.md](../../docs/demos/tas-lab.md). The details below are for
running and debugging.

Scenarios 01-04 exercise the **kube-scheduler** (KEP-5732); 05-08 exercise **Kueue**. Each scenario
directory has a README with its lab fabric, a table of its cases and how they are validated; every
case's `description` states it as Given, When and Then. Chainsaw waits for the state to be true, gives
every test its own namespace and removes everything it created, so there are no wait loops, result
files or cleanup traps.

```bash
./scenarios/run.sh                                           # every case (about 20 minutes)
./scenarios/run.sh scenarios/05-kueue-label-keys             # one scenario
./scenarios/run.sh --include-test-regex 'chainsaw/too-big'   # only the cases whose name matches
```

(`chainsaw/` in the regex is not a directory: Chainsaw names every test `chainsaw/<case>`, so the
regex has to start with it.)

Use `scenarios/run.sh`: it exports the lab directory that the tests' scripts need. The cases share one
cluster and its node labels, so run one thing at a time (`chainsaw-config.yaml` runs the tests
serially). Scenario 03 restarts the scheduler twice, so its test takes a few minutes. The shared steps
(the Kueue queues, a Job, switching the Topograph labels, the scheduler metrics) are in
[`scenarios/templates/`](scenarios/templates).

Scenarios that submit pods to the kube-scheduler also check its placement metrics, which the tests read
through `lib/capture-metrics.sh` (a baseline before the submission, the change after it):

- `scheduler_generated_placements_total`: candidate placements, one per distinct constraint value
- `scheduler_placement_evaluations_total{result}`: feasibility checks of those candidates
- `scheduler_placement_evaluation_duration_seconds`: how long they took

### 01 Gang with a rack constraint

**What it shows.** The baseline: a `Workload`/`PodGroup` with `gang.minCount: 4` and
`schedulingConstraints.topology[0].key: fabric.topograph.run/tier-0`, plus four pods that join the
group, placed all-or-nothing inside one tier-0 domain. A second case adds an ordinary pod with no
`schedulingGroup` next to the gang.

**Run.** `./scenarios/run.sh scenarios/01-gang-rack`

**Expect.**
- Case 1: all four pods Running on workers that share one tier-0 value (both workers of one rack).
  `scheduler_generated_placements_total` +2 (one candidate each for `rack-a` and `rack-b`),
  `scheduler_placement_evaluations_total{result="feasible"}` +2, and two observations of the
  evaluation-duration histogram (about 1 ms in total).
- Case 2: five pods Running. The plain pod lands on any node, unconstrained, and adds no
  placement: the metrics change exactly as in case 1.

### 02 A node without the constraint key

**What it shows.** What the scheduler does with a node that lacks the constraint key. Topograph
labels the workers from [`tas-lab-nodomain.yaml`](../../tests/models/tas-lab-nodomain.yaml), where
`tas-lab-worker3` has no accelerator domain and so gets no `accelerator.topograph.run/domain` label.

**Run.** `./scenarios/run.sh scenarios/02-missing-label`

**Expect.** With the other workers cordoned and a one-pod gang constrained on the domain key, the
gang stays `Pending` although `tas-lab-worker3` is free. A node without the key belongs to no
placement and adds none (two placements per attempt: `nvl-a` and `nvl-c`).

The test uncordons the workers and restores the default labels with `./setup/topograph.sh generate`
when it ends.

### 03 Gate enabled after the fact

**What it shows.** The trap of turning the feature on late. The gang here fits only if the rack
constraint is **not** enforced (four pods, one per node, but a rack has two nodes), so the gate's
effect is visible. The test turns the scheduler's `TopologyAwareWorkloadScheduling` gate off,
submits the gang, turns the gate on, then deletes and recreates the pods under the same PodGroup. It
needs `docker exec` access to the control-plane node and always leaves the gate on.

**Run.** `./scenarios/run.sh scenarios/03-gate-enabled-after` (about two minutes; the scheduler restarts twice
and the test waits about 30 s after each for it to take its leader lease, because pods submitted
sooner sit `Pending` and look like a result).

**Expect.**
1. Gate off: the constraint is silently ignored. All four pods run, spread over both racks.
2. Gate turned on: the running pods are left alone.
3. Pods recreated under the same PodGroup: the constraint is now enforced, so all four stay `Pending`.

The workload looks fine until it is rescheduled.

The test also asserts that the gate is back on at the end.

### 04 Topograph labels as kube-scheduler constraint keys

**What it shows.** Which Topograph labels work as the `PodGroup` `topology[0].key`, and the
single-level limitation. The tests switch the nodes to `tas-lab-multitier.yaml`, which labels the
same four workers three ways: `tier-0` (four leaf switches, one node each), `tier-1` (two spines, two
nodes each) and `accelerator.topograph.run/domain` (three NVLink domains; `nvl-a` fills the whole of
spine-1, `nvl-b` and `nvl-c` are single leaves, so a domain never spans spines). Each gang runs one
pod per node (hostname anti-affinity), so a domain needs as many nodes as pods.

**Run.** `./scenarios/run.sh scenarios/04-scheduler-label-keys`

**Expect.**

| Case | Key | Gang | Result |
|---|---|---|---|
| `on-tier1` | `fabric.topograph.run/tier-1` | 2 pods | Running on the two nodes of one spine; 2 placements generated (one per spine) |
| `on-tier0` | `fabric.topograph.run/tier-0` | 2 pods | `Pending`: a leaf has one node. 4 placements per attempt, all infeasible; `PodGroupInitiallyScheduled=False` |
| `on-domain` | `accelerator.topograph.run/domain` | 2 pods | Running on `nvl-a`; 3 placements generated, only `nvl-a` feasible |
| `tier1-too-big` | `fabric.topograph.run/tier-1` | 3 pods | `Pending`: a spine has two nodes |
| `domain-too-big` | `accelerator.topograph.run/domain` | 3 pods | `Pending`: the largest domain has two nodes |
| two constraints | `tier-1` and `tier-0` | n/a | Rejected by the API: `Too many: 2: must have at most 1 item` |

The number of placements generated per attempt equals the number of distinct label values; that is
the quantity `PercentageOfPlacementsToScore` caps in releases that have it (not in v1.37).

### 05 Topograph labels as Kueue Topology levels

**What it shows.** The same labels in a Kueue `Topology` with `podset-required-topology` and
`podset-preferred-topology`. Two Topologies: `tier-1 > tier-0 > hostname`, and
`tier-1 > accelerator domain > tier-0 > hostname`, where the domain nests in the fabric. Each pod
requests just over half of a worker's CPU, so a Job of N pods needs N nodes.

**Run.** `./scenarios/run.sh scenarios/05-kueue-label-keys`

**Expect.**

| Annotation | Level | Pods | Result |
|---|---|---|---|
| required | `tier-1` | 2 | Admitted, both pods in one spine |
| required | `tier-1` | 3 | Not admitted: `allows to fit only 2 out of 3 pod(s)` |
| preferred | `tier-1` | 3 | Admitted across both spines |
| required | `tier-0` | 2 | Not admitted (a leaf is one node) |
| required | accelerator domain | 2 | Admitted on `nvl-a`, inside one spine |
| required | accelerator domain | 3 | Not admitted |
| preferred | accelerator domain | 3 | Admitted at a wider level, across both spines |
| required | `tier-1`, Topology without a hostname level | 2 | Admitted |

### 06 Kueue topology spreading of LeaderWorkerSet groups

**What it shows.** `TASTopologySpreading`: two `LeaderWorkerSet` groups (2 pods each) that must each
stay in one spine (`podset-required-topology: ...tier-1`) and are spread across spines by
`podset-topology-spreading` (rule on `tier-1`, `maxShareAllowingPlacement: 0.5`, `Required`). Run
twice, with and without the spreading annotation. Pods request a small share of a worker's CPU, so without spreading nothing
stops the groups from sharing a spine.

**Run.** `./scenarios/run.sh scenarios/06-kueue-topology-spreading` (needs setup steps 3 and 4)

**Expect.** Without the annotation both groups land in the same spine (here on one node). With it the
groups land in different spines and each group's pods stay together.

### 07 Rack and block aliases

**What it shows.** Whether workload manifests need to know which `tier-N` is the rack. The k8s
engine's `fabricLabels` parameter writes alias keys (`cloud.provider.com/topology-rack` and
`-block`, which are provisional) instead of `tier-N`.
Mapping A makes rack the leaf (`tier-0`) and block the spine (`tier-1`). Mapping B makes rack the
spine on the same fabric. The Jobs and LeaderWorkerSets name only the aliases.

**Run.** `./scenarios/run.sh scenarios/07-kueue-label-aliases` (needs setup steps 3 and 4)

**Expect.**
- Mapping A: only the alias labels exist (the default `tier-N` keys are gone); a 2-pod Job with
  required rack is not admitted (a rack is one node), required block is admitted; two
  LeaderWorkerSet groups share a rack without spreading and are in different racks with it.
- Mapping B: the **same** required-rack manifest is now admitted. The manifests are free of `tier-N`
  numbers, but what "rack" means is set by the Topograph configuration of each cluster, and so are
  the Kueue `Topology` levels.

Each test removes its alias labels and regenerates the default labels when it finishes.

### 08 Kueue Topology per label shape, and changing its levels

**What it shows.** A Kueue `Topology` for each shape of Topograph labels (the manifests are in the
[scenario's README](scenarios/08-kueue-topology-shapes/README.md#the-topology-for-each-shape)): fabric tiers
only (`tas-lab.yaml`), fabric plus accelerator domain (`tas-lab-multitier.yaml`), and fabric plus
domain plus sub-domain (`tas-lab-subdomain.yaml`). Then a Topology whose levels change while a
workload waits and while one is admitted. [`templates/stack.yaml`](scenarios/templates/stack.yaml) adds
the `ResourceFlavor` and queues over each Topology.

**Run.** `./scenarios/run.sh scenarios/08-kueue-topology-shapes` (needs setup step 3)

**Expect.**
- Each shape admits a Job that fits one domain of a level and refuses one that does not
  (`allows to fit only 2 out of 3 pod(s)`); required domain across both sub-domains is admitted.
- A node that lacks one of the Topology's level labels (here, one worker without its accelerator
  domain label) is ignored: a Job that needs it is refused (`allows to fit only 3 out of 4 pod(s)`)
  and a smaller one is placed on the other nodes.
- Levels: a Job requiring a label that is not yet a level waits (`Flavor ... does not contain the
  requested level`); adding the level admits it without recreating it; dropping the widest level
  while it is admitted leaves it admitted and running; removing the hostname level is rejected
  (`levels are mutable only when kubernetes.io/hostname is the lowest level both before and after
  the change`).

## Tear down

```bash
./teardown/teardown.sh
```

This deletes the kind cluster (Topograph and Kueue go with it) and the scratch files the
scenarios write under `${TMPDIR:-/tmp}/tas-lab-<uid>`. The Topograph image stays in the local image
store; remove it with `docker rmi ghcr.io/dsx-ai-factory/topograph:tas-lab`.
