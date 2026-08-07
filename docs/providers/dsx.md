# DSX Topology Provider

The `dsx` topology provider reads topology data from the **DSX Topology API** and converts it into Topograph's canonical topology graph.

The provider calls `GET /v1/topology/nodes`, which returns an ordered list of switch adjacency entries. Each entry maps a switch name to the set of downstream switches and compute nodes it serves. From this it builds a switch tree (for Slurm `topology/tree` or Kubernetes labels) and, when NVLink domain IDs are present, an accelerator domain map (for `topology/block`).

All `topology.ComputeInstances` groups in the request are aggregated into a single node-ID list sent to this endpoint. The `Region` field on each group and any VPC ID are not used — the DSX service infers the scope from the caller's identity.

Authentication and rate limiting are enforced by the Envoy proxy sidecar before requests reach this service. Topograph sends an optional `Authorization: Bearer <token>` header; when no token is configured, the Envoy sidecar is expected to supply SVID-based authentication transparently for in-cluster callers.

## When to Use This Provider

Use this provider for **DSX clusters** where the DSX Topology API is the topology source. It works with both the Slurm engine (generating `topology.conf`) and the Kubernetes engine (labeling nodes).

The request's `nodes` list maps provider node IDs to hostnames. All entries across every group are merged into a single paginated sequence of API calls against `GET /v1/topology/nodes`.

## Prerequisites

- The DSX Topology API endpoint reachable from the Topograph host (`base_url`)
- The caller's SVID injected by Envoy (in-cluster, zero-config), **or** a Bearer token with permission to read topology

## Credentials

| Field | Required | Description |
|---|---|---|
| `token` | No | Bearer token sent as `Authorization: Bearer <token>`. Omit when running in-cluster and relying on the Envoy sidecar for SVID-based authentication |

Store the token in a YAML file when needed:

```yaml
token: <API_TOKEN>
```

Reference that file from the Topograph config:

```yaml
credentialsPath: /etc/topograph/dsx-credentials.yaml
```

Credentials can also be supplied directly in the topology request payload under `provider.creds`.

## Parameters

| Field | Required | Description |
|---|---|---|
| `base_url` | Yes | Base URL for the DSX Topology API, for example `https://topology.example.com` |
| `trimTiers` | No | Number of highest topology tiers to trim from output. Defaults to `0` |

The top-level Topograph `pageSize` setting controls the `page_size` query parameter for paginated topology requests (default 100, max 1000 per the API). The `region` field on each `nodes` entry is not used by this provider — the DSX service infers the scope from the caller's identity.

## Configuration

Example Topograph config for Slurm:

```yaml
http:
  port: 49021
  ssl: false

provider: dsx
engine: slurm

requestAggregationDelay: 15s
credentialsPath: /etc/topograph/dsx-credentials.yaml

providerParams:
  base_url: https://topology.example.com

engineParams:
  plugin: topology/tree
  topologyConfigPath: /etc/slurm/topology.conf
```

Example request payload:

```json
{
  "provider": {
    "name": "dsx",
    "creds": {
      "token": "<API_TOKEN>"
    },
    "params": {
      "base_url": "https://topology.example.com"
    }
  },
  "engine": {
    "name": "slurm",
    "params": {
      "plugin": "topology/tree"
    }
  },
  "nodes": [
    {
      "region": "",
      "instances": {
        "<NODE_ID_1>": "node001",
        "<NODE_ID_2>": "node002"
      }
    }
  ]
}
```

When running in-cluster without a token, omit the `creds` field entirely — the Envoy sidecar supplies SVID authentication.

## How It Works

The provider aggregates node IDs from all `topology.ComputeInstances` groups in the request (the `Region` field on each group is ignored) and sends them as the `node_ids` comma-separated query parameter. It pages through the single global endpoint until the response carries an empty `next_page_token`:

```text
GET <base_url>/v1/topology/nodes?node_ids=<id1>,<id2>&page_size=<pageSize>
Authorization: Bearer <token>          # omitted when using Envoy SVID
```

The response envelope:

```json
{
  "switches": [
    { "<switch-name>": { "switches": ["<child-switch>"], "nodes": [] } },
    { "<leaf-name>":   { "switches": [],                 "nodes": [{ "node_id": "<id>", "accelerated_network_id": "<domain>" }] } }
  ],
  "next_page_token": "<cursor-or-empty>"
}
```

`switches` is an **ordered list of single-key objects**. Non-leaf entries carry `switches` (their downstream switches); leaf entries carry `nodes` (the compute nodes attached to them). This ordering reflects the fabric hierarchy from core to leaf.

Switch entries from every page are accumulated before any graph is built. Only after the final page (empty `next_page_token`) are parent-child relationships resolved and instance topologies emitted. This ensures cross-page ancestry is correct — for example, a spine switch returned on page 1 is correctly recognised as the parent of a leaf switch returned on page 2.

Each node is translated as follows:

| API field | Topograph field |
|---|---|
| `node_id` | Instance ID (matched against the request's instance-to-hostname map) |
| Switch that lists the node under its `nodes` | Leaf (tier 0, closest to node) |
| Parent of the leaf (from `switches` adjacency) | Spine (tier 1) |
| Parent of the spine | Core (tier 2) |
| `accelerated_network_id` | Accelerator / NVLink domain (`XclrDomainID`) |

Tier assignment is closest-first: tier 0 is the leaf switch directly attached to the node, tier 1 is the spine, and tier 2 is the core. When `accelerated_network_id` is non-empty the node is placed into that NVLink domain, enabling `topology/block` output.

## Verifying the Output

Sanity-check the API directly (in-cluster, Envoy supplies auth):

```bash
curl -s "$BASE_URL/v1/topology/nodes?node_ids=node1,node2" | jq .
```

Or with an explicit token:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "$BASE_URL/v1/topology/nodes?node_ids=node1,node2" | jq .
```

Then trigger topology generation and read the result:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

For the Slurm engine, verify the generated `topology.conf` reflects the expected switch hierarchy for your nodes.

## Simulation

A `dsx-sim` provider variant is registered for testing without a live API. Instead of calling the topology API, it reads a YAML simulation model and serves it through the same translation path. Select it with `provider: dsx-sim` and point it at a model file via the `modelFileName` parameter; see [Test Mode and Test Provider](./test.md) for the model-file format and simulation parameters.
