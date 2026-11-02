# Scenario 08: the Kueue Topology for each label shape

A Kueue `Topology` lists node labels, coarsest level first and `kubernetes.io/hostname` last, and a
node that lacks a level label is ignored. These tests build the Topology that fits each shape of
Topograph labels, and change a Topology's levels while a Job waits or runs. Each pod fills a node,
so a Job of N pods needs N nodes. Needs
`setup/kueue.sh`.

## The cases

| Case | Model | Topology levels | Job | Expected |
|---|---|---|---|---|
| [`s1-tier1-4-admitted`](s1-tier1-4-admitted) | tas-lab | tier-1 > tier-0 > hostname | 4, required tier-1 | Admitted, in one spine |
| [`s1-tier0-2-admitted`](s1-tier0-2-admitted) | tas-lab | same | 2, required tier-0 | Admitted, in one rack |
| [`s1-tier0-3-refused`](s1-tier0-3-refused) | tas-lab | same | 3, required tier-0 | Not admitted: a rack has two nodes |
| [`s2-domain-2-admitted`](s2-domain-2-admitted) | multitier | tier-1 > domain > tier-0 > hostname | 2, required domain | Admitted, in one domain inside one spine |
| [`s2-domain-3-refused`](s2-domain-3-refused) | multitier | same | 3, required domain | Not admitted |
| [`s2-missing-4-refused`](s2-missing-4-refused) | nodomain | same | 4, preferred tier-1 | Not admitted: a node without the domain label is ignored |
| [`s2-missing-3-admitted`](s2-missing-3-admitted) | nodomain | same | 3, preferred tier-1 | Admitted, never on the unlabelled node |
| [`s4-crossing-nested-refused`](s4-crossing-nested-refused) | crossing | same | 3, required domain | Not admitted: nested, the domain has two nodes per spine |
| [`s4-crossing-domain-first-admitted`](s4-crossing-domain-first-admitted) | crossing | domain > tier-1 > tier-0 > hostname | 3, required domain | Admitted, on both spines |
| [`s3-sub-2-admitted`](s3-sub-2-admitted) | subdomain | tier-1 > domain > sub-domain > tier-0 > hostname | 2, required sub-domain | Admitted, in one sub-domain |
| [`s3-sub-3-refused`](s3-sub-3-refused) | subdomain | same | 3, required sub-domain | Not admitted |
| [`s3-domain-4-admitted`](s3-domain-4-admitted) | subdomain | same | 4, required domain | Admitted, across both sub-domains |
| [`mutable-levels`](mutable-levels) | multitier | changed during the test | 2, required domain | See below |

`mutable-levels` runs in order: the Job requires the domain, which is not a level yet (not admitted,
and Kueue says the level is missing); the domain is added as a level (the waiting Job is admitted
without being recreated, inside one domain); the widest level is dropped (it stays admitted and
Running); the hostname level is removed (the API server rejects it).

## The Topology for each shape

A Kueue `Topology` lists node label keys, coarsest level first and `kubernetes.io/hostname` last; a
`ResourceFlavor` selects the nodes it covers and names one of these with `spec.topologyName`
([`templates/stack.yaml`](../templates/stack.yaml) creates the flavor and queues). The tests build
these Topologies. Every node of a flavor must carry every level label: Kueue ignores a node that lacks
one.

```yaml
# Shape 1: fabric tiers only (for example nebius). The tiers widest first.
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: topograph-fabric
spec:
  levels:
    - nodeLabel: fabric.topograph.run/tier-1
    - nodeLabel: fabric.topograph.run/tier-0
    - nodeLabel: kubernetes.io/hostname
---
# Shape 2: fabric tiers plus an accelerator domain that nests inside the fabric (for example aws,
# gcp, oci without rack metadata). The domain sits between the fabric tier that contains it and
# the tier it contains.
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: topograph-fabric-domain
spec:
  levels:
    - nodeLabel: fabric.topograph.run/tier-1
    - nodeLabel: accelerator.topograph.run/domain
    - nodeLabel: fabric.topograph.run/tier-0
    - nodeLabel: kubernetes.io/hostname
---
# Shape 3: fabric tiers, an accelerator domain and a sub-domain nested inside it (oci with rack
# metadata).
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: topograph-fabric-domain-subdomain
spec:
  levels:
    - nodeLabel: fabric.topograph.run/tier-1
    - nodeLabel: accelerator.topograph.run/domain
    - nodeLabel: accelerator.topograph.run/sub-domain
    - nodeLabel: fabric.topograph.run/tier-0
    - nodeLabel: kubernetes.io/hostname
---
# Shape 4: an accelerator domain that crosses fabric tiers (for example one NVLink domain whose nodes
# sit under two spines). The domain cannot sit below a tier it crosses, so it goes above it. The
# nodes of one domain are then grouped by the fabric tiers inside it.
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: topograph-domain-first
spec:
  levels:
    - nodeLabel: accelerator.topograph.run/domain
    - nodeLabel: fabric.topograph.run/tier-1
    - nodeLabel: fabric.topograph.run/tier-0
    - nodeLabel: kubernetes.io/hostname
```

## How a case is validated

As in scenario 05: the Workload's `Admitted` condition (and, for a refusal, Kueue's reason), all pods Ready for an admitted Job, and the
number of distinct label values among the pods' nodes.

## Run

```bash
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/08-kueue-topology-shapes
```

Run one thing at a time: the cases share the cluster and its node labels.
