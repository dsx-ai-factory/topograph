# DSX Topology Provider

The `dsx` topology provider reads topology data from the **DSX Topology API** and converts it into Topograph's canonical topology graph.

The provider calls `GET /v1/topology/vpcs/{vpc_id}/nodes`, which returns an ordered list of switch adjacency entries. Each entry maps a switch name to the set of downstream switches and compute nodes it serves. From this it builds a switch tree (for Slurm `topology/tree` or Kubernetes labels) and, when NVLink domain IDs are present, an accelerator domain map (for `topology/block`).

The caller's org must own the VPC; otherwise the API returns `403`. The `Region` field on each node group in the request is not used.

With the Slurm engine, the provider takes the Slurm node names as the DSX node IDs, so a request without a `nodes` map works when the Slurm hostnames equal the DSX `node_id` values. Otherwise supply the `nodes` map (DSX node ID to node name) in the request, as in the example request payload below.

Authentication and rate limiting are enforced by the Envoy proxy sidecar before requests reach this service. Topograph authenticates with a Bearer token, either a static `token` or one it requests with OAuth2 client credentials. When no credentials are configured, no `Authorization` header is sent and the Envoy sidecar is expected to supply SVID-based authentication transparently for in-cluster callers.

## When to Use This Provider

Use this provider for **DSX clusters** where the DSX Topology API is the topology source. It works with both the Slurm engine (generating `topology.conf`) and the Kubernetes engine (labeling nodes).

## Prerequisites

- The DSX Topology API endpoint reachable from the Topograph host (`base_url`)
- The VPC ID (`vpc_id`) whose topology to read
- One of: the caller's SVID injected by Envoy (in-cluster, zero-config), a static Bearer token, or OAuth2 client credentials with permission to read topology

## Credentials

| Field | Required | Description |
|---|---|---|
| `token` | No | Static Bearer token sent as `Authorization: Bearer <token>`. Omit when running in-cluster and relying on the Envoy sidecar for SVID-based authentication |
| `client_id` | No | OAuth2 client ID for the client-credentials grant (SSA). Requires `client_secret`; mutually exclusive with `token` |
| `client_secret` | No | OAuth2 client secret. Requires `client_id` |
| `issuer_url` | With `client_id`, unless `token_url` is set | OIDC issuer (must be `https`); the token endpoint is read from `<issuer_url>/.well-known/openid-configuration` |
| `token_url` | No | Token endpoint (must be `https`). When set, OIDC discovery is skipped and `issuer_url` is not needed |
| `scopes` | No | OAuth2 scopes to request, as a list or a space-separated string, for example `[nvll_operator_user_ro]` |

With `client_id` and `client_secret`, Topograph requests an access token from the token endpoint, caches it until it expires, and sends it as the Bearer token. If `token_url` is not set, the token endpoint is discovered from `issuer_url`. `token` and the client credentials cannot be combined. Supply `client_secret` through the credentials file or the request payload, never in committed files. Redirects from `https` to `http` are refused, so credentials are never sent in cleartext.

Store credentials in a YAML file when needed, for a static token:

```yaml
token: <API_TOKEN>
```

Reference that file from the Topograph config:

```yaml
credentialsPath: /etc/topograph/dsx-credentials.yaml
```

Credentials can also be supplied directly in the topology request payload under `provider.creds`.

When a request supplies no `provider.creds`, Topograph uses the credentials from `credentialsPath`. To keep those credentials from being sent to an endpoint the operator did not choose, such a request must use the same `base_url` as the server's `providerParams`, or the provider returns `403`. A request that supplies its own credentials may use any `base_url`, and other parameters such as `vpc_id` may differ either way.

## Parameters

| Field | Required | Description |
|---|---|---|
| `base_url` | Yes | Base URL for the DSX Topology API, for example `https://topology.example.com`. Must be `http` or `https`; `https` is required when a token or client credentials are configured. If a gateway serves the API under a path prefix, include it, for example `https://gateway.example.com/topology`; the request path `/v1/topology/vpcs/{vpc_id}/nodes` is appended to it |
| `vpc_id` | Yes | VPC whose nodes are queried, sent as `GET /v1/topology/vpcs/{vpc_id}/nodes`. Letters, digits, `.`, `_` and `-` only |
| `trimTiers` | No | Number of highest topology tiers to trim from output. Defaults to `0` |

The top-level Topograph `pageSize` setting controls the `page_size` query parameter. Values outside the API range of 100–1000 are clamped; when unset, the API default (100) applies.

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
  vpc_id: <VPC_ID>

engineParams:
  plugin: topology/tree
  topologyConfigPath: /etc/slurm/topology.conf
```

Example credentials file for OAuth2 client credentials (for example SSA):

```yaml
client_id: <CLIENT_ID>
client_secret: <CLIENT_SECRET>
issuer_url: https://<ssa-issuer-host>
scopes: [nvll_operator_user_ro]
```

The provider parameters are the same as above. With the Slurm engine, the node names are used as the DSX node IDs unless the request supplies a `nodes` map.

Example request payload:

```json
{
  "provider": {
    "name": "dsx",
    "creds": {
      "token": "<API_TOKEN>"
    },
    "params": {
      "base_url": "https://topology.example.com",
      "vpc_id": "<VPC_ID>"
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

The provider collects the node IDs from all node groups in the request. It does not send a node filter, because a long `node_ids` list would not fit in a URL for large clusters. Instead it pages through the VPC's topology until the response carries an empty `next_page_token`, then keeps only the requested nodes. Requested nodes that the API does not return are logged as a warning.

Each page is requested as:

```text
GET <base_url>/v1/topology/vpcs/<vpc_id>/nodes?page_size=<pageSize>&page_token=<cursor>
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

## Error Handling

A `4xx` response from the DSX API (for example `403` when the caller does not own the VPC, or `404` for an unknown VPC) is returned with the same status code, except `401`. A `401` means DSX rejected Topograph's own credentials, so it is returned as `502`, like other upstream failures and token-request failures. If generation exceeds its 10-minute total deadline, the provider returns `504`. It returns `422` if the request context is cancelled, the API repeats a page token, or pagination exceeds 10,000 pages.

## Verifying the Output

Sanity-check the API directly (in-cluster, Envoy supplies auth):

```bash
curl -s "$BASE_URL/v1/topology/vpcs/$VPC_ID/nodes?page_size=100" | jq .
```

Or with an explicit token:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "$BASE_URL/v1/topology/vpcs/$VPC_ID/nodes?page_size=100" | jq .
```

Then trigger topology generation and read the result:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

For the Slurm engine, verify the generated `topology.conf` reflects the expected switch hierarchy for your nodes.

## Simulation

A `dsx-sim` provider variant is registered for testing without a live API. Instead of calling the topology API, it reads a YAML simulation model and serves it through the same translation path. Select it with `provider: dsx-sim` and point it at a model file via the `modelFileName` parameter; see [Test Mode and Test Provider](./test.md) for the model-file format and simulation parameters.
