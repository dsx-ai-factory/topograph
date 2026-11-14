# Minerva Topology Provider

[NVIDIA Minerva](https://docs.nvidia.com/) exports the topology model of a fabric — hierarchy groups, devices, physical Layer-2 and NVLink connections, BGP adjacencies, and hosted components such as GPUs — as typed records through its `export-topology` API. The API returns the records in device-based pages that the client follows with a cursor.

The Topograph Minerva provider queries this endpoint twice per request. The `east-west` layer gives the device/link graph, from which the provider builds the switch tree consumed by your workload manager in the same style as the [NetQ provider](./netq.md): it derives switch tiers from each device's role and reduces Clos fabrics to a canonical tree using `topology.Merger`. The `nvlink` layer gives the rack that holds each node, which the provider reports as the node's NVLink domain.

## When to Use This Provider

Use the Minerva provider when Minerva is deployed as the fabric management plane and you want Topograph to source switch-tree topology from it rather than from `ibnetdiscover` or a CSP placement API.

| Scenario | Recommended Topograph provider |
|---|---|
| Minerva deployed and managing the fabric | Minerva |
| Traditional IB fabric, no Minerva | [InfiniBand](./infiniband.md) |
| Spectrum-X fabric, NetQ deployed | [NetQ](./netq.md) |

## Output

The Minerva provider produces a switch tree (`topology/tree`) and, when Minerva models racks for the NVLink layer, accelerator domains (`topology/block`), consumed by whichever engine you configure:

- **Slurm engine** (`engine: slurm`) — generates `topology.conf` for Slurm topology-aware scheduling: written to `topologyConfigPath` when set, otherwise returned in the HTTP response
- **Kubernetes engine** (`engine: k8s`) — applies `fabric.topograph.run/` labels to nodes; see [Kubernetes deployments](#kubernetes-deployments) for the node metadata the Helm chart needs
- **NFD engine** (`engine: nfd`) — publishes topology as Node Feature Discovery `NodeFeature` and `NodeFeatureGroup` custom resources
- **Slinky engine** (`engine: slinky`) — writes topology data to a Kubernetes ConfigMap

### NVLink domains

Minerva's `nvlink` layer reports a rack hierarchy, the devices in each rack, NVLink neighbors, and hosted components such as GPUs. It has no attribute that names an NVLink domain, so the provider treats each rack as one NVLink domain: a requested node's domain is the rack named in its `rack` field or, when that field is absent, its primary parent when that parent is a `rack` group. Minerva scopes groups by their ancestry, so two racks can share a name: when they do, the domain ID is qualified with the rack's parent (`SU-1.Rack-1`, with a numeric suffix such as `-2` if that ID is already the name of another rack), and the rack that lists the node among its children is the one used. A node whose rack name is shared and that no single rack lists gets no domain, and a warning is logged. The export carries only names for a group, with no field for its full ancestry, so two racks with the same name under same-named parents in different pods cannot be told apart and form one domain. A rack that a node's `rack` field names without any group record is a domain under that name, and no qualified ID takes it. Sub-domains are not populated. Because a rack is treated as one domain, a rack that holds more than one NVLink partition puts nodes from separate partitions into the same `topology/block` domain. Verify that Minerva's rack grouping matches how your scheduler should group nodes. If it does not, and you use a Kubernetes-based engine, publish the partition as a node label and set `acceleratorDomainSourceLabel` to it, which takes precedence over the rack even when Minerva models racks (see [Using an existing accelerator-domain label](../engines/k8s.md#using-an-existing-accelerator-domain-label)). A requested node that the `nvlink` layer does not place in a rack gets no domain, and when no requested node has one the graph carries no `topology/block` data. NVLink neighbors and GPU components are not read.

The switch tree does not depend on the `nvlink` layer. If the `nvlink` request fails or times out, the provider logs a warning and returns the tree without `topology/block` data; only a canceled request still fails. An `nvlink` response with no records, for example on an Ethernet-only fabric, is not an error: it also yields the tree without `topology/block` data, and nothing is logged. Topograph selects a single provider per request, so the [DRA provider](./dra.md) can't be combined with Minerva in the same request. If your Minerva deployment does not model NVLink racks, set the `k8s`/`nfd`/`slinky` engine's `acceleratorDomainSourceLabel` parameter to read accelerator-domain membership directly from a node label — this is engine-level configuration and works with any provider, including Minerva. See the [Kubernetes engine documentation](../engines/k8s.md#configuration) for details.

See the engine documentation (`docs/engines/`) for details on each output format.

## Prerequisites

- A running NVIDIA Minerva instance accessible from the Topograph host, serving a TLS certificate that the Topograph host trusts (see [TLS](#tls))
- An API key authorized to call `POST /v2/export-topology`

## TLS

Topograph verifies the Minerva server certificate, because the API key is sent with every request and the response decides the cluster topology. If Minerva uses a certificate issued by a private CA, add that CA to the trust store of the Topograph host. On Linux you can instead point Go at a CA bundle through the `env` section of the Topograph config, for example `SSL_CERT_FILE: /etc/topograph/minerva-ca.pem`. An untrusted certificate fails the request with a `502` that names the certificate error. Topograph also does not follow redirects from Minerva, so the API key is only ever sent to the configured `apiUrl`; a redirect fails the request with a `502`.

On macOS, Go verifies certificates with the system Security framework, which uses the Keychain and ignores `SSL_CERT_FILE`, and which also rejects a certificate that is not standards compliant, such as one without a Subject Alternative Name for the host or with too long a validity period (`x509: "<name>" certificate is not standards compliant`). Re-issue the certificate with a matching Subject Alternative Name, or run Topograph on Linux.

To skip the check, set the optional `insecureSkipVerify` credential to `true`. It is a convenience for quickly validating Topograph against a Minerva instance in a trusted environment, such as when its certificate has not been set up yet. With it set, Topograph does not authenticate the server, so the API key and the topology are not protected against someone on the network path. Topograph logs a warning when the provider is loaded with it. It does not relax the other safeguards: `apiUrl` must still be an `https` URL, and redirects are still refused.

## Credentials

| Field | Required | Description |
|---|---|---|
| `apiKey` | Yes | Minerva API key, sent as the `X-Api-Key` header |
| `insecureSkipVerify` | No | Default `false`. When `true`, the Minerva server certificate is not verified; see [TLS](#tls). |

## Parameters

| Field | Required | Description |
|---|---|---|
| `apiUrl` | Yes | Base URL of the Minerva API (e.g. `https://minerva.example.com/minerva/api`); the provider appends `v2/export-topology`. Must be an `https` URL; any other scheme is rejected so the API key is never sent in clear text. |

## Configuration

### Credentials via File

Store credentials in a YAML file:

```yaml
apiKey: <API_KEY>
# insecureSkipVerify: true   # optional; skips TLS certificate verification, see TLS
```

Reference the file in your Topograph config:

```yaml
http:
  port: 49021
  ssl: false

provider: minerva
providerParams:
  apiUrl: https://minerva.example.com/minerva/api
engine: slurm

requestAggregationDelay: 15s
credentialsPath: /path/to/credentials.yaml
```

### Credentials via API Request Payload

Pass credentials directly in the topology request:

```json
{
  "provider": {
    "name": "minerva",
    "creds": {
      "apiKey": "<API_KEY>"
    },
    "params": {
      "apiUrl": "https://minerva.example.com/minerva/api"
    }
  },
  "engine": {
    "name": "slurm"
  },
  "nodes": [{
    "region": "local",
    "instances": {"srv-01": "srv-01", "srv-02": "srv-02"}
  }]
}
```

Instance keys are carried to the output as the compute nodes' IDs. Values are node names, which must match Minerva's device names. Minerva has no regions, so use `local`. When a Slurm request omits `nodes`, the Slurm engine discovers the node list with `scontrol` and the provider uses each exact node name as its own instance ID, in region `local`.

### Kubernetes deployments

The Helm chart's default `nodeDataBroker.enabled: true` does not work with this provider: the node-data-broker has no Minerva mode, and fails with `unsupported provider "minerva"`. Set `nodeDataBroker.enabled: false`, so the node observer also skips the broker readiness checks, and give Topograph the node identity another way:

- Point `config.nodeMetadata` at existing node labels or annotations. Both `instanceID` and `region` are required, each with a `type` (`label` or `annotation`) and a `key`. See [Node instance ID and region sources](../api.md#node-instance-id-and-region-sources).
- Or supply `nodes` explicitly in each request, as in the example above.

Minerva has no regions, so any consistent non-empty region value works. The node *name* is what Topograph looks up among Minerva's device names, so Kubernetes node names must match them; the instance ID is carried to the output unchanged.

```yaml
nodeDataBroker:
  enabled: false

config:
  nodeMetadata:
    instanceID:
      type: label
      key: kubernetes.io/hostname
    region:
      type: annotation
      key: example.com/region   # any non-empty value, for example "local"
```

## How It Works

1. Calls `POST /v2/export-topology` with `layers: ["east-west"]` and a `deviceLimit`. The request page size, when set, is the `deviceLimit`; it defaults to, and is capped at, Minerva's maximum of 500 devices per page. The provider follows `nextCursor`, passing it back unchanged, until `complete` is `true`, and fails the request with a `502` if an incomplete page carries no cursor, if a cursor repeats, or if a traversal exceeds 10,000 pages. Minerva reads a live, eventually consistent model, so a multi-page traversal is not a point-in-time snapshot.
2. Collects the `device` records and the `layer2` neighbor records. Minerva does not report tier numbers, and its export API does not enumerate device roles, so each device's tier is derived from the standard roles a Minerva fabric reports: `server` is tier `-1`, `leaf` is tier `0`, `spine` is tier `1`, and `super_spine` is tier `2`. Devices with any other role — including `unknown` (a role that is not modeled) and Minerva's deployment-specific "Custom" roles — take no part in the switch tree, and neither does a device tagged with the `north-south` fabric type: the `east-west` layer carries north-south devices too, but they are not part of the compute fabric, so such a device is never a compute node, even when it is requested, and a requested one is grouped under `no-topology` like any requested node that is not a server. Minerva includes a `device` record only for the devices it selected as hierarchy anchors; a neighbor reference does not add the far-end device. A compute node can therefore appear only as the far end of its leaf's links, with no `device` record and no role. Because the caller names the compute nodes, a requested node whose role Minerva does not report (no record, or role `unknown`) is a server; a role that Minerva does report is never overridden, and a device that nobody requested and that has no role takes no part in the tree. A physical neighbor's `from`/`to` order carries no meaning, so each link is recorded on both devices. BGP sessions are ignored. A requested server's vertex ID is the instance ID it was requested with, and its name is the node name. A switch's vertex ID is its name, unless that name is a requested instance ID or the ID of the shared fallback group (`no-topology`, see below); the ID is then prefixed with `switch-` (a prefix that is valid in the Kubernetes label values some engines publish switch IDs as), repeated until the ID is unique, that is, until it is not the name of another device, a requested instance ID, or the ID of another renamed switch (renamed switches are handled in name order, so the result is the same on every run). The shared fallback group keeps the ID `no-topology`, unless a requested instance ID is itself `no-topology`; the group is then called `no-topology-group` (or that name followed by another `-group`, if an instance ID takes it too), so that no compute node's ID is changed. This only happens when a requested instance ID is literally `no-topology`: the Slurm engine rejects a group that contains a node with its own ID. Engines that recognize only the exact ID `no-topology` as the fallback marker, such as the graph engine, then report the renamed group as a network layer of those nodes. Switch IDs avoid whichever ID the group has.
3. Builds the switch tree outward from the requested server nodes (filtered by the incoming `ComputeInstances`), following each device's links to a neighbor at a higher tier — regardless of which of the two devices declared the link in Minerva's response — the same directed-by-tier approach the NetQ provider uses once it has tier data. A requested server with no resolvable switch parent is grouped under a shared `no-topology` switch rather than being dropped or reported as a partial failure. A requested node that Minerva does not report at all is grouped there too.

   A server cabled to more than one leaf (for example one per plane in a multi-plane fabric) can have only one parent in the output tree, so the provider keeps the parent at the lowest tier and, among equals, the one whose device name sorts first.
4. Reduces Clos fabrics — switches that fan out to the same set of children at a given tier — into a canonical tree via `topology.Merger`, exactly as the NetQ provider does. In a partially connected fabric a switch can still be reachable through several parents after merging (for example two spines sharing one leaf); the provider keeps it under the first parent in ID order and prunes switches left empty, so the output is always a tree.

   Known limitation: if a switch has children at different depths (for example a spine with both a directly attached server and a leaf branch beneath it), `topology.Merger` can drop the deeper branch's children from the merged tree. A requested server lost this way is not omitted silently; it is grouped under `no-topology` and recorded in the missing-topology metric.

5. Calls `POST /v2/export-topology` again with `layers: ["nvlink"]`, following the same pagination, and maps each requested node to its rack as described under [NVLink domains](#nvlink-domains).

## Errors

| Condition | Result |
|---|---|
| `apiUrl` missing, not an `https` URL, or `apiKey` missing or empty | `400`, when the provider is loaded |
| Minerva answers the `east-west` request with a non-2xx status (for example `400` for a rejected request, `403` for a missing or invalid key, or `503` for a model-query failure) | `502`; the message names Minerva's status and carries its response body (a `502` that Minerva itself returns is reported as is, without a second status). The caller's own request was valid, so Minerva's status is not passed through |
| The server certificate is not trusted, Minerva answers with a redirect, the host cannot be reached, or a page takes longer than 2 minutes to answer, or a layer takes longer than 10 minutes in total | `502` |
| Response is not valid JSON | `502` |
| The response has no record collection for the requested layer (an empty collection is fine) | `502` |
| An incomplete page has no `nextCursor`, a `nextCursor` repeats, or a layer takes more than 10,000 pages | `502` |
| No nodes are requested and none are discovered, so the node list is empty | `502` `no topology available from Minerva` |

These errors describe the `east-west` request. The same failures on the `nvlink` request are logged as a warning and leave the graph without domains, unless the request was canceled; a missing `nvlink` record collection is such a failure.

A request whose nodes Minerva does not know is not an error: those nodes are grouped under `no-topology` and a warning naming each one is logged.

## Verifying the Output

After triggering topology generation, query the result endpoint:

```bash
response=$(curl -s -w '\n%{http_code}' -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
code=$(tail -n1 <<<"$response")
id=$(sed '$d' <<<"$response")
if [ "$code" != 202 ]; then
  echo "topology request rejected ($code): $id" >&2
  exit 1
fi

while :; do
  response=$(curl -s -w '\n%{http_code}' "http://localhost:49021/v1/topology?uid=$id")
  code=$(tail -n1 <<<"$response")
  body=$(sed '$d' <<<"$response")
  case "$code" in
    202) sleep 2 ;;
    200) echo "$body"; break ;;
    *) echo "topology request failed ($code): $body" >&2; exit 1 ;;
  esac
done
```

For the Slurm engine, verify the generated `topology.conf` reflects the expected switch hierarchy: read the file at `topologyConfigPath`, or, when it is unset, check the `topology.conf` content in the response body above. See the [Slurm engine documentation](../engines/slurm.md) for details.
