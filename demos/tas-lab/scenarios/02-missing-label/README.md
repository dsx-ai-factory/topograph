# Scenario 02: a node that lacks the constraint key

A node without the label a PodGroup constrains on is not a domain of its own: the scheduler does not
place the gang there, even when it is the only free node.
The scheduler's placement metrics are checked as well.

## The lab fabric (tests/models/tas-lab-nodomain.yaml)

The multi-tier model, except that `tas-lab-worker3` has no accelerator domain and so no
`accelerator.topograph.run/domain` label. The test cordons the other three workers, so that node is
the only free one, and uncordons them when it ends.

## The cases

| Case | Gang | Expected |
|---|---|---|
| [`unlabelled-node-not-used`](unlabelled-node-not-used) | 1 pod, domain constraint | PodGroup not scheduled; the pod stays Pending |

## How a case is validated

- **Decided:** the PodGroup's `PodGroupInitiallyScheduled` condition is `False`, so the scheduler tried.
- **Not placed:** after a short wait the pod is still `Pending`.
- **Scheduler metrics:** placements were generated, in a multiple of 2 (nvl-a and nvl-c per attempt): the
  unlabelled node adds none.

## Run

```bash
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/02-missing-label
```

Run one thing at a time: the cases share the cluster and its node labels.
