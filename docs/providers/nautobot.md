# Nautobot Topology Provider

The `nautobot` provider starts with fabric switches selected by role, location, and platform,
collects compute endpoints from their cable peers, and follows connected switches
missing from the initial result. It passes the discovered topology in Topograph's
canonical format to the configured engine.

## Prerequisites

- A reachable [Nautobot GraphQL API](https://docs.nautobot.com/projects/core/en/stable/user-guide/platform-functionality/graphql/)
  exposing `devices`, `interfaces(has_cable: true)`, and `cable_peer_interface`.
- An API token with read access to devices, interfaces, roles, statuses, and
  cables throughout the selected fabric. Restricted permissions can hide links
  and produce incomplete topology.
- Complete switch-to-switch and switch-to-compute cabling records. Select only
  the workload fabric; management networks must be outside the selected inventory.
- Unique switch names throughout the discovered fabric, including connected
  switches fetched outside the location and platform filters. Duplicate switch names fail
  topology generation with HTTP 502 and an error naming both device UUIDs,
  preventing ambiguous scheduler output. Excluded-status devices do not count
  toward this uniqueness requirement.

## Credentials

Provide the required token in `provider.creds.token` in a request or through the
credentials file referenced by `credentialsPath`. A credentials file contains a
top-level `token` field and can be mounted from a Kubernetes Secret. The provider
does not read tokens from environment variables. Do not put a working token in
repository configuration files.

The provider sends `Authorization: Token …` to Nautobot on each GraphQL request,
including when the token comes from a mounted Secret. It verifies TLS using the system trust
store, uses a 120-second HTTP timeout, and does not follow redirects. Set the
final GraphQL URL to avoid authentication redirects.
Transport failures return HTTP 502 with the underlying HTTP client error message
to help diagnose connection, DNS, TLS, and timeout failures.

## Parameters

| Field | Required | Description |
|---|---|---|
| `endpoint` | Yes | Nautobot base URL, such as `https://nautobot.example.com`, or the full `/api/graphql/` endpoint, including any deployment prefix. |
| `location` | No | Location names filtering the initial switch query. Omitted means all accessible locations. Connected switches and compute peers may be in other locations. |
| `platform` | No | Platform names filtering the initial switch query. Omitted or empty means all platforms. Connected switches and compute peers may use other platforms. |
| `deviceRole` | Yes | Non-empty list of compute-device roles, such as `GPU` or `CPU`. These classify compute endpoints among switch cable peers; no separate compute-device query is made. |
| `switchRoles` | Yes | Non-empty list of switch roles to select and traverse, such as `Leaf Switch`, `Spine Switch`, and `Core Switch`. Must not overlap `deviceRole`. Role names must match your Nautobot inventory. |
| `excludeStatuses` | No | Excluded device status names, applied to switches and their peers. Omitted defaults to `Deleted`; an explicit empty list or `null` disables exclusion. |
| `trimTiers` | No | Non-negative number of highest fabric tiers to remove; defaults to `0`. |

Switch queries use GraphQL `devices(limit: N, offset: M, ...)` pagination.
Topograph's service configuration `pageSize` sets `N` (default `100`; non-positive
values use that default). The offset starts at `0` and increases by the number
of devices actually returned until a page contains fewer than `N` switches.
An exact multiple requires a final empty
page to confirm completion. Nautobot must support these arguments. A response
exceeding the requested limit logs a warning and discovery continues, advancing
past all returned devices. Keep the inventory stable while discovery is paging
through it.

All initial pages are collected before following switch peers. Subsequent queries
fetch only connected switches missing from those pages, by UUID and `switchRoles`,
without reapplying the location or platform filters. `pageSize` also limits the number of UUIDs
in each expansion batch. A batch stops once all requested UUIDs have been fetched.
Devices with an empty ID or name are skipped with a warning; offsets still count
those records. Repeated valid device IDs or a failed page abort discovery with
HTTP 502; duplicate errors include the repeated ID. Connected switches omitted
after filtering invalid records still fail the expansion consistency check.

## Configuration

Set the service's `provider` to `nautobot` and configure the engine as usual.
Set `pageSize` in the Topograph YAML configuration, not in `provider.params` or
the generation request:

```yaml
pageSize: 100
```

For example, this generation request uses the graph engine and the `creds`
section. Replace the token placeholder at runtime:

```json
{
  "provider": {
    "name": "nautobot",
    "creds": {
      "token": "<NAUTOBOT_TOKEN>"
    },
    "params": {
      "endpoint": "https://nautobot.example.com/api/graphql/",
      "location": ["lab"],
      "platform": ["SONiC"],
      "deviceRole": ["GPU"],
      "switchRoles": ["Leaf Switch", "Spine Switch", "Core Switch"],
      "excludeStatuses": ["Deleted"]
    }
  },
  "engine": {"name": "graph"},
  "nodes": [{
    "region": "local",
    "instances": {"compute-01": "compute-01"}
  }]
}
```

Instance keys may be Nautobot device UUIDs, exact device names, or unique serial
numbers; values are scheduler node names. Ambiguous aliases and nodes with no
cabling match remain in Topograph's standard missing-topology group. Slurm
auto-discovery uses exact node names as instance keys and reports region `local`.
For Kubernetes engines, supply the node instance/region annotations described in
the [node annotation reference](../reference/node-labels.md); this provider does
not add a node-data-broker discovery mode.

## How It Works

1. Query switches using `location`, `platform`, `switchRoles`, and status exclusions.
   The `switchRoles` parameter maps to Nautobot's GraphQL `role` filter. Fetch
   each switch's cabled interfaces and peer device IDs, names, serials, statuses,
   and roles. Collect compute peers whose roles match `deviceRole`, excluding
   configured statuses. Repeated compute peers (for example, dual-homed hosts)
   are deduplicated by UUID; conflicting names or serials fail discovery.
2. Collect switch peers missing from the initial result, fetch their interfaces
   by UUID, and repeat until no unvisited switches remain. Each switch is fetched
   once, so cycles and redundant cables terminate naturally.
   Traversal follows all connected configured switches, including lateral links;
   it does not assume switch roles encode a tier. Unknown roles, routers outside
   `switchRoles`, and excluded-status devices are not traversed or treated as
   compute endpoints. All eligible compute peers of the discovered switches seed
   inference, regardless of their location or platform. Uncabled compute devices are absent
   from discovery and remain in the standard missing-topology group if requested.
   Missing expected switch records fail discovery instead of silently truncating
   the fabric. HTTP failures, malformed responses, missing data, and GraphQL errors
   return HTTP 502; invalid configuration returns HTTP 400.
3. Assign tier 0 to switches connected to compute endpoints. Infer higher tiers
   using minimum switch-hop distance from **all** selected compute endpoints,
   so requesting a subset of nodes does not change the hierarchy. Same-tier
   links do not create parent relationships.
4. Reduce redundant paths to a tree: same-tier switches with shared children
   are grouped, including transitive overlaps. This handles dual-homed hosts and
   folded Clos fabrics conservatively. A group's lowest UUID and its device name
   represent the group; it is a locality group rather than a selected cable path.
5. Populate closest-first fabric tiers and let Topograph build the canonical
   graph. No accelerator domain is inferred from racks or device models.

Tier inference assumes a layered fabric with compute endpoints attached at the
leaves. Mixed management/data networks, non-layered meshes, or incomplete cabling
can invalidate that assumption. Missing or unconfigured roles stop traversal;
configure every switch role needed to reach the core. Grouping
overlapping paths can coarsen locality, and representative UUIDs can change when
group membership changes. Validate the inferred tree against your inventory
before using it for scheduling.
