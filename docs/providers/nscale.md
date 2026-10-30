# Nscale Topology Provider

The `nscale` topology provider reads topology data from the Nscale Radar API and converts it into Topograph's canonical three-tier topology graph.

The provider uses two Nscale data sources:

- **Radar API**: returns each instance's network path via `GET /v2/topology`
- **Instance Metadata Service (IMDS)**: each node exposes its own server ID and region at `http://169.254.169.254/openstack/latest/meta_data.json`

The Radar response supplies the provider server ID, switch path, and optional block ID. For Slurm auto-discovery, the provider reaches each Slurm node over `pdsh` (SSH-based) and queries that node's own IMDS endpoint to build the server-ID-to-hostname map. If the `region` credential is set, nodes whose IMDS region does not match it are excluded from the topology query.

## When to Use This Provider

Use this provider for Nscale environments where Radar is the topology source. It is most commonly used with the Slurm engine to generate `topology.conf` from the current Slurm node list.

If the request payload supplies explicit `nodes`, Topograph uses those server ID to node name mappings directly. If `nodes` is omitted and the Slurm engine is used, Topograph runs `scontrol show nodes -o` to get the Slurm node list, then uses `pdsh` to query each node's local IMDS endpoint for its server ID (`serverID`) and region (`regionID`), merging the results into a single server-ID-to-hostname map.

## Prerequisites

