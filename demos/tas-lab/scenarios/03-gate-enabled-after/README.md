# Scenario 03: the gate enabled after the fact

Turning the scheduler's `TopologyAwareWorkloadScheduling` gate on does not affect pods that already
run; only pods created afterwards are evaluated.

The test edits the static pod manifest of the scheduler with `docker exec` on
`tas-lab-control-plane` (set `DOCKER` to use another container CLI) and waits for the scheduler to
come back, which takes about 40 seconds per switch. Its cleanup always turns the gate back **on**.

## The cases

| Case | Steps | Expected |
|---|---|---|
| [`gate-enabled-after`](gate-enabled-after) | gate OFF, run a 4-pod gang (one pod per node, tier-0 constraint); gate ON; delete and recreate the pods | OFF: all four Running, on both racks. ON: the existing pods keep running. Recreated: all four Pending. The gate is ON at the end |

## Run

```bash
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/03-gate-enabled-after
```

Run one thing at a time: the cases share the cluster and its node labels.
