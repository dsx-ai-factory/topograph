# Scenario 01: a gang inside one rack

The kube-scheduler (Kubernetes v1.37, `TopologyAwareWorkloadScheduling`) places a gang inside one
domain of the node label named by its PodGroup's `schedulingConstraints.topology`. These tests use
`fabric.topograph.run/tier-0` (the rack Topograph writes).
The placement and feasibility counts the scheduler reports are checked as well.

## The lab fabric (tests/models/tas-lab.yaml)

Four workers, two per rack: `tas-lab-worker` and `tas-lab-worker2` in rack-a, `tas-lab-worker3` and
`tas-lab-worker4` in rack-b, under one spine.

## The cases

| Case | Gang | Expected |
|---|---|---|
| [`gang-in-one-rack`](gang-in-one-rack) | 4 pods, tier-0 constraint | All four Running, in one tier-0 value |
| [`gang-with-plain-pod`](gang-with-plain-pod) | the same, plus a pod with no schedulingGroup | Five pods Running; the gang pods still in one rack |

Each case's `description` states it as Given, When and Then.

## How a case is validated

- **Running:** the pods whose names start with the Job or pod name are in phase `Running`.
- **Locality:** their nodes carry one distinct `tier-0` value.
- **Scheduler metrics:** the placements the scheduler generated (one per `tier-0` value, so 2) and the feasible
  evaluations (2) changed by the expected amount since a baseline taken before the gang was submitted; the
  plain pod adds no placement.

## Run

```bash
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/01-gang-rack
```

Run one thing at a time: the cases share the cluster and its node labels.
