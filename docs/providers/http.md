# HTTP API Topology Provider

The `http` provider reads topology from any HTTPS endpoint that returns JSON
inventory records. Authentication, pagination, and response field names are
configured rather than coded, so a cloud or on-premises inventory service can
feed Topograph without a dedicated provider. Each record describes one compute
instance: its fabric path (switch IDs, closest to the node first) and an
optional accelerator domain such as an NVLink partition.

If you operate a cloud and want Topograph to support it, start with the
[onboarding guide](../get-started/onboard-http-provider.md). This page is the
configuration reference.

## Prerequisites

- An HTTPS topology endpoint reachable from the Topograph server, with a
  certificate trusted by the server's system trust store. Plain HTTP is rejected.
- Instance IDs in the API response that match the instance IDs Topograph is
  asked about:
  - Kubernetes: read from a node label or annotation through
    [`nodeMetadata`](../api.md#node-instance-id-and-region-sources). The Node Data
    Broker does not support this provider; disable it (see
    [Kubernetes](#kubernetes)).
  - Slurm and direct API calls: supply the instance-to-node mapping in the
    request's `nodes` field.

## Credentials

Credentials come from Topograph's runtime credentials file (`credentialsPath`,
or `config.credentialsSecret` in Helm) or from `provider.creds` in a topology
request. When a request supplies a nonempty `provider.creds`, it replaces the
file; the two are not merged. All values must be strings.

| `auth.type` | Required credential keys |
|---|---|
| `none` | none |
| `basic` | `username`, `password` |
| `apiKey` | `apiKey` |
| `oauth2` | `clientId`, `clientSecret` |
| `bearer` | Any keys. The whole map is sent as the login request body, with field names unchanged. |

Credentials are never placed in URLs, static headers, or logs. Do not put
working secrets in repository files.

## Parameters

Parameters go in `provider.params` (Helm and API requests) or `providerParams`
(Topograph config file). Unknown fields are rejected at load time, so a typo or
a credential placed in the wrong block fails with HTTP 400 before any network
call.

### Top level

| Field | Required | Default | Description |
|---|---|---|---|
| `request` | Yes | | Topology endpoint; see [Request](#request). |
| `auth` | No | `none` | Authentication; see [Authentication](#authentication). |
| `pagination` | No | `mode: none` | See [Pagination](#pagination). |
| `parsing` | No | `type: default` | See [Response format](#response-format). |
| `missingNodes` | No | `error` | `error` fails when a requested node has no record. `warn` logs up to 10 missing node names and places them under the `no-topology` branch. |
| `trimTiers` | No | `0` | Remove 0 to 2 outermost fabric tiers. |
| `timeout` | No | `5m` | Deadline for one discovery, covering login, every page, and retries. Go duration syntax. |
| `maxResponseBytes` | No | `33554432` (32 MiB) | Size limit for one page. A larger response fails with HTTP 502 and is not retried. |

Topograph's server-level `pageSize` setting sets the page size (default 100).

### Request

| Field | Required | Description |
|---|---|---|
| `request.url` | Yes | Absolute `https` URL of the topology endpoint, with no query string, fragment, or user information. |
| `request.method` | No | Only `GET`, the default, is accepted. Reserved for future use. |
| `request.headers` | No | Static headers. `Accept: application/json` is added when absent. `Authorization`, `Proxy-Authorization`, `Host`, `Content-Length`, and the API-key header are rejected. |
| `request.query` | No | Static query parameters, such as `status: active`. Names used by pagination are rejected. |
| `request.body` | No | Reserved; rejected when set. |

### Authentication

| `auth.type` | Settings | Behavior |
|---|---|---|
| `none` | | No authentication header. |
| `basic` | | `Authorization: Basic …` |
| `apiKey` | `headerName` (required), for example `X-API-Key` | The `apiKey` credential is sent in that header. |
| `oauth2` | `oauth2.tokenUrl` (required, HTTPS); `oauth2.scopes`; `oauth2.authStyle`: `basic` (default) or `form` | OAuth2 client credentials grant. The token is reused across pages and retries in one discovery and renewed before it expires. |
| `bearer` | `bearer.url` (required, HTTPS); `bearer.tokenPointer` (required JSON Pointer to the token in the login response, such as `/access_token`); `bearer.method`: `POST` (default) or `PUT`; `bearer.encoding`: `json` (default) or `form` | Custom login. The token is acquired once per discovery and reused for every page and retry, with no expiry handling. |

A nonempty `auth` block must set `type`. A flow block that does not match the
type (for example `oauth2` with `type: basic`) is rejected. Tokens are never
shared between discoveries.

### Pagination

| Field | Modes | Default | Description |
|---|---|---|---|
| `mode` | | `none` | `none`, `page`, `offset`, or `cursor`. |
| `sizeParam` | page, offset, cursor | not sent | Query parameter that carries the page size. |
| `pageParam` | page | `page` | Query parameter that carries the page number. |
| `initialPage` | page | `1` | First page number: `0` or `1`. |
| `offsetParam` | offset | `offset` | Query parameter that carries the item offset. |
| `initialOffset` | offset | `0` | First offset. The offset advances by the number of items returned. |
| `cursorParam` | cursor | `cursor` | Query parameter that carries the cursor. It is omitted on the first request. |
| `completion` | page, offset | `empty` | `empty` stops at the first page with no items. `short` also stops at a page with fewer items than the page size, and requires `sizeParam`. |
| `maxPages` | page, offset, cursor | `1000` | Most pages fetched in one discovery, including the empty final page. Needing more fails with HTTP 502. |

| Mode | Stops when |
|---|---|
| `none` | After one request. |
| `page`, `offset` | A page has no items (or, with `completion: short`, fewer items than the page size). |
| `cursor` | The next cursor is missing, `null`, or `""`. A repeated cursor fails with HTTP 502. |

Settings that do not apply to the selected mode are rejected.

### Response format

#### Default parser

With `parsing` omitted, the response must look like this:

```json
{
  "items": [
    {
      "instance_id": "instance-01",
      "region": "region-a",
      "network_path": ["leaf-1", "spine-1", "core-1"],
      "accelerator_domain": "nvl-01"
    }
  ],
  "next_cursor": "opaque-token"
}
```

| Field | Required | Description |
|---|---|---|
| `items` | Yes | Array of records; empty when there are no results. |
| `instance_id` | Yes | Nonempty string. Matched exactly, including case. |
| `network_path` | No | Switch IDs, closest to the compute node first, any depth. Do not include the node itself. |
| `accelerator_domain` | No | Accelerator interconnect domain, such as an NVLink partition ID. |
| `region` | No | When present, must equal the region of the requested instance. |
| `next_cursor` | Cursor mode | Cursor for the next page. Ignored in other modes. |

Each matched record needs a nonempty `network_path`, an `accelerator_domain`,
or both. All IDs must be JSON strings; numbers are not converted.

#### Field-mapping parser

Set `parsing.type: mapping` to read an existing response shape with
[JSON Pointers](https://www.rfc-editor.org/rfc/rfc6901):

| Field | Required | Description |
|---|---|---|
| `itemsPointer` | Yes | Pointer to the items array. Use `""` when the response is a top-level array. |
| `fields.identity` | Yes | Pointer, relative to each item, to the instance ID. |
| `fields.networkPath` | One of these two | Pointer to the closest-first array of switch IDs. |
| `fields.acceleratorDomain` | One of these two | Pointer to the accelerator domain ID. |
| `fields.region` | No | Pointer to the region. |
| `fields.nextCursor` | Cursor mode only | Pointer, relative to the whole response, to the next cursor. |

Pointers only select values; they do not compute, join, reverse, or flatten
anything. If your API lists switches root first, or does not have the data in
one field, adapt the API (see the onboarding guide).

## Validation

Records for instances that were not requested are ignored, but every record must
have a nonempty string identity. Matched records are validated together, and
any violation fails the whole discovery with HTTP 502:

- An instance appears more than once (even identically), or its region does not
  match the requested region.
- A switch has different parents in different paths. `[leaf-1, spine-a]` and
  `[leaf-1, spine-b]` conflict, and so do `[leaf-1]` and `[leaf-1, spine-a]`.
  Paths may differ in length when shared switches agree on the path to the root,
  for example `[leaf-1, spine-1, core-1]` and `[spine-1, core-1]`.
- A path repeats a switch, uses a requested instance ID as a switch ID, or uses
  the reserved ID `no-topology`.
- A switch ID or accelerator domain ID appears in more than one region.

In the request, instance IDs must be unique across regions, and each node name
must map to one instance (HTTP 400 otherwise). An empty request returns an empty
graph without calling the API.

## Errors and retries

| Failure | HTTP status |
|---|---|
| Invalid parameters, credentials, or request | 400 |
| Token or login endpoint failure | The status returned by the identity endpoint |
| Topology endpoint failure, redirect, oversize response, invalid JSON, validation, or missing nodes | 502 |
| Discovery canceled | 408 |
| `timeout` exceeded | 504 |

The topology request is retried up to five times on transport errors and on
HTTP 429, 502, 503, and 504, with exponential backoff and jitter starting at
500 ms, or the `Retry-After` delay when present (capped at five minutes). Other
statuses are not retried. Token and login requests are not retried by the
provider. Redirects are never followed, so credentials are sent only to the
configured hosts. Errors and logs do not include URLs, query values, response
bodies, or credentials.

The whole inventory is collected and validated before the graph is built. A
failure never produces a partial topology.

## Configuration examples

### Topograph config file

```yaml
provider: http
engine: slurm
pageSize: 200
credentialsPath: /etc/topograph/credentials.yaml
providerParams:
  request:
    url: https://topology.example.com/v1/nodes
    query:
      status: active
  auth:
    type: oauth2
    oauth2:
      tokenUrl: https://identity.example.com/oauth/token
      scopes: [topology.read]
  pagination:
    mode: cursor
    sizeParam: limit
```

`/etc/topograph/credentials.yaml` holds `clientId` and `clientSecret`.

### Kubernetes

The Node Data Broker cannot discover instance IDs for this provider, so read
them from an existing node label or annotation and disable the broker:

```yaml
provider:
  name: http
  params:
    request:
      url: https://topology.example.com/v1/nodes
    auth:
      type: apiKey
      headerName: X-API-Key
    pagination:
      mode: page
      sizeParam: per_page
engine:
  name: k8s
config:
  credentialsSecret: topograph-http-credentials
  nodeMetadata:
    instanceID:
      type: label
      key: example.com/instance-id
    region:
      type: label
      key: topology.kubernetes.io/region
nodeDataBroker:
  enabled: false
```

Create the secret from a `credentials.yaml` file holding `apiKey`:

```bash
kubectl create secret generic topograph-http-credentials --from-file=credentials.yaml=<path>
```

### Direct request

```json
{
  "provider": {
    "name": "http",
    "creds": { "username": "<USERNAME>", "password": "<PASSWORD>" },
    "params": {
      "request": { "url": "https://topology.example.com/v1/nodes" },
      "auth": { "type": "basic" }
    }
  },
  "engine": { "name": "graph" },
  "nodes": [
    { "region": "region-a", "instances": { "instance-01": "worker-01", "instance-02": "worker-02" } }
  ]
}
```

## Verifying the output

Save a request as `payload.json`, then:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

Repeat the second request while it returns HTTP 202. With the `graph` engine,
check that each instance's `network_layers` lists its switches closest first and
that instances sharing an accelerator domain have the same
`accelerator.topograph.run/domain` value in `labels`. Run Topograph
with `-v 4` to log the item count of each page.
