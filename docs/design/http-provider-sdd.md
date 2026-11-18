# Software Design Document: Generic HTTP API Provider

## Status

Proposed. The HTTP API provider configuration and interfaces below are not currently
supported features. Implementation follows review. The existing
`internal/httpauth` package supplies the shared authentication helpers described
below.

Feature issue: [dsx-ai-factory/topograph#697](https://github.com/dsx-ai-factory/topograph/issues/697).

## Problem and goals

Operators with an existing topology HTTP API currently need a dedicated provider
even when the differences are authentication, pagination, and JSON field names.
Add a provider named `http` that discovers fabric paths and optional accelerator
interconnect domains (for example, NVL partitions) from such APIs and feeds the existing canonical topology model.
The initial scope is one topology endpoint queried with GET and returning JSON,
with configurable authentication and pagination. The endpoint is configured as a
request block so that other methods and request bodies can be added later
without changing the configuration shape.

The provider must support:

- Pluggable authentication, including OAuth2 client credentials and custom
  login-token acquisition (`auth.bearer`), available through configuration.
- Unpaginated, page-number, offset/limit, and cursor-based APIs.
- A documented default JSON response and configurable field mappings.
- Explicit instance/node matching with deterministic ambiguity handling.
- Existing engines and graph contracts, with no changes to `pkg/topology/`.

Out of scope for the initial implementation: arbitrary endpoint orchestration,
dynamic Go plugin loading, external parser processes, scheduler-specific
discovery, and changes to node labels or identity annotations.

## Architecture

Implement `pkg/providers/http/` with a `NamedLoader` registered in
`pkg/registry/registry.go`. Extension factories stay local to this provider; the
shared provider interface is unchanged. Reuse `internal/httpauth` for authentication
mechanics. Its typed configuration and error semantics are the baseline below;
the HTTP API provider and its configuration decoding remain proposed. See
[shared HTTP authentication](../architecture.md#shared-http-authentication) for
the implemented helpers and existing-provider integrations.

```text
providers.Config + requested ComputeInstances
  -> validated HTTP API configuration
  -> request builder + authenticator
  -> HTTP response + pagination state
  -> default or field-mapping parser -> normalized records
  -> identity matching + topology validation
  -> ClusterTopology -> ToGraph -> existing engines
```

All pages are collected and validated before the graph is built; an upstream or
parsing failure never returns a partial graph.

## Default response and normalized records

```json
{
  "items": [
    {
      "instance_id": "instance-01",
      "region": "region-a",
      "network_path": ["leaf-1", "spine-1"],
      "accelerator_domain": "nvl-01"
    }
  ],
  "next_cursor": null
}
```

- `items`: required array (empty when there are no results).
- `instance_id`: required nonempty string.
- `network_path`: switch IDs only, closest to the compute node first, any depth.
  Absent or empty is valid for domain-only records.
- `accelerator_domain`: optional; when present, a nonempty string.
- Each matched record needs a nonempty `network_path` or an `accelerator_domain`.
- `region`: optional identity metadata, subject to the matching rules.
- `next_cursor`: used only in cursor mode.

Cluster node names are not part of the response: service providers identify
their instances and do not know Kubernetes or Slurm names.

The parser yields provider-local normalized records (identity, fabric IDs,
optional accelerator domain). After matching they map to existing fields:

| API record | Canonical topology |
|---|---|
| Matched requested instance ID | `InstanceTopology.InstanceID` |
| `network_path` | `FabricTiers` via `ClosestFirstFabricTiers` |
| `accelerator_domain` | `XclrDomainID` (empty when omitted) |

IDs must be strings; null or empty path elements are rejected. Numbers are not
coerced, paths are not flattened, and hierarchy is never inferred from names.

## Authentication

Use `internal/httpauth` for OAuth2 client credentials, custom bearer login, and
Basic/Bearer headers. Keep the authenticator interface, factory registry, and
`none`/`apiKey` handling local to the provider. Shared configuration and token
behavior are documented in
[shared HTTP authentication](../architecture.md#shared-http-authentication).

Authentication is configured in `provider.params.auth` (or `providerParams.auth`
in the config file). Token flows follow `httpauth.AuthConfig`:

| `auth.type` | Authentication configuration | Required credentials |
|---|---|---|
| `none` | none | none |
| `oauth2` | `oauth2.tokenUrl`; optional `oauth2.scopes`, `oauth2.authStyle` | `clientId`, `clientSecret` |
| `bearer` | `bearer.url`, `bearer.tokenPointer`; optional `bearer.method`, `bearer.encoding` | Nonempty login body with the exact field names required by the login API |
| `basic` | none | `username`, `password` |
| `apiKey` | `headerName`: HTTP header that carries the `apiKey` credential (e.g. `X-API-Key`) | `apiKey` |

An absent or empty `auth` map selects `none`; a nonempty map must set `type`.
Reject unknown fields and types, mismatched flow blocks, missing required fields,
and conflicting static authentication headers at load time. Custom Go factories
require rebuilding Topograph; runtime plugins are outside scope.

Credentials come from `providers.Config.Creds`: the server selects a nonempty
request `provider.creds` map when supplied, otherwise the `credentialsPath` YAML
file. These maps are not merged. Convert to `httpauth.Credentials`, rejecting
non-string values. Credentials stay separate from authentication parameters and
never appear in URLs or logs. For custom bearer login, the complete credentials
map becomes the JSON or form body, preserving its field names and string values.

Keep one OAuth2 token source per discovery invocation for reuse and expiry
renewal. Acquire a custom bearer token once per invocation and reuse it across
pages and retries, without inferring expiry or refresh behavior. Apply
authentication to every outgoing request, including retries; do not share tokens
across invocations. Transport authentication (mTLS) remains a follow-up scope
decision.

## Request construction and pagination

The topology request is described by one `request` block:

| Field | Meaning | Initial implementation |
|---|---|---|
| `url` | Absolute `https` URL of the topology endpoint, without query string | Required |
| `method` | HTTP method | Optional; only `GET` (the default) is accepted |
| `headers` | Static request headers | Optional |
| `query` | Static query parameters | Optional |
| `body` | Request body | Reserved; rejected when set |

A structured block, rather than a plain URL string, lets later versions add POST
requests with filter bodies, filtering by requested instance IDs, or additional
calls without changing the type of an existing field and breaking deployed
configurations. Until then, a non-`GET` method or a `body` fails at load time.

Static headers and query parameters must not carry credentials; credentials are
applied only by the authenticator. Reject static headers that conflict with the
authentication header and static query parameters that conflict with pagination
parameters. The inventory is fetched and matched to the requested instances
locally. Query values are encoded with URL APIs.

Use the existing `pageSize` setting, or the provider default when unset. `pageSize`
supplies the value; `pagination.sizeParam` only names the query parameter that
carries it. Parameter names are configurable; pagination counts all returned items
before node matching.

`pagination` holds only request-side settings. Everything extracted from a response,
including the next cursor, belongs to the parser (see
[Response customization](#response-customization)).

| Mode | Progression | Stop when |
|---|---|---|
| `none` | One GET | Response received |
| `page` | Start at page 0 or 1; increment by one | Empty page |
| `offset` | Start at configured offset; advance by returned item count | Empty page |
| `cursor` | Omit cursor initially; use the next cursor extracted by the parser (`next_cursor` for the default parser, `fields.nextCursor` for the mapping parser) | Cursor is missing, null or empty |

`maxPages` limits the number of topology pages fetched in one discovery invocation,
preventing unbounded pagination. Each successful page response counts, including
an empty terminal page; retries do not count as additional pages. If pagination
would require another page after the limit, discovery fails with HTTP 502 and
returns no partial graph. The default limit remains an open decision.

## Response customization

Two parsers turn response fields into topology:

1. **Default parser:** the response above. It reads `/items` and, in cursor mode,
   `/next_cursor`; neither is configurable.
2. **Field-mapping parser:** JSON Pointers select the items array, the fields
   (identity, region, path, accelerator domain) within each item, and, in cursor mode, the
   next cursor (`fields.nextCursor`, a response-level pointer). Paths are always
   closest-first. Pointers only extract; nothing is computed or restructured.

`fields.nextCursor` is required when `pagination.mode` is `cursor` with the mapping
parser, and is rejected with any other pagination mode or with the default parser.

Both parsers validate the response structure and field types. Normalized-record
validation checks identity and topology constraints before graph conversion.

## Instance and node matching

The provider matches API records to the requested instance IDs. The request
supplies the corresponding node names: `instance-01: worker-01` means an API
record for `instance-01` describes `worker-01`. The API does not need to know the
node name. `GenerateTopologyConfig` receives this mapping grouped by region in
`[]topology.ComputeInstances`.

- Match instance IDs exactly, including case. Field mappings may select an ID
  from a custom response field, but do not change its value.
- If a record includes a region, it must match the requested instance's region.
  Requested instance IDs must be unique across regions because the graph uses
  instance IDs alone as keys.
- Validate record identities before ignoring unrelated inventory. Fully validate
  matched records; reject ambiguous, conflicting or duplicate matches, including
  identical duplicates. Each requested node name must map to only one instance.

`missingNodes: error` (default) fails if any requested node has no matching record.
`missingNodes: warn` logs a count and a bounded list of missing nodes, and places
them under `NoTopology` in the graph. An empty requested set returns an empty graph
without upstream I/O.

Kubernetes uses the existing [instance-ID and region sources](../api.md#node-instance-id-and-region-sources).
Slurm requires explicit instance-to-node mappings; automatic identity discovery
is outside this proposal.

## Fabric and accelerator domain constraints

Upstream switch and accelerator domain IDs must identify the same resource wherever they
appear in the requested inventory. The provider preserves these IDs and validates
matched records together before graph conversion:

- Reject a switch or accelerator domain ID used in multiple regions, even with identical
  paths. Shared resources across regions are outside the initial scope.
- Every occurrence of a switch must have the same path toward the fabric root.
  For example, `[leaf-1, spine-a]` and `[leaf-1, spine-b]` conflict. Paths may have
  different lengths when their shared switches have consistent paths to the root.
- Reject repeated switch IDs within a path, switch IDs equal to requested
  instance IDs, and switch or instance IDs equal to the reserved `no-topology`
  value.

Within one region, nodes with the same accelerator domain ID belong
to the same domain. Domain and switch IDs may share a name because they are stored
separately. Conflicting topology records fail with 502 before graph conversion.

## Proposed configuration

Provider-specific names are illustrative and subject to review; the authentication
flow blocks follow `httpauth.AuthConfig`.

```yaml
provider: http
pageSize: 100
credentialsPath: /etc/topograph/credentials.yaml
providerParams:
  request:
    url: https://topology.example.invalid/v1/nodes
    method: GET              # only GET in the initial implementation
    headers:
      Accept: application/json
    query:
      status: active
  auth:
    type: oauth2
    oauth2:
      tokenUrl: https://identity.example.invalid/oauth/token
      scopes:
        - api.read
  pagination:
    mode: cursor
    sizeParam: limit
    cursorParam: cursor
    maxPages: 1000
  parsing:
    type: mapping            # or "default"
    itemsPointer: /items
    fields:
      identity: /instance_id
      region: /region
      networkPath: /network_path
      acceleratorDomain: /accelerator_domain
      nextCursor: /next_cursor   # cursor mode only
  missingNodes: error
```

Page mode adds `pageParam`, `initialPage` and a completion policy; offset mode
uses `offsetParam`, `initialOffset`. Credential values are not shown; the loader
receives them in `providers.Config.Creds` and no new top-level `creds` section
is added. Incompatible options, invalid pointers, unknown factories, conflicting
static/pagination parameters, an unsupported `request.method` or a
`request.body`, and missing credential fields fail at load time.

## HTTP behavior and errors

Use verified TLS, disable redirects, and bound response sizes and discovery time.
Each provider invocation has one deadline covering token acquisition and renewal,
pagination and retries;
all network calls and retry waits honor cancellation. Supply an explicit HTTP
client and use bounded, redacted HTTP execution. Logs and errors omit credentials,
query values and response bodies.

Retry transient transport errors and GET responses 429/502/503/504 with bounded
backoff, jitter and `Retry-After`. Return parsing, matching and authentication
failures without local retries. Custom login and OAuth2 token calls are not retried
locally. The existing server retry policy still applies to returned errors.
The shared HTTP retry policy differs from this proposal (it also retries 408 and
500 and has no jitter); the HTTP API provider must implement the policy above.
Response size violations are not retried.

Validate configuration in the loader before network I/O. Return `*httperr.Error`:

| Failure | Status |
|---|---|
| Invalid request input or provider configuration | 400 |
| Authentication endpoint HTTP failure | Preserve the shared helper's upstream 4xx/5xx status |
| Other upstream, protocol, parsing or matching failure; malformed token or rejected redirect | 502 |
| Invocation canceled | 408 |
| Upstream timeout or invocation deadline exceeded | 504 |

Return shared authentication errors directly, preserving their redacted messages
and statuses so the server can apply its existing retry policy.

## Verification

Test authentication configuration and credential validation, integration with
`internal/httpauth`, per-invocation token reuse and renewal, pagination modes and
limits, field mappings, matching and graph constraints, and both missing-node
policies. Include upstream failures, cancellation, timeouts, redaction, redirect
rejection, response limits, preserved authentication statuses, and existing server
retry behavior. Shared authentication mechanics are tested in `internal/httpauth`.

## Open decisions

- Set default page, record, response-size and discovery-time limits.
- Decide whether target APIs need POST requests with filter bodies initially;
  the `request` block reserves `method` and `body` for this.
- Decide whether header cursors are required initially. mTLS remains a follow-up
  scope decision.