- A Radar API endpoint reachable from the Topograph host
- An Nscale organization ID
- An API token with permission to read Radar topology data
- For Slurm auto-discovery: `scontrol` and `pdsh` must be available to the Topograph process, with SSH access to every Slurm node, and each node must be able to reach the configured `imdsUrl` endpoint (`169.254.169.254` by default; see [Parameters](#parameters))

## Credentials

| Field | Required | Description |
|---|---|---|
| `org` | Yes | Nscale organization ID |
| `token` | Yes | Bearer token used for Radar API requests |
| `region` | No | Restricts Slurm auto-discovery to nodes whose IMDS region (`regionID`) matches. Nodes with a different or missing IMDS region are excluded from the topology query and logged as a warning. Unset means no filtering |

Store credentials in a YAML file:

```yaml
org: <ORGANIZATION_ID>
token: <API_TOKEN>
region: <REGION_ID>
```

Reference that file from the Topograph config:

```yaml
credentialsPath: /etc/topograph/nscale-credentials.yaml
```

Credentials can also be supplied directly in the topology request payload under `provider.creds`.

## Parameters

| Field | Required | Description |
|---|---|---|
| `radarApiUrl` | Yes | Base URL for the Radar API, for example `https://radar.example.com` |
| `trimTiers` | No | Number of highest topology tiers to trim from output. Defaults to `0` |
| `imdsUrl` | No | Override for the IMDS URL queried by Slurm auto-discovery (`pdsh` on each Slurm node) and by the node-data-broker's own per-node self-query on Kubernetes. Defaults to `http://169.254.169.254/openstack/latest/meta_data.json` |

The top-level Topograph `pageSize` setting controls pagination for the Radar topology request.

## Configuration

Example Topograph config for Slurm:

```yaml
http:
  port: 49021
  ssl: false

provider: nscale
engine: slurm

requestAggregationDelay: 15s
credentialsPath: /etc/topograph/nscale-credentials.yaml

providerParams:
  radarApiUrl: https://radar.example.com

engineParams:
  plugin: topology/tree
  topologyConfigPath: /etc/slurm/topology.conf
```

Example request payload:

```json
{
  "provider": {
    "name": "nscale",
    "creds": {
      "org": "<ORGANIZATION_ID>",
      "token": "<API_TOKEN>",
      "region": "<REGION_ID>"
    },
    "params": {
      "radarApiUrl": "https://radar.example.com"
    }
  },
  "engine": {
    "name": "slurm",
    "params": {
      "plugin": "topology/tree"
    }
  }
}
```

If you already have the server ID to hostname mapping, you can include it explicitly:

```json
{
  "provider": {
    "name": "nscale",
    "creds": {
      "org": "<ORGANIZATION_ID>",
      "token": "<API_TOKEN>",
      "region": "<REGION_ID>"
    },
    "params": {
      "radarApiUrl": "https://radar.example.com"
    }
  },
  "engine": {
    "name": "slurm"
  },
  "nodes": [
    {
      "region": "<REGION_ID>",
      "instances": {
        "<SERVER_ID_1>": "node001",
        "<SERVER_ID_2>": "node002"
      }
    }
  ]
}
```

## How It Works

For each region in the compute instance list, the provider fetches topology pages from Radar:

```text
GET <radarApiUrl>/v2/topology?limit=<pageSize>&offset=<offset>
Authorization: Bearer <token>
X-Organization: <org>
X-Region: <region>
```

Each returned instance is translated as follows:

| Radar field | Topograph field |
|---|---|
| `server_id` | Server ID |
| `network_node_path[0]` | Core tier |
| `network_node_path[1]` | Spine tier |
| `network_node_path[2]` | Leaf tier |
| `block_id` | Accelerator / NVLink domain |

For Slurm auto-discovery, the provider runs `scontrol show nodes -o` to get the current Slurm node list, then uses `pdsh` to run the following on every node in that list:

```bash
res=$(curl -fsS -- http://169.254.169.254/openstack/latest/meta_data.json) && echo "$res"
```

A topology request runs this single `pdsh` sweep once, and its result builds both the server-ID-to-hostname map and the region map. Each node's response is a JSON document with a `meta` map. The provider extracts two fields per node:

| IMDS `meta` field | Purpose |
|---|---|
| `serverID` | Server ID, used as the key in the server-ID-to-hostname map and matched against the Radar API's `server_id` field |
| `regionID` | Region, used to group nodes for region-scoped Radar topology requests |

Nodes that omit `serverID`, or whose IMDS response fails to parse, are dropped from the map; a node's absence from `regionID` simply omits it from the region map. If the `region` credential is set, nodes whose `regionID` does not match it (including nodes missing `regionID`) are excluded from both maps entirely, with a warning logged per excluded node.

## Verifying the Output

You can reproduce the same server-ID-to-hostname map Topograph builds by running the equivalent `pdsh` command directly. Replace `imds_url` below with the configured `imdsUrl` parameter if you override the default. This command is **unfiltered** — it does not apply the `region` credential's filtering, so if `region` is set, Topograph's actual map may exclude nodes this command still lists; cross-check those nodes' `regionID` against the configured `region` by hand if needed:

```bash
set -uo pipefail

imds_url=http://169.254.169.254/openstack/latest/meta_data.json

slurm_nodes=$(scontrol show nodes -o | grep -oE 'NodeName=[^ ]+' | cut -d= -f2 | sort -u)
status=$?
[ "$status" -eq 0 ] && [ -n "$slurm_nodes" ] || { echo "FAIL: scontrol returned no usable node list (exit $status)"; exit 1; }

mappings=$(pdsh -R ssh -w "$(echo "$slurm_nodes" | paste -sd,)" \
  "res=\$(curl -fsS -- '$imds_url') && echo \"\$res\"" \
  | while IFS=: read -r node json; do
      server_id=$(echo "$json" | jq -er '.meta["serverID"] // empty' 2>/dev/null)
      if [ -z "$server_id" ]; then
        echo "WARN: $node did not return a valid serverID, skipping" >&2
        continue
      fi
      printf '%s\t%s\n' "$server_id" "$node"
    done)

[ -n "$mappings" ] || { echo "FAIL: no usable serverID-to-node mappings produced"; exit 1; }
printf '%s\n' "$mappings"
```

A node that fails to respond, or whose IMDS response is missing `serverID`, is silently dropped from the generated topology rather than causing an error — run this check before relying on Slurm auto-discovery.

Then trigger topology generation:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

For the Slurm engine, verify that the generated `topology.conf` contains the expected switch hierarchy or block topology for the Nscale instances.
