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

# Akamai Cloud Topology Provider

The `akamai` provider discovers switch topology from the Akamai Cloud external
API. The `akamai-sim` provider runs the same
discovery without cloud access, using a Topograph model or a saved API response.

## Prerequisites

- Network access from the Topograph server to the topology endpoint. HTTPS
  certificates must be trusted by the server's system trust store.
- An externally obtained API token with access to topology in the requested regions.
- Requested instance IDs matching `hosts[].host_uuid`, grouped by region. Supply
  node mappings explicitly in the request, or use the existing configurable
  [Kubernetes identity sources](../api.md#node-instance-id-and-region-sources).
  In Kubernetes, the Node Data Broker can collect these identities from IMDS as
  described below. For Slurm, the provider discovers instances by running
  the same IMDS token exchange on each requested node through `pdsh`, and groups the resulting `host_uuid`
  values by region. Nodes with missing or malformed IMDS metadata are skipped
  with a warning.

## Credentials

Supply the externally obtained token as `provider.creds.token` or through a
runtime credentials file containing a top-level `token` field, following the
Nautobot provider's credential configuration pattern. With Kubernetes Secret
mounts, use the `credentials.yaml` filename described in [Config and API](../api.md).
The provider does not read tokens from environment variables directly. Keep
working tokens out of repository files.

Every topology request sends `Authorization: Bearer <token>`. The topology API
has no authentication endpoint; the topology client does not acquire or refresh
tokens. The caller provides and manages this token. Missing, blank, non-string,
or multiline tokens fail configuration with HTTP 400. HTTP 401/403 errors
from the topology endpoint are returned directly.

## Parameters

| Field | Required | Description |
|---|---|---|
| `apiUrl` | Yes (`akamai` only) | API base URL, optionally including a base path. The provider appends `/topology`. |
| `trimTiers` | No | Remove 0–2 outermost switch tiers from every discovered path. Default: 0. |

URLs must be absolute HTTP(S) URLs without embedded credentials, query strings,
or fragments. Use HTTPS for live tokens; HTTP is supported for local test
endpoints. Redirects are rejected. Requests have a 30-second timeout, respect
context cancellation, and are sent once without automatic retries.

Topograph's server `pageSize` setting controls the number of switch records
requested per page (default: 100). The client sends `region`, `page` (starting
at 1), and `page_size` query parameters:

```text
GET <apiUrl>/topology?region=<region>&page=1&page_size=100
```

The pagination request parameter names follow the initial numbered-page
contract and still require live-endpoint verification. Responses contain
`region`, `data`, `page`, `pages`, and `results`. All pages are collected before
switch relationships are resolved. Page counts and result counts must remain
consistent, the response region must match the requested region, and the final
record count must equal `results`. A failure on any page fails the generation.

## Node Identity from IMDS

With `provider.name: akamai`, the Node Data Broker obtains local identity from
the [Akamai Metadata service](https://techdocs.akamai.com/cloud-computing/docs/metadata-service-api):

1. Send `PUT http://169.254.169.254/v1/token` with
   `Metadata-Token-Expiry-Seconds: 3600` to obtain a node-local metadata token.
2. Send `GET http://169.254.169.254/v1/instance` with that `Metadata-Token` and
   `Accept: application/json`.
3. Decode `host_uuid` and `region` from the JSON response and write
   the standard Node annotations:

| Annotation | IMDS field |
|---|---|
| `topograph.run/instance` | `host_uuid` |
| `topograph.run/region` | `region` |

The numeric IMDS `id` is not the topology host UUID. Both fields must be
non-empty strings; invalid or incomplete metadata prevents the node
update. Other Node annotations and labels are preserved.

The broker obtains its own metadata token independently of `provider.creds.token`
used by the topology API. No topology API credentials are needed for IMDS.
Metadata requests use the shared HTTP helper, which retries transient failures.
The instance URL is always `http://169.254.169.254/v1/instance`; the token URL is
`http://169.254.169.254/v1/token`.

For Helm, set `provider.name: akamai` and keep `nodeDataBroker.enabled: true`.
For a standalone broker, its configuration can use the defaults:

```yaml
provider:
  name: akamai
healthzPort: 8080
```

Each broker pod must be able to reach its node's IMDS. The broker applies the
annotations before serving its health endpoint and becoming Ready.

## How Discovery Works

1. Index entries in `data` by switch `name`. Switch names are unique within a
   region; repeated records for one name are combined, including their uplinks.
2. Entries with non-empty `hosts` arrays identify switches with attached hosts.
   Match `host_uuid` to requested instance IDs and deduplicate repeated ports
   for the same host on a switch.
3. Follow each switch's `uplink.device` as its parent switch name until a
   switch has `uplink: null`. Follow every parent if repeated records give the
   same switch different uplinks. `connections` does not determine parents.
4. Convert every host attachment and parent path through the canonical topology
   builder. Switch paths have arbitrary depth and run closest-first. Canonical
   switch IDs are scoped by region; switch display names and exported
   `network_layers` retain the API's `name` values.
5. Retain requested hosts absent from all attachment lists under `no-topology`.

The `fabric`, `serial`, `is_healthy`, and port fields do not affect discovery.
Both `fabric` and `gpu_fabric` entries participate, as do entries with an unknown
or absent fabric value. Missing health and explicit false health are decoded
separately, but neither filters hosts. No accelerator domains are inferred.

Multiple leaf attachments and parent paths are preserved in the returned graph.
**The provider does not merge the graph into a tree.** Tree-only translations
(including Slurm, Slinky, Kubernetes, and NFD) can reject repeated vertices;
such topologies require a separate merge step before those engines can consume
them. The graph engine's per-instance `network_layers` can represent one path
only: it reports the first path in sorted leaf-name and parent-name order.
The full canonical graph retains all paths.

Blank switch names, blank host UUIDs, and blank non-null uplink devices return
HTTP 502. Cycles or unknown parents on a requested host's path also return
HTTP 502. Invalid caller configuration, missing regions, empty requested IDs,
or conflicting region/node mappings for one host UUID return HTTP 400.
Topology HTTP failures preserve upstream 4xx/5xx status codes. Redirects,
connection failures, cancellation, malformed responses, and
inconsistent pagination return HTTP 502. Error response bodies are omitted.

## Example Request and Verification

```json
{
  "provider": {
    "name": "akamai",
    "creds": {
      "token": "<AKAMAI_TOKEN>"
    },
    "params": {
      "apiUrl": "https://extapi.linode.com"
    }
  },
  "engine": { "name": "graph" },
  "nodes": [
    {
      "region": "<REGION>",
      "instances": { "<HOST_UUID>": "worker-1" }
    }
  ]
}
```

Save the request as `payload.json`, substitute the runtime token and node
identities, and submit it to a running Topograph server:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

Generation is asynchronous; repeat the result request while it returns HTTP
202. On success, verify that `network_layers` starts at a host-attached switch
and follows the named uplinks toward the root.

## Simulation

`akamai-sim` needs exactly one of `modelFileName` and `topologyFileName`:

| Parameter | Description |
|---|---|
| `modelFileName` | A standard Topograph YAML model basename, such as `small-tree.yaml`, or a relative/absolute model path. Generates Akamai-shaped switch records, host attachments, uplinks, and downstream connections. |
| `topologyFileName` | Path to a saved JSON or YAML API response containing one complete region: `page: 1`, `pages: 1`, and `results` matching the number of records in `data`. Preserves redundant host attachments and switch uplinks. File extension does not matter. |
| `trimTiers` | Remove 0–2 outermost tiers. Default: 0. |
| `api_error` | `1`: topology failure (502); `400`–`599`: topology failure with that HTTP status. Default: 0. |

The simulator returns deterministic numbered pages using the server's `pageSize`
and uses production discovery and graph conversion. No credentials or network
access are needed. Model instance labels
are preserved; model accelerator annotations do not create accelerator domains.

For model simulation, replace the live provider section with:

```json
{
  "name": "akamai-sim",
  "params": { "modelFileName": "small-tree.yaml" }
}
```

Use model node names as host UUIDs; the standard small-tree model's region is
`none`. For the supplied response fixture, use:

```json
{
  "name": "akamai-sim",
  "params": { "topologyFileName": "tests/output/akamai/topology.yaml" }
}
```

The test suite keeps a JSON copy at `pkg/providers/akamai/testdata/topology.json`.
Explicit node requests filter either input and preserve missing requested
hosts. Engines supporting provider `GetComputeInstances` can obtain model nodes
or all unique snapshot host UUIDs automatically. Snapshot auto-discovery uses
the host UUID as the node name.

See [Modeling and API Simulation](../modeling.md) for the model format.
