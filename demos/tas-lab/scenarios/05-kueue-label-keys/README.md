# Scenario 05: Topograph labels as Kueue Topology levels

Kueue can place a Job's pods by locality (Topology Aware Scheduling). It is told which node labels
form the hierarchy by a `Topology` object, and how strict to be by a pod annotation:

- `kueue.x-k8s.io/podset-required-topology: <label>`: all pods must fit inside one domain of that
  level, otherwise the Job is not admitted.
- `kueue.x-k8s.io/podset-preferred-topology: <label>`: try that level, then wider ones.

These tests use the labels Topograph writes (`fabric.topograph.run/tier-0`, `tier-1` and
`accelerator.topograph.run/domain`) as the levels. Each pod requests just over half of a worker's
CPU, so one pod fills one node and the placement is easy to read.

## The lab fabric (tests/models/tas-lab-multitier.yaml)

| Node | tier-0 (leaf) | tier-1 (spine) | accelerator domain |
|---|---|---|---|
| tas-lab-worker | leaf-1 | spine-1 | nvl-a |
| tas-lab-worker2 | leaf-2 | spine-1 | nvl-a |
| tas-lab-worker3 | leaf-3 | spine-2 | nvl-b |
| tas-lab-worker4 | leaf-4 | spine-2 | nvl-c |

So a leaf holds one node, a spine holds two, and the largest domain (nvl-a) holds two.

## The cases

Every case starts by making sure the nodes carry the labels above, creates its own `Topology`,
queues and Job in a private namespace, asserts the outcome, and is cleaned up by Chainsaw.

| Case | Topology levels | Job | Expected |
|---|---|---|---|
| [`required-tier1-fits`](required-tier1-fits) | tier-1 > tier-0 > hostname | 2 pods, required tier-1 | Admitted; both pods Running in one spine |
| [`required-tier1-too-big`](required-tier1-too-big) | same | 3 pods, required tier-1 | Not admitted ("fit only 2 out of 3"): a spine has two nodes |
| [`preferred-tier1-spans-spines`](preferred-tier1-spans-spines) | same | 3 pods, preferred tier-1 | Admitted; all three Running, across both spines |
| [`required-tier0-refused`](required-tier0-refused) | same | 2 pods, required tier-0 | Not admitted: a leaf is one node |
| [`required-domain-fits`](required-domain-fits) | tier-1 > domain > tier-0 > hostname | 2 pods, required domain | Admitted; both Running in one domain (nvl-a) and one spine |
| [`required-domain-too-big`](required-domain-too-big) | same | 3 pods, required domain | Not admitted: the largest domain has two nodes |
| [`preferred-domain-spans-spines`](preferred-domain-spans-spines) | same | 3 pods, preferred domain | Admitted at a wider level; Running across both spines |
| [`no-hostname-level`](no-hostname-level) | tier-1 > tier-0 (no hostname) | 2 pods, required tier-1 | Admitted; both Running |

Each case's `description` in its `chainsaw-test.yaml` states it as Given (the setup), When (the
action) and Then (the expected result). The table above is the same thing in short.

## How a case is validated

- **Admitted or refused:** the Kueue `Workload` of the Job has an `Admitted` condition of `True` or
  `False`. A refused case must also have the `QuotaReserved` message it expects (for example
  `fit only 2 out of 3`), so a refusal for another reason, such as Kueue not yet knowing the nodes,
  does not pass.
- **Actually running:** an admitted Job has all of its pods Ready, not only assigned to nodes.
- **Locality:** the pods' nodes carry the expected number of distinct `tier-1` or domain values
  (one for "fits in one domain", two for "spans both spines").

## Run

From anywhere (the wrapper finds the lab directory itself):

```bash
demos/tas-lab/scenarios/run.sh                                           # every scenario
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/05-kueue-label-keys   # this directory
demos/tas-lab/scenarios/run.sh --include-test-regex 'chainsaw/too-big'      # cases whose name matches
```

`chainsaw/` in the regex is not a directory: Chainsaw names every test `chainsaw/<case>`.

Run one thing at a time: the cases share the cluster and its node labels, and one pod fills a node.
