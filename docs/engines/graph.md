# Topograph Graph Engine

The `graph` engine returns instance-oriented topology as JSON: for each compute instance, the switches on its path through the fabric and, when the provider discovers them, its accelerator domain labels. It is intended for clients that need per-instance placement context rather than scheduler-specific output such as `topology.conf`, Kubernetes node labels, or a Slinky ConfigMap.

The engine preserves the provider/engine boundary: providers discover topology and carry it on the canonical topology graph, and the `graph` engine only formats it. It works with every provider. If a provider attaches per-instance records to the graph (the `test` provider and other model-backed simulation providers do, carrying the labels defined in the model), the engine uses them and fills in only what is missing: `network_layers` and the accelerator domain labels, taken from the graph's fabric tiers and domains. Values the provider already set are kept. Otherwise it derives each instance from the graph's fabric tiers and accelerator domains.

## Output

By default, the generated JSON is returned in the `/v1/topology` response.

Each instance entry contains:
- `id` — the provider instance ID (for `infiniband`, `infiniband-sim` and `netq`, the host or node identifier the provider reports)
- `network_layers` — switch names, or IDs when a switch has no name, in closest-first order (tier 0 is the switch directly connected to the node, increasing outward); the depth is variable and provider-dependent. A node that the provider placed in its graph without fabric topology has an empty list. A requested node the provider did not return at all (for example, a host missing from an `infiniband-sim` capture) is omitted, so do not assume one entry per requested node.
- `labels` — optional per-instance labels. Providers that discover accelerator domains add `accelerator.topograph.run/domain`, and `accelerator.topograph.run/sub-domain` when the provider reports one. Model-backed simulation providers also include the labels defined in the model (for example, `topology.kubernetes.io/region`). The field is omitted when there are no labels.

Entries are sorted by `id`.

### How entries are built

- If the provider attached per-instance records, they are returned. Any `network_layers` or accelerator domain labels a record lacks are filled in from the graph; values the provider set are never overwritten. The sub-domain label is added only when the record's domain matches the graph's.
- Otherwise each entry is derived from the graph. Every requested compute node in the fabric tree becomes an entry whose `network_layers` are its ancestor switches, closest first. Nodes that were not requested are never reported, so a request for unknown nodes returns an empty `instances` list. Nodes under Topograph's internal no-topology placeholder get an empty list.
- An accelerator domain host that is not in the fabric tree is reported with an empty `network_layers`, but only if it was requested. Providers that list every node in an NVLink domain therefore do not add unrelated hosts to the output.
- If a host appears in several domains, the first domain in sorted order is used. If a node ID repeats in the fabric tree, the first path in sorted order is used.

## Request

The engine needs the instance IDs to export. Supply `nodes` in the request, or use a provider that can supply compute instances directly.

Set `engine.params.topologyConfigPath` to write the JSON to an existing validated path on the Topograph host. When it is set, the HTTP result body is `OK` and the JSON is in the file.

## Examples

### Live or replayed fabric (`infiniband-sim`)

The `infiniband-sim` provider replays a saved `ibnetdiscover` capture, so this request runs without any fabric access. Use an absolute path for `ibnetdiscoverFileName` when the server runs from a different directory.

```json
{
  "provider": {
    "name": "infiniband-sim",
    "params": {
      "ibnetdiscoverFileName": "tests/output/ibnetdiscover/example.out"
    }
  },
  "engine": { "name": "graph" },
  "nodes": [
    {
      "region": "local",
      "instances": {
        "b07-p1-dgx-07-c01": "b07-p1-dgx-07-c01"
      }
    }
  ]
}
```

Response from `GET /v1/topology?uid=<request-id>`:

```json
{
  "instances": [
    {
      "id": "b07-p1-dgx-07-c01",
      "network_layers": [
        "S-2c5eab0300c26140",
        "S-2c5eab0300c25f00",
        "S-2c5eab0300b879c0"
      ]
    }
  ]
}
```

This provider simulates fabric tiers only, so there are no accelerator domain labels. With a provider that discovers accelerator domains (for example `aws`, `oci`, or `infiniband` on NVLink hosts), each entry also carries `accelerator.topograph.run/domain` under `labels`.

### Model-backed simulation (`test`)

The `test` provider builds the topology from a YAML model in `tests/models/`. The `nvl72.yaml` model defines region and zone labels and accelerator domains. The domain label is filled in from the graph:

```json
{
  "provider": {
    "name": "test",
    "params": {
      "modelFileName": "nvl72.yaml"
    }
  },
  "engine": { "name": "graph" }
}
```

The response contains one entry per node (72 for this model). The first two are:

```json
{
  "instances": [
    {
      "id": "i-node1101",
      "network_layers": ["leaf-1-1", "spine-1", "core"],
      "labels": {
        "accelerator.topograph.run/domain": "nvl-1-1",
        "topology.kubernetes.io/region": "us-east",
        "topology.kubernetes.io/zone": "zone1"
      }
    },
    {
      "id": "i-node1102",
      "network_layers": ["leaf-1-1", "spine-1", "core"],
      "labels": {
        "accelerator.topograph.run/domain": "nvl-1-1",
        "topology.kubernetes.io/region": "us-east",
        "topology.kubernetes.io/zone": "zone1"
      }
    }
  ]
}
```

Instance IDs in the `test` provider are the model's node names prefixed with `i-`. Models that define no labels still get `topology.kubernetes.io/region: "none"`.

### Writing to a file

```json
{
  "provider": {
    "name": "test",
    "params": {
      "modelFileName": "small-tree.yaml"
    }
  },
  "engine": {
    "name": "graph",
    "params": {
      "topologyConfigPath": "topology.json"
    }
  }
}
```

The HTTP response body is `OK`, and `topology.json` contains the same kind of document as above, shown here truncated to the first entry:

```json
{"instances":[{"id":"i-I21","network_layers":["S2","S1"],"labels":{"accelerator.topograph.run/domain":"nvl2","topology.kubernetes.io/region":"none"}}]}
```
