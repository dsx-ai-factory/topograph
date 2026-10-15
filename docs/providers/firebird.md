# Firebird Topology Provider

The `firebird` provider reads fabric topology from Firebird's
`GET /topology/nodes` endpoint and translates node network paths into the
canonical Topograph graph. It supports any fabric depth and does not discover
accelerator domains.

## Prerequisites

- A Firebird API reachable over HTTPS from the Topograph server, with a
  certificate trusted by the server’s system trust store.
- Requested instance IDs matching Firebird `nodeId` values. Supply the
  instance-to-hostname mapping in the request's `nodes` field; this provider
  does not implement Slurm automatic instance discovery or node-local metadata
  collection.

## Credentials

| Field | Required | Description |
|---|---|---|
| `clientId` | Yes | Firebird client identifier used for token authentication. |
| `clientSecret` | Yes | Firebird client secret used for token authentication. |

Supply credentials through Topograph's runtime credentials configuration or
`provider.creds`. The provider does not read environment variables directly.
Do not store working client secrets or tokens in repository config files.

## Parameters

| Field | Required | Description |
|---|---|---|
| `apiUrl` | Yes | Absolute HTTPS base URL, optionally including a base path. The provider appends `/auth/token` and `/topology/nodes`. Plain HTTP URLs, URL credentials, query strings, and fragments are rejected. |
| `trimTiers` | No | Number of outermost fabric tiers to remove, from 0 to 2. Default: 0. |

## Configuration

Example topology request using explicit node identities and the graph engine:

```json
{
  "provider": {
    "name": "firebird",
    "creds": {
      "clientId": "<CLIENT_ID>",
      "clientSecret": "<CLIENT_SECRET>"
    },
    "params": {
      "apiUrl": "https://firebird.example.com"
    }
  },
  "engine": { "name": "graph" },
  "nodes": [
    { "instances": { "node-id-1": "worker-1", "node-id-2": "worker-2" } }
  ]
}
```

Supply `clientId` and `clientSecret` through runtime credentials or
`provider.creds`, as illustrated with placeholders above. Other engines consume
the same canonical graph.

## How It Works

1. Authenticate using `POST /auth/token` with a JSON body containing `clientId`
   and `clientSecret`. Use the returned `accessToken` as a bearer token for
   topology requests.
2. Fetch `/topology/nodes` with `page_size` (default 100, overridden by Topograph's
   server `pageSize` setting). Non-positive sizes use the default.
3. Pass each non-empty `nextPageToken` back as the `page_token` query parameter.
   Continue even when an intermediate page is empty; stop when the token is
   absent or empty. Repeated continuation tokens cause an upstream error.
4. Match `nodeId` against the requested instance IDs. Regions do not scope the
   Firebird endpoint; the API base URL defines discovery scope.
5. Convert `networkPath` switch names to closest-first canonical fabric tiers.
   Leaf-first ordering preserves `["leaf", "spine", "core"]`.
   Paths contain switch names only, excluding
   the compute node itself. Use these names as canonical switch identifiers.
   The graph engine exports them as `network_layers`; the response
   `tiers` field is ignored.
6. Preserve requested nodes absent from the response or without a network path
   under Topograph's missing-topology branch. Duplicate requested node records
   and blank switch IDs cause an upstream error.

Authentication occurs before the first topology request. Access tokens are
cached per provider instance until shortly before `expiresIn` elapses, with
concurrent callers sharing a serialized token exchange. A zero or omitted
`expiresIn` disables token reuse. Token renewal repeats the client credentials
exchange; `idToken` and `refreshToken` are not retained. Only bearer token types
are supported; an omitted `tokenType` is treated as Bearer.

If a topology request returns HTTP 401, the provider invalidates the rejected
token, authenticates again, and retries that page once. Persistent HTTP 401
and HTTP 403 errors are returned to the caller. The simulation provider does
not authenticate or require credentials.

The `nodeKind`, `observedAt`, and `topologyStatus` fields are decoded but do not
affect graph construction. The provider does not infer accelerator domains,
status filtering, or freshness thresholds from them. Only requested
IDs with non-empty paths produce discovered topology.

Configuration errors return HTTP 400. Documented Firebird API errors preserve
these statuses:

| HTTP status | Meaning |
|---|---|
| 400 | Missing or malformed authentication fields, or invalid `node_kind`, `page_size`, or `page_token`. |
| 401 | Missing or invalid authentication token or rejected client credentials. |
| 403 | Insufficient permissions or unverified tenant. |
| 404 | `switch_id` not found. |
| 429 | Rate limit exceeded; try again later. |

The provider parses the `error` envelope and includes its `code`, `message`,
and string-valued `details` in the Topograph error message. It does not change
Topograph's response schema to the Firebird error envelope. Unstructured error
bodies are omitted. Other upstream failures and malformed success responses
return HTTP 502.

Requests use the shared HTTP retry policy, respect context cancellation, and
verify TLS certificates. HTTP 429 and retryable server errors receive up to five
attempts, honoring `Retry-After` or using exponential backoff. If rate limiting
persists after retries, the provider returns HTTP 429 with the final Firebird
error diagnostics. HTTP 400, 403, and 404 are not retried. Authentication HTTP 401 is not
retried; topology HTTP 401 triggers the single reauthentication attempt above.

## Verifying the Output

Save the example as `payload.json`, substitute your API URL and node IDs, then:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

Verify that `network_layers` lists the leaf switch first and the root last,
using the names from `networkPath` (for example, `sw.leaf-pod3-su1`,
`sw.spine-pod3`, `sw.fabric`).
For Kubernetes or Slurm output, verify the same hierarchy using the corresponding
[Kubernetes](../engines/k8s.md) or [Slurm](../engines/slurm.md) documentation.

## Simulation

`firebird-sim` generates Firebird-shaped node records from a Topograph YAML
model and runs them through the production pagination and topology conversion
logic. It requires no API URL, credentials, or network access.

| Parameter | Required | Description |
|---|---|---|
| `modelFileName` | Yes | Model basename from `tests/models/` (such as `small-tree.yaml`), or an absolute or relative file path. |
| `trimTiers` | No | Remove 0–2 outermost fabric tiers. Default: 0. |
| `api_error` | No | Set to `1` for a generic API failure (HTTP 502), or `400`, `404`, or `429` for the corresponding structured Firebird API error. Default: 0. |

Example request:

```json
{
  "provider": {
    "name": "firebird-sim",
    "params": { "modelFileName": "small-tree.yaml" }
  },
  "engine": { "name": "graph" },
  "nodes": [
    { "instances": { "I21": "worker-1", "I34": "worker-2" } }
  ]
}
```

Use model node names as instance IDs. Engines that support provider
`GetComputeInstances` can discover nodes directly from the model. Explicit
requests still filter the model and retain missing nodes under the
missing-topology branch.

Simulation records have `nodeKind: bm`, `topologyStatus: complete`, and switch
paths derived from model switch names. Nodes are paginated in
sorted model-node order, with continuation tokens and partial final pages.
Topograph's server `pageSize` setting controls the simulated page size too.
Simulated API errors return directly without HTTP requests or retry delays.
Model instance labels are preserved; accelerator annotations do not create
accelerator domains, matching the live Firebird provider's fabric-only behavior.

See [Modeling and API Simulation](../modeling.md) for the YAML model format.
