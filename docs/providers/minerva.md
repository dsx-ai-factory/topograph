# Minerva Topology Provider

[NVIDIA Minerva](https://docs.nvidia.com/) exports the physical Layer-2 topology of a fabric — every device, its interfaces, the links between them, and the GPUs attached to server nodes — as a single JSON document via its `export-topology` API.

The Topograph Minerva provider queries this endpoint to build the switch tree consumed by your workload manager, in the same style as the [NetQ provider](./netq.md): it fetches the raw device/link graph, derives switch tiers from each device's role, and reduces Clos fabrics to a canonical tree using `topology.Merger`.

## When to Use This Provider

Use the Minerva provider when Minerva is deployed as the fabric management plane and you want Topograph to source switch-tree topology from it rather than from `ibnetdiscover` or a CSP placement API.

| Scenario | Recommended Topograph provider |
|---|---|
| Minerva deployed and managing the fabric | Minerva |
| Traditional IB fabric, no Minerva | [InfiniBand](./infiniband.md) |
| Spectrum-X fabric, NetQ deployed | [NetQ](./netq.md) |

## Output

The Minerva provider produces a switch tree (`topology/tree`), consumed by whichever engine you configure:

- **Slurm engine** (`engine: slurm`) — writes a `topology.conf` file for Slurm topology-aware scheduling
- **Kubernetes engine** (`engine: k8s`) — applies `fabric.topograph.run/` labels to nodes
- **NFD engine** (`engine: nfd`) — publishes topology as Node Feature Discovery `NodeFeature` and `NodeFeatureGroup` custom resources
- **Slinky engine** (`engine: slinky`) — writes topology data to a Kubernetes ConfigMap

The Minerva `export-topology` response also carries per-server GPU attachments (`GPUs`), but it does not report NVLink domain membership, so this provider does not populate `topology/block`. Topograph selects a single provider per request, so the [DRA provider](./dra.md) can't be combined with Minerva in the same request — DRA derives `topology/block` from an existing `nvidia.com/gpu.clique` node label, but has no way to discover Minerva's switch-fabric data, so it can't augment a Minerva response either. If you need `topology/block` alongside Minerva's `topology/tree`, set the `k8s`/`nfd`/`slinky` engine's `acceleratorDomainSourceLabel` parameter to read accelerator-domain membership directly from that same node label — this is engine-level configuration and works with any provider, including Minerva. See the [Kubernetes engine documentation](../engines/k8s.md#configuration) for details.

See the engine documentation (`docs/engines/`) for details on each output format.

## Prerequisites

- A running NVIDIA Minerva instance accessible from the Topograph host
- An API key authorized to call `POST /v1/export-topology`

## Credentials

| Field | Required | Description |
|---|---|---|
| `apiKey` | Yes | Minerva API key, sent as the `X-Api-Key` header |

## Parameters

| Field | Required | Description |
|---|---|---|
| `apiUrl` | Yes | Base URL of the Minerva API (e.g. `https://minerva.example.com/minerva/api`) |

## Configuration

### Credentials via File

Store credentials in a YAML file:

```yaml
apiKey: <API_KEY>
```

Reference the file in your Topograph config:

```yaml
http:
  port: 49021
  ssl: false

provider: minerva
engine: slurm

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
  }
}
```

## How It Works

1. Calls `POST /v1/export-topology` with a JSON-marshaled empty `ExportTopologyRequest` payload (`{}`) to export the entire fabric — a full-fabric export is a single, consistent snapshot with no client-side paging to manage. The optional page-size request parameter, when set, is forwarded as `limit` to control Minerva's internal pagination batch size.
2. Parses the returned device/link list (`topology.layer2`). Minerva does not report tier numbers, so each device's tier is derived from its `role`, per Minerva's documented standard roles and interface-role pairs: `server` is tier `-1`, `leaf` is tier `0`, `spine` is tier `1`, and `super_spine` is tier `2`. Devices with any other role — including Minerva's deployment-specific "Custom" roles — take no part in the switch tree.
3. Builds the switch tree outward from the requested server nodes (filtered by the incoming `ComputeInstances`), following each device's links to a neighbor at a higher tier — regardless of which of the two devices declared the link in Minerva's response — the same directed-by-tier approach the NetQ provider uses once it has tier data. A requested server with no resolvable switch parent is grouped under a shared `no-topology` switch rather than being dropped or reported as a partial failure.
4. Reduces Clos fabrics — switches that fan out to the same set of children at a given tier — into a canonical tree via `topology.Merger`, exactly as the NetQ provider does.

## Verifying the Output

After triggering topology generation, query the result endpoint:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)

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

For the Slurm engine, verify the generated `topology.conf` reflects the expected switch hierarchy. See the [Slurm engine documentation](../engines/slurm.md) for details.
