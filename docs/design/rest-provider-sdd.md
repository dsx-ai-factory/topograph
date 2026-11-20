<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Software Design Document: Generic REST API Provider

## Status

Proposed. Configuration and interfaces below are proposals, not currently
supported features. Implementation follows review.

Feature issue: [dsx-ai-factory/topograph#697](https://github.com/dsx-ai-factory/topograph/issues/697).

## Problem and goals

Operators with an existing topology REST API currently need a dedicated provider
even when the differences are authentication, pagination, and JSON field names.
Add a provider named `rest` that discovers fabric paths and optional NVL
partitions from such APIs and feeds the existing canonical topology model.

The provider must support:

- Pluggable authentication, including OAuth2 client credentials and custom
  login-token acquisition (`tokenRequest`), available through configuration.
- Unpaginated, page-number, offset/limit, and cursor-based APIs.
- A documented default JSON response and configurable field mappings.
- Explicit instance/node matching with deterministic ambiguity handling.
- Existing engines and graph contracts, with no changes to `pkg/topology/`.

Out of scope for the initial implementation: arbitrary endpoint orchestration,
dynamic Go plugin loading, external parser processes, scheduler-specific
discovery, and changes to node labels or identity annotations.

## Architecture

Implement `pkg/providers/rest/` with a `NamedLoader` registered in
`pkg/registry/registry.go`. Extension factories stay local to this provider; the
shared provider interface is unchanged. Shared authentication helpers are tracked
separately in [#699](https://github.com/dsx-ai-factory/topograph/issues/699); this
proposal does not prescribe their implementation or existing-provider migrations.

```text
providers.Config + requested ComputeInstances
  -> validated REST configuration
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
      "nvl_partition": "nvl-01"
    }
  ],
  "next_cursor": null
}
```

- `items`: required array (empty when there are no results).
- `instance_id`: required nonempty string.
- `network_path`: switch IDs only, closest to the compute node first, any depth.
  Absent or empty is valid for partition-only records.
- `nvl_partition`: optional; when present, a nonempty string.
- Each matched record needs a nonempty `network_path` or an `nvl_partition`.
- `region`: optional identity metadata, subject to the matching rules.
- `next_cursor`: used only in cursor mode.

Cluster node names are not part of the response: service providers identify
their instances and do not know Kubernetes or Slurm names.

The parser yields provider-local normalized records (identity, fabric IDs,
optional NVL partition). After matching they map to existing fields:

| REST record | Canonical topology |
|---|---|
| Matched requested instance ID | `InstanceTopology.InstanceID` |
| `network_path` | `FabricTiers` via `ClosestFirstFabricTiers` |
| `nvl_partition` | `XclrDomainID` (empty when omitted) |

IDs must be strings; null or empty path elements are rejected. Numbers are not
coerced, paths are not flattened, and hierarchy is never inferred from names.

## Authentication

A provider-local authenticator interface prepares each outgoing request and
receives its context. Built-in factories: `none`, `oauth2`, `bearer`, `basic` and `apiKey`.
The `bearer` factory acquires its token through a required nested `tokenRequest`
configuration for custom login APIs. The `oauth2` factory implements the standard
client-credentials grant and owns token caching, expiry and renewal. Both factories
use `auth.tokenRequest`, with fields and behavior selected by `auth.type`. Authentication is
reapplied on each retry. Transport authentication (mTLS) belongs in HTTP client
construction, not in the request hook.

Authentication is configured in `provider.params.auth` (or `providerParams.auth`
in the config file), passed through `providers.Config.Params`. Credential values
come from `providers.Config.Creds`: the existing server selects a nonempty request
`provider.creds` map when supplied, otherwise the `credentialsPath` YAML file
(a mounted Secret in Kubernetes). These credential maps are not merged.

| `auth.type` | Required authentication configuration | Required credentials |
|---|---|---|
| `none` | none | none |
| `oauth2` | `tokenRequest.tokenUrl`; optional `tokenRequest.scopes` | `clientId`, `clientSecret` |
| `bearer` | `tokenRequest` (below) | Nonempty login body, with the exact field names required by the login API |
| `basic` | none | `username`, `password` |
| `apiKey` | `headerName` | `apiKey` |

An absent or empty `auth` map selects `none`; a nonempty map must set `type`.
Unknown types and missing fields fail at load time. Custom Go factories register
in the provider-local registry (requires rebuilding Topograph; no shell commands
or runtime code downloads) and receive authentication configuration and credentials
separately. Credential values never appear in
`providerParams`, URLs, logs, the top-level `env` section, or version control.

### OAuth2 client credentials

For `type: oauth2`, `auth.tokenRequest` configures the token endpoint and
requested scopes. Both `clientId` and `clientSecret` belong in the effective
credentials map, never in `auth.tokenRequest`. Field names below are relative to
`auth.tokenRequest` for `type: oauth2`; the grant is fixed to client credentials.

| Field | Meaning |
|---|---|
| `tokenUrl` | Required absolute HTTPS OAuth2 token endpoint; may differ from the topology endpoint. Client credentials are sent only here. |
| `scopes` | Optional list of nonempty scope strings; sent as the space-delimited OAuth2 `scope` parameter. Omit to use the authorization server's default scopes. |

Example request fragment (credential values are placeholders):

```yaml
provider:
  name: rest
  params:
    auth:
      type: oauth2
      tokenRequest:
        tokenUrl: https://identity.example.invalid/oauth/token
        scopes:
          - api.read
  creds:
    clientId: <client-id>
    clientSecret: <client-secret>
```

The loader requires nonempty string credentials and validates the endpoint and
scope list before network I/O. Reject `clientId` and `clientSecret` in
`auth.tokenRequest` rather than accepting credentials in parameters.
The token request uses `POST` with `grant_type=client_credentials` and standard
OAuth2 client authentication. The credentials map is not serialized as a custom
login body; `clientId` and `clientSecret` supply the OAuth2 client credentials.
The returned access token is sent as `Authorization: Bearer <token>`;
conflicting static `Authorization` headers fail configuration validation.
Reuse the token within a discovery invocation and renew it before its advertised
expiry; client-credentials renewal requests a new token rather than using a
refresh token. Token requests share the invocation deadline and are not retried
locally. Tokens are not shared across invocations or credential maps.

### Custom bearer `tokenRequest`

For `type: bearer`, the required `tokenRequest` configuration defines the login
call and token extraction from its JSON response. The token is sent as
`Authorization: Bearer <token>`; conflicting static `Authorization` headers fail
configuration validation. The login body is the complete effective credentials
map, preserving its field names and values. Field names below are relative to
`auth.tokenRequest`.

| Field | Meaning |
|---|---|
| `url` | Absolute HTTPS login URL; may differ from the topology endpoint. Login credentials are sent only here. |
| `method` | `POST` (default) or `PUT`. |
| `encoding` | `json` (default) serializes the credentials map as a JSON object, including nested values. `form` encodes the same field names and values as form fields; every value must be a string. |
| `tokenPointer` | JSON Pointer to a required nonempty token string in the response (e.g. `/access_token`). |

The loader validates that bearer credentials are nonempty and serializable in the
selected encoding, without logging their values. Authentication configuration is
not included in the login body.

Example request fragment (credential values are placeholders):

```json
{
  "provider": {
    "name": "rest",
    "params": {
      "auth": {
        "type": "bearer",
        "tokenRequest": {
          "url": "https://identity.example.invalid/login",
          "encoding": "json",
          "tokenPointer": "/access_token"
        }
      }
    },
    "creds": {
      "login": "<login>",
      "secret": "<secret>"
    }
  }
}
```

The login JSON body is exactly the `creds` object. With `encoding: form`,
those same entries become form fields. A credentials file contains only
that `creds` object's fields and values.

## Request construction and pagination

Topology requests are GETs to one configured endpoint with static query
parameters and headers. The inventory is fetched and matched to the requested
instances locally. Query values are encoded with URL APIs.

Use the existing `pageSize` setting, or the provider default when unset. Parameter
names are configurable; pagination counts all returned items before node matching.

| Mode | Progression | Stop when |
|---|---|---|
| `none` | One GET | Response received |
| `page` | Start at page 0 or 1; increment by one | Empty page |
| `offset` | Start at configured offset; advance by returned item count | Empty page |
| `cursor` | Omit cursor initially; use each response's next cursor | Cursor is missing, null or empty |

## Response customization

Two parsers turn response fields into topology:

1. **Default parser:** the response above.
2. **Field-mapping parser:** JSON Pointers select the items array and the
   fields (identity, region, path, partition) within each item. Paths are always
   closest-first. Pointers only extract; nothing is computed or restructured.

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

## Fabric and partition constraints

Upstream switch and partition IDs must identify the same resource wherever they
appear in the requested inventory. The provider preserves these IDs and validates
matched records together before graph conversion:

- Reject a switch or partition ID used in multiple regions, even with identical
  paths. Shared resources across regions are outside the initial scope.
- Every occurrence of a switch must have the same path toward the fabric root.
  For example, `[leaf-1, spine-a]` and `[leaf-1, spine-b]` conflict. Paths may have
  different lengths when their shared switches have consistent paths to the root.
- Reject repeated switch IDs within a path, switch IDs equal to requested
  instance IDs, and switch or instance IDs equal to the reserved `no-topology`
  value.

Within one region, nodes with the same partition ID belong to the same accelerator
domain. Partition and switch IDs may share a name because they are stored
separately. Conflicting topology records fail with 502 before graph conversion.

## Proposed configuration

Names are illustrative and subject to review.

```yaml
provider: rest
pageSize: 100
credentialsPath: /etc/topograph/credentials.yaml
providerParams:
  endpoint: https://topology.example.invalid/v1/nodes
  auth:
    type: oauth2
    tokenRequest:
      tokenUrl: https://identity.example.invalid/oauth/token
      scopes:
        - api.read
  pagination:
    mode: cursor
    sizeParam: limit
    cursorParam: cursor
    nextCursorPointer: /next_cursor
    maxPages: 1000
  parsing:
    type: mapping            # or "default"
    itemsPointer: /items
    fields:
      identity: /instance_id
      region: /region
      networkPath: /network_path
      nvlPartition: /nvl_partition
  missingNodes: error
```

Page mode adds `pageParam`, `initialPage` and a completion policy; offset mode
uses `offsetParam`, `initialOffset`. Credential values are not shown; the loader
receives them in `providers.Config.Creds` and no new top-level `creds` section
is added. Incompatible options, invalid pointers, unknown factories, conflicting
static/pagination parameters and missing credential fields fail at load time.

## HTTP behavior and errors

Use verified TLS, disable redirects, and bound response sizes and discovery time.
Each provider invocation has one deadline covering token acquisition and renewal,
pagination and retries;
all network calls and retry waits honor cancellation. Logs and errors omit
credentials, query values and response bodies.

Retry transient transport errors and GET responses 429/502/503/504 with bounded
backoff, jitter and `Retry-After`. Return parsing, matching and authentication
failures without local retries. Custom login and OAuth2 token calls are not retried
locally. The existing server retry policy still applies to returned errors.

Validate configuration in the loader before network I/O. Return `*httperr.Error`:

| Failure | Status |
|---|---|
| Invalid request input or provider configuration | 400 |
| Upstream, protocol, parsing, matching or authentication failure | 502 |
| Upstream timeout | 504 |

## Verification

Test authentication and exact custom-login credential-body serialization,
OAuth2 grant and scope encoding, client authentication, token reuse and expiry
renewal within one discovery, rejection of OAuth2 credentials in parameters,
pagination modes and limits, field mappings, matching and graph
constraints, and both missing-node policies. Include invalid configuration,
upstream failures, cancellation, redaction and existing server retry behavior.

## Open decisions

- Set default page, record, response-size and discovery-time limits.
- Decide whether header cursors are required initially. mTLS remains a follow-up
  scope decision.
