# NetQ Topology Provider

[NVIDIA NetQ](https://docs.nvidia.com/networking-ethernet-software/cumulus-netq/) collects telemetry from Ethernet switches, DPUs, hosts, and NVLink fabrics, normalizes it, and streams it into a central analytics layer where metrics, events, and topology data are correlated in near real time. It exposes this processed data through a unified monitoring and operations platform supporting alerting, validation, and visibility for large-scale GPU and Ethernet/NVLink environments.

The Topograph NetQ provider queries the NetQ API to extract fabric topology and NVLink domain data, translating it into the format expected by your workload manager. It builds both a switch tree (for Slurm `topology/tree` or Kubernetes labels) and an NVLink domain map (for `topology/block`).

Topology discovery scope is determined by the NetQ server and the premises accessible to the configured account. No CSP credentials are required.

## When to Use This Provider

**Spectrum-X environments**: NetQ is the standard management plane for Spectrum-X and is the recommended provider — it has authoritative, real-time visibility into the fabric that `ibnetdiscover`-based approaches cannot provide.

**Multi-node NVLink (MNNVL) environments**: NetQ includes NVLink Management (previously packaged as NMX-M), which provides native visibility into NVLink fabric topology, domain membership, and partitions at the infrastructure level. The [DRA provider](./dra.md) is a narrower option for Slinky `topology/block`: it groups nodes using pre-existing `nvidia.com/gpu.clique` labels but does not discover the backend switch fabric between NVLink partitions. Use NetQ when workloads can span partitions and placement must account for network proximity.

**Traditional IB environments**: If NetQ is already deployed and managing your IB fabric, use this provider to leverage its existing topology data. If NetQ is not present, use the [InfiniBand provider](./infiniband.md) instead.

| Scenario | Recommended Topograph provider |
|---|---|
| Spectrum-X fabric | NetQ |
| MNNVL fabric, including cross-partition locality | NetQ |
| MNNVL with Slinky, single-partition workloads, and `nvidia.com/gpu.clique` present | [DRA](./dra.md) |
| Traditional IB fabric, NetQ deployed | NetQ |
| Traditional IB fabric, no NetQ | `infiniband-bm` or `infiniband-k8s` |

## Observed vs. Intended Topology

The NetQ provider reports what the fabric actually looks like right now, not what configuration files say it should look like. Because NetQ exposes live telemetry, Topograph can observe link states below the hard-failure threshold — degraded links that are still technically up but impacting performance. That signal is invisible to `ibnetdiscover`-based discovery and unreported by any cloud placement API. At scale, where nodes cycle continuously and link degradation is a constant background rate, this observed-topology view is substantively different from the static view that hand-maintained labels or placement snapshots provide.

## Output

The NetQ provider produces the same topology representation as the InfiniBand providers, consumed by whichever engine you configure:

- **Slurm engine** (`engine: slurm`) — writes a `topology.conf` file for Slurm topology-aware scheduling
- **Kubernetes engine** (`engine: k8s`) — applies `fabric.topograph.run/` labels to nodes
- **NFD engine** (`engine: nfd`) — publishes topology as Node Feature Discovery `NodeFeature` and `NodeFeatureGroup` custom resources
- **Slinky engine** (`engine: slinky`) — writes topology data to a Kubernetes ConfigMap

See the engine documentation (`docs/engines/`) for details on each output format.

## Prerequisites

- A running NetQ server accessible from the Topograph host
- A NetQ account with access to at least one premises with topology data

## Credentials

| Field | Required | Description |
|---|---|---|
| `username` | Yes | NetQ account username |
| `password` | Yes | NetQ account password |

## Parameters

| Field | Required | Description |
|---|---|---|
| `apiUrl` | Yes | Base URL of the NetQ server (e.g. `https://netq.example.com`) |

## Configuration

### Credentials via File

Store credentials in a YAML file:

```yaml
username: <USERNAME>
password: <PASSWORD>
```

Reference the file in your Topograph config:

```yaml
http:
  port: 49021
  ssl: false

provider: netq
engine: slurm

credentialsPath: /path/to/credentials.yaml
```

### Credentials via API Request Payload

Pass credentials directly in the topology request:

```json
{
  "provider": {
    "name": "netq",
    "creds": {
      "username": "<USERNAME>",
      "password": "<PASSWORD>"
    },
    "params": {
      "apiUrl": "https://netq.example.com"
    }
  },
  "engine": {
    "name": "slurm"
  }
}
```

## How It Works

The provider makes two independent API calls and combines their results:

**Switch tree (topology/tree):**
1. Authenticates via `POST api/netq/auth/v1/login` to obtain an access token and list of premises
2. For each premises with topology data, selects it via `GET api/netq/auth/v1/select/opid/{opid}`
3. Fetches the fabric topology graph via `POST api/netq/telemetry/v1/object/topologygraph/fetch-topology`
4. Parses the tier-based node and link graph into a switch tree; Clos topologies are reduced to a canonical tree representation

**NVLink domains (topology/block):**
1. Fetches compute node records via `GET nmx/v1/compute-nodes` using Basic auth — the `nmx` path reflects the NetQ NVLink Management API, previously known as NMX-M
2. Groups nodes by `DomainUUID` to build the NVLink domain map. The `DomainUUID` is a NetQ/NMX identifier and differs in format from the `ClusterUUID.CliqueId` value used by `nvidia.com/gpu.clique` and the InfiniBand provider — both identify the same physical NVLink domain, but the values are not directly comparable as strings.

Authorization headers use the shared
[HTTP authentication helpers](../architecture.md#shared-http-authentication).
The provider retains its typed `Credentials` struct and uses `ToMap()` to create
a fresh `httpauth.Credentials` map when building the login request. It delegates token
HTTP requests and extraction to `httpauth.AcquireBearerToken`, retaining the login
and premises-selection sequence. Both token requests use the shared HTTP retry
policy for transient failures. HTTP errors are returned directly from the shared
HTTP helper, with redacted execution messages. Both
responses must contain a nonempty string `access_token`; malformed or missing
tokens return HTTP 502. Token responses are limited to 1 MiB and redirects are
rejected. NVLink Management requests continue to use Basic authentication with
the same account credentials.

NVLink domain discovery is best-effort — if it fails, Topograph logs a warning and returns the switch tree only. Multi-premises environments are supported: Topograph iterates over all accessible premises and merges their topology graphs.

## Verifying the Output

After triggering topology generation, query the result endpoint:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

For the Slurm engine, verify the generated `topology.conf` reflects the expected switch hierarchy. See the [Slurm engine documentation](../engines/slurm.md) for details.
