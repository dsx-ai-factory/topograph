# Onboard Your Cloud with the HTTP Provider

This guide is for GPU cloud operators (neoclouds) who want Topograph to place
workloads on their infrastructure according to its physical network topology.
Instead of contributing a provider written in Go, you expose one HTTPS endpoint
that returns your topology as JSON, and your customers configure Topograph's
generic [`http` provider](../providers/http.md) to read it.

When you finish, Topograph running in your customers' Slurm or Kubernetes
clusters can:

- Label Kubernetes nodes with their fabric tiers and NVLink domain, for use by
  topology-aware schedulers such as KAI Scheduler and Kueue.
- Generate Slurm `topology.conf` with `topology/tree` or `topology/block`.

## How it works

```text
Your inventory ──► Topology API (HTTPS, JSON) ◄── Topograph `http` provider
                                                        │
                                       canonical topology graph
                                                        │
                                 Kubernetes labels / Slurm topology.conf
```

1. Topograph learns which instances it must describe: the instance ID of each
   cluster node, from a Kubernetes node label or a Slurm node list.
2. It calls your API, following pagination until the inventory is complete.
3. It matches records to those instance IDs, validates the topology, and passes
   it to the scheduler engine.

Your API does not need to know cluster node names; Topograph maps instance IDs
to node names itself.

## Step 1: Decide what to expose

For each customer-visible compute instance, return:

| Data | Example | Notes |
|---|---|---|
| Instance ID | `i-0a12` | The same ID your customers can see on the machine, for example in a node label or the instance metadata service. |
| Network path | `["leaf-07", "spine-2", "core-1"]` | IDs of the switches above the instance in the compute (east-west) fabric, closest first. Any depth. |
| Accelerator domain | `nvl72-rack-14` | Optional. The NVLink domain or partition the instance's GPUs belong to. |
| Region | `us-east-2` | Optional. Used as a consistency check. |

Guidelines:

- **Use stable, unique switch IDs.** Two instances under the same leaf switch
  must report the same leaf ID. The same ID must always have the same parent
  switch: if `leaf-07` sits under `spine-2` in one record, it must do so in every
  record.
- **Make IDs unique across regions.** Topograph rejects a switch or domain ID
  that appears in two regions. Prefix IDs with the region or site if they repeat.
- **Report only the fabric used for GPU-to-GPU traffic** (InfiniBand or
  RoCE/Ethernet), not the management or storage network.
- **Report only what you know.** A record may have a network path, an accelerator
  domain, or both. Omit a field rather than inventing a value.
- **Hide what you must.** IDs can be opaque (for example, hashed) as long as they
  are consistent. Topograph uses them only to group instances.
- **Strings only.** Numeric IDs must be sent as JSON strings.

## Step 2: Build the endpoint

The simplest path is to return the provider's default format. Then your
customers need no parsing configuration at all.

```http
GET /v1/topology/instances?limit=100 HTTP/1.1
Host: api.example-cloud.com
Authorization: Bearer <token>
Accept: application/json
```

```json
{
  "items": [
    {
      "instance_id": "i-0a12",
      "region": "us-east-2",
      "network_path": ["leaf-07", "spine-2", "core-1"],
      "accelerator_domain": "nvl72-rack-14"
    },
    {
      "instance_id": "i-0a13",
      "region": "us-east-2",
      "network_path": ["leaf-07", "spine-2", "core-1"],
      "accelerator_domain": "nvl72-rack-14"
    }
  ],
  "next_cursor": "eyJvZmZzZXQiOjEwMH0"
}
```

Requirements:

- **HTTPS** with a certificate from a public or customer-trusted CA. Topograph
  does not follow redirects.
- **GET** request with no body. You may require static query parameters or
  headers; customers set them in `request.query` and `request.headers`.
- **Scope the response to the caller.** Return only instances the authenticated
  customer owns. Topograph ignores records for instances it did not ask about,
  but other tenants' data should never leave your API.
- **Pagination** (recommended above a few hundred instances). Pick one:
  - Cursor: return `next_cursor`, accept it back as a query parameter, and
    return `null` or omit it on the last page.
  - Page number or offset: accept a page or offset parameter and a size
    parameter, and return an empty `items` array after the last page.
- **Size:** keep each page under 32 MiB (customers can raise the limit).
- **Rate limiting:** answer `429`, `503`, or `504` with a `Retry-After` header;
  Topograph retries those up to five times. Other `4xx` and `5xx` statuses are
  not retried.
- **Consistency:** Topograph reads all pages in one pass, within a deadline
  (5 minutes by default). Use a snapshot or a stable sort order so an instance
  is not returned twice or skipped while pages are being read. Duplicates fail
  the discovery.

### Already have an API?

If your existing inventory API returns the same information under different
field names, customers can map it with JSON Pointers instead of you building a
new endpoint:

```json
{
  "data": {
    "machines": [
      {
        "hw": { "id": "i-0a12", "zone": "us-east-2" },
        "net": { "uplinks": ["leaf-07", "spine-2", "core-1"] },
        "gpu": { "nvlink_domain": "nvl72-rack-14" }
      }
    ],
    "page_token": "abc"
  }
}
```

```yaml
parsing:
  type: mapping
  itemsPointer: /data/machines
  fields:
    identity: /hw/id
    region: /hw/zone
    networkPath: /net/uplinks
    acceleratorDomain: /gpu/nvlink_domain
    nextCursor: /data/page_token
```

