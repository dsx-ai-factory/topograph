# Scenario 06: Kueue topology spreading

Two LeaderWorkerSet groups of two pods, each group asking Kueue for required `tier-1` locality, on a
Topology tier-1 > tier-0 > hostname. The pods are small (an eighth of a worker's CPU), so nothing but
topology decides where they go. The `kueue.x-k8s.io/podset-topology-spreading` annotation adds a
rule that keeps the groups out of the same spine. Needs
`setup/kueue.sh` and `setup/lws.sh`.

## The cases

| Case | Annotations | Expected |
|---|---|---|
| [`baseline-shares-a-spine`](baseline-shares-a-spine) | required tier-1 | Both groups admitted, each in one spine, both in the same spine |
| [`spread-uses-different-spines`](spread-uses-different-spines) | required tier-1 + spreading rule on tier-1 | Both admitted, each in one spine, in different spines |

## How a case is validated

- **Admitted:** two Kueue Workloads have an `Admitted` condition of `True`.
- **Running:** two pods Running in each group (the pods exist some time after the LeaderWorkerSet).
- **Locality:** each group sits in one `tier-1` value; the groups' values are equal or different as expected.

## Run

```bash
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/06-kueue-topology-spreading
```

Run one thing at a time: the cases share the cluster and its node labels.
