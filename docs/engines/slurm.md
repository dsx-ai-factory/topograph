# Topograph with SLURM

For the SLURM engine, topograph supports [tree](https://slurm.schedmd.com/topology.conf.html#SECTION_topology/tree) and [block](https://slurm.schedmd.com/topology.conf.html#SECTION_topology/block) topology configurations.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="../assets/topograph-slurm-topology-formats-community-dark.png" />
    <img src="../assets/topograph-slurm-topology-formats-community-light.png" width="100%" alt="Topograph Slurm topology formats diagram" />
  </picture>
</p>

## Automatic block-size inference

When `blockSizes` is not configured, Topograph derives it from the discovered
accelerator domains.

For single-level topology, let `D` be the smallest discovered accelerator-domain
size and `N` be the number of discovered domains. Topograph emits a doubling
sequence through the largest power-of-two group that fits within `N` domains:

```text
blockSizes = [D, 2D, 4D, ..., 2^k * D]
k = floor(log2(N))
```

This can produce more than one block size as the cluster grows:

| Smallest domain (`D`) | Domain count (`N`) | Inferred `blockSizes` |
|---:|---:|---|
| 8 | 1 | `[8]` |
| 8 | 2 | `[8, 16]` |
| 8 | 3 | `[8, 16]` |
| 8 | 4 | `[8, 16, 32]` |
| 8 | 8 | `[8, 16, 32, 64]` |

For topology containing accelerator sub-domains, Topograph:

1. Finds the largest sub-domain size, `maxSubDomainSize`.
2. Finds the largest total accelerator-domain size, `maxDomainSize`.
3. Starting with `maxSubDomainSize`, repeatedly doubles it until it is at least
   `maxDomainSize`.

The resulting aggregate size is:

```text
aggregateSize = maxSubDomainSize * 2^n
```

where `n` is the smallest non-negative integer for which
`aggregateSize >= maxDomainSize`. The inferred result is:

```text
blockSizes = [maxSubDomainSize, aggregateSize]
```

If `aggregateSize` equals `maxSubDomainSize`, only one value is emitted.

| Largest sub-domain | Largest domain | Inferred `blockSizes` |
|---:|---:|---|
| 8 | 8 | `[8]` |
| 8 | 12 | `[8, 16]` |
| 8 | 16 | `[8, 16]` |
| 9 | 135 | `[9, 144]` |

The inferred sizes are used both to construct the complemented block tree and
to emit the final `BlockSizes` value. Explicitly configured `blockSizes` always
take precedence over inference.

## Deriving block names from node names

For `topology/block`, the optional `blockName` engine parameter derives each block name from the names of its nodes. Both `nodeNameRegexp` and `format` are required when `blockName` is set.

```yaml
engine:
  name: slurm
  params:
    plugin: topology/block
    blockSizes: [8, 16]
    blockName:
      nodeNameRegexp: 'd([0-9]{2})-r([0-9]{2})'
      format: 'domain${1}_rack${2}'
```

For a block containing nodes such as `gpu-d05-r04-srv4`, this produces the name `domain05_rack04`. The expression uses Go regular-expression syntax and may match anywhere in the node name; use `^` or `$` when the site naming convention requires anchoring. The format uses Go regexp expansion syntax, including numeric captures such as `${1}` and named captures such as `${domain}`.

Every node in a non-empty block must match the expression and produce the same non-empty block name. A block where a node doesn't match, whose nodes disagree on the derived name, or whose formatted name comes out empty is dropped from the output with a warning naming the domain, and the rest of the topology is still generated. If every block ends up dropped this way, a per-partition topology falls back to a flat topology, while a cluster-wide `topology/block` request returns an error, since there is no flat output for it to fall back to. Different blocks must produce unique names — that case is still rejected outright, since it's ambiguous which block is misconfigured. Empty complemented blocks have no node name to evaluate and retain their generated default name.

The option can also be set on each `topologies` entry for per-partition output.

### Test Provider and Engine
There is a special *provider* and *engine* named `test`, which supports both SLURM and Kubernetes. This configuration returns static results and is primarily used for testing purposes.

## Installation and Configuration
Topograph can be installed using the `topograph` Debian or RPM package. This package sets up a service but does not start it automatically, allowing users to update the configuration before launch.

The configuration file and certificates created by the installer are located in the /etc/topograph directory.

#### Service Management
To enable and start the service, run the following commands:
```bash
systemctl enable topograph.service
systemctl start topograph.service
```

Upon starting, the service executes:
```bash
/usr/local/bin/topograph -c /etc/topograph/topograph-config.yaml
```

To disable and stop the service, run the following commands:
```bash
systemctl stop topograph.service
systemctl disable topograph.service
systemctl daemon-reload
```

#### Verifying Health
To verify the service is healthy, you can use the following command:

```bash
curl http://localhost:49021/healthz
```

#### Automated Solution for SLURM

The Cluster Topology Generator enables a fully automated solution when combined with SLURM's `strigger` command. You can set up a trigger that runs whenever a node goes down or comes up:

```bash
strigger --set --node --down --up --flags=perm --program=<script>
```

In this setup, the `<script>` would contain the curl command to call the endpoint:

```bash
curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate
```

We provide `scripts/create-topology-update-script.sh` in the repository, which performs the steps outlined above: it creates the topology update script and registers it with the strigger.

The script accepts the following parameters:
- **provider name** (`aws`, `gcp`, `oci`, `nebius`, `netq`, `nscale`, `lambdai`, or `infiniband-bm`)
- **path to the generated topology update script**
- **path to the topology.conf file**

Usage:
```bash
create-topology-update-script.sh -p <provider name> -s <topology update script> -c <path to topology.conf>
```

Example:
```bash
create-topology-update-script.sh -p aws -s /etc/slurm/update-topology-config.sh -c /etc/slurm/topology.conf
```

This automation ensures that your cluster topology is updated and SLURM configuration is reloaded whenever there are changes in node status, maintaining an up-to-date cluster configuration.
