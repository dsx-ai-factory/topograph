# Scenario 07: rack and block aliases

The same Kueue checks as scenarios 05 and 06, written against `rack` and `block` aliases instead of
`fabric.topograph.run/tier-N`. Topograph's k8s engine writes the aliases through its
`fabricLabels` parameter (closest tier first), so which tier is the rack is decided once, in the
Topograph configuration.
Needs `setup/kueue.sh` and `setup/lws.sh`.

The aliases are `cloud.provider.com/topology-rack`, `cloud.provider.com/topology-block` and
`example.com/leaf`. Each test removes them from the nodes and regenerates the default labels when
it ends.

## The mappings (tests/models/tas-lab-multitier.yaml)

- **A:** `fabricLabels: [rack, block]`: rack = tier-0 (leaf, one node), block = tier-1 (spine, two nodes).
- **B:** `fabricLabels: [example.com/leaf, rack]`: tier-0 goes to the leaf key, rack = tier-1 (spine).

## The cases

| Case | Mapping | Workload | Expected |
|---|---|---|---|
| [`a-required-rack-refused`](a-required-rack-refused) | A | 2-pod Job, required rack | Not admitted; the default tier labels are gone |
| [`a-required-block-admitted`](a-required-block-admitted) | A | 2-pod Job, required block | Admitted and Running |
| [`a-lws-baseline-shares-a-rack`](a-lws-baseline-shares-a-rack) | A | 2 LWS groups, required rack | Both Running, each in one rack, the same rack |
| [`a-lws-spread-uses-different-racks`](a-lws-spread-uses-different-racks) | A | the same, with spreading on rack | Each in one rack, in different racks |
| [`b-same-manifest-new-meaning`](b-same-manifest-new-meaning) | B | the 2-pod Job, required rack | Admitted: a rack is now a spine |

## Run

```bash
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/07-kueue-label-aliases
```

Run one thing at a time: the cases share the cluster and its node labels.
