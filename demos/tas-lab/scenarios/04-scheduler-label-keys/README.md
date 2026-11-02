# Scenario 04: Topograph labels as TopologyConstraint keys

Which Topograph labels work as the key of the kube-scheduler's `TopologyConstraint`. Each gang runs
one pod per node (hostname anti-affinity), so its size is the number of nodes it needs.
The scheduler's placement metrics are checked for `on-tier1`, `on-tier0`, `on-domain` and `on-subdomain`.

## The lab fabric (tests/models/tas-lab-multitier.yaml)

| Node | tier-0 (leaf) | tier-1 (spine) | accelerator domain |
|---|---|---|---|
| tas-lab-worker | leaf-1 | spine-1 | nvl-a |
| tas-lab-worker2 | leaf-2 | spine-1 | nvl-a |
| tas-lab-worker3 | leaf-3 | spine-2 | nvl-b |
| tas-lab-worker4 | leaf-4 | spine-2 | nvl-c |

The sub-domain cases use tas-lab-subdomain.yaml: one domain, a sub-domain per rack of two nodes.

## The cases

| Case | Key | Gang | Expected |
|---|---|---|---|
| [`on-tier1`](on-tier1) | tier-1 | 2 | Running in one spine; PodGroup scheduled |
| [`on-tier0`](on-tier0) | tier-0 | 2 | Not scheduled: a leaf is one node; Pending |
| [`on-domain`](on-domain) | domain | 2 | Running in nvl-a, inside one spine |
| [`tier1-too-big`](tier1-too-big) | tier-1 | 3 | Pending: a spine has two nodes |
| [`domain-too-big`](domain-too-big) | domain | 3 | Pending: the largest domain has two nodes |
| [`on-subdomain`](on-subdomain) | sub-domain | 2 | Running in one sub-domain |
| [`subdomain-too-big`](subdomain-too-big) | sub-domain | 3 | Pending: a sub-domain has two nodes |
| [`two-constraints`](two-constraints) | tier-1 and tier-0 | 2 | The API server rejects the PodGroup ("at most 1 item") |

## How a case is validated

- **Placed or not:** the PodGroup's `PodGroupInitiallyScheduled` condition is `True` or `False`.
  A case that must not place waits a few seconds after the decision and asserts the pods are still Pending.
- **Locality:** the pods' nodes carry the expected number of distinct values of the key.
- **Scheduler metrics:** the placements generated equal the distinct values of the key (2, 3), a multiple
  of 4 per attempt for tier-0, and only nvl-a is feasible for the domain. The templates
  [`scheduler-metrics-snapshot`](../templates/scheduler-metrics-snapshot.yaml) and
  [`scheduler-metrics-delta`](../templates/scheduler-metrics-delta.yaml) call `lib/capture-metrics.sh`.

## Run

```bash
demos/tas-lab/scenarios/run.sh demos/tas-lab/scenarios/04-scheduler-label-keys
```

Run one thing at a time: the cases share the cluster and its node labels.