Mappings only select fields. The network path must already be a single array
ordered closest first. If your API orders switches root first, splits them
into separate `leaf`, `spine`, and `core` fields, or needs several calls to
assemble a record, add a small endpoint in the default format instead.

## Step 3: Choose authentication

Pick the scheme your API already supports. Customers supply the credentials to
Topograph at runtime; they never appear in configuration files or URLs.

| Your API uses | `auth` configuration | Credentials the customer provides |
|---|---|---|
| OAuth2 client credentials | `type: oauth2`, `oauth2.tokenUrl`, optional `scopes` | `clientId`, `clientSecret` |
| A login call that returns a token | `type: bearer`, `bearer.url`, `bearer.tokenPointer` (for example `/access_token`) | The exact fields your login body expects, for example `username` and `password` |
| A static API key in a header | `type: apiKey`, `headerName` | `apiKey` |
| HTTP Basic | `type: basic` | `username`, `password` |

Issue customers a read-only credential that can reach only the topology
endpoint. OAuth2 is preferred: tokens are short-lived and Topograph renews them
automatically.

## Step 4: Tell Topograph which instance each node is

Topograph needs the instance ID of each cluster node, and it must equal
`instance_id` in your API.

- **Kubernetes.** Put the instance ID on each Node as a label or annotation
  during provisioning, for example `example-cloud.com/instance-id: i-0a12`, and
  the region in `topology.kubernetes.io/region`. Topograph reads them through
  `nodeMetadata`.
- **Slurm.** The topology request lists instance-to-node mappings explicitly;
  see [Install on Slurm](./quickstart-slurm.md). Publishing the instance ID in
  your instance metadata service makes that mapping easy to script.

## Step 5: Test it

Run Topograph locally against your API with the `graph` engine. Create
`payload.json`, replacing the placeholders. Use test credentials, and do not
commit them.

```json
{
  "provider": {
    "name": "http",
    "creds": { "clientId": "<CLIENT_ID>", "clientSecret": "<CLIENT_SECRET>" },
    "params": {
      "request": { "url": "https://api.example-cloud.com/v1/topology/instances" },
      "auth": {
        "type": "oauth2",
        "oauth2": { "tokenUrl": "https://auth.example-cloud.com/oauth/token" }
      },
      "pagination": { "mode": "cursor", "sizeParam": "limit" }
    }
  },
  "engine": { "name": "graph" },
  "nodes": [
    { "region": "us-east-2", "instances": { "i-0a12": "gpu-node-1", "i-0a13": "gpu-node-2" } }
  ]
}
```

```bash
./bin/topograph -c config/topograph-config.yaml -v 4 &
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

Repeat the last request while it returns HTTP 202. Check that:

- Instances under the same leaf switch are grouped together, and each
  instance's `network_layers` starts at its leaf switch.
- Instances in the same NVLink domain share the same
  `accelerator.topograph.run/domain` value in `labels`.
- No instance appears under `no-topology`.

If the request fails, the error states the cause, for example `switch "leaf-07"
has conflicting paths toward the fabric root` or `2 requested node(s) have no
topology record: gpu-node-3, gpu-node-4`. See
[Validation](../providers/http.md#validation) and
[Errors and retries](../providers/http.md#errors-and-retries).

## Step 6: Publish customer instructions

Give customers a short page with their Helm values. For a Kubernetes cluster:

```yaml
provider:
  name: http
  params:
    request:
      url: https://api.example-cloud.com/v1/topology/instances
    auth:
      type: oauth2
      oauth2:
        tokenUrl: https://auth.example-cloud.com/oauth/token
    pagination:
      mode: cursor
      sizeParam: limit
engine:
  name: k8s
config:
  credentialsSecret: topograph-credentials
  nodeMetadata:
    instanceID:
      type: label
      key: example-cloud.com/instance-id
    region:
      type: label
      key: topology.kubernetes.io/region
nodeDataBroker:
  enabled: false
```

and the secret, created from a `credentials.yaml` file containing `clientId` and
`clientSecret`:

```bash
kubectl create secret generic topograph-credentials --from-file=credentials.yaml=<path>
```

Then follow [Install on Kubernetes](./quickstart-k8s.md) with these values.
Topograph labels each node with `fabric.topograph.run/tier-N` and
`accelerator.topograph.run/domain`; see
[Node Labels and Annotations](../reference/node-labels.md).

## When to write a dedicated provider instead

The `http` provider covers a single GET endpoint. Consider contributing a
dedicated provider (see the
[contribution guide](https://github.com/dsx-ai-factory/topograph/blob/main/CONTRIBUTING.md))
if you need:

- Several API calls per discovery, or POST requests with filter bodies.
- Derived values, such as switch IDs computed from rack positions.
- Automatic instance ID discovery on the node itself (the Node Data Broker).
- Accelerator sub-domains, or mTLS authentication.

## Onboarding checklist

- [ ] HTTPS endpoint returns instance ID, closest-first network path, and
      optional accelerator domain, as strings.
- [ ] Switch IDs are stable, unique across regions, and have one parent each.
- [ ] Response is scoped to the authenticated customer.
- [ ] Pagination terminates with an empty page or an empty cursor.
- [ ] `429` and `503` responses include `Retry-After`.
- [ ] Read-only credentials are available to customers.
- [ ] Each node carries its instance ID (Kubernetes label or Slurm mapping).
- [ ] A test with the `graph` engine shows the expected grouping.
- [ ] Customer-facing Helm values and secret instructions are published.
