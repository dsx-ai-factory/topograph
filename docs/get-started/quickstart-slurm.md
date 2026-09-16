# Install on Slurm (bare metal)

Install Topograph on a Slurm head node so it can generate topology configuration (`topology.conf` or per-partition `topology.yaml`) for the Slurm controller to consume.

## Prerequisites

- **Slurm** cluster with a head node you can install system packages on
- **Go** and **`make`** to build the package from source (see [`go.mod`](https://github.com/dsx-ai-factory/topograph/blob/main/go.mod) for the exact Go version), or a pre-built Debian/RPM package if your organization distributes one
- **A supported provider** for your environment — see the [provider documentation](../providers/) for per-provider setup

## Install

Clone the repo and build a native package for your distribution:

```bash
git clone https://github.com/dsx-ai-factory/topograph.git
cd topograph

make deb        # Debian / Ubuntu — produces .deb under bin/
# or
make rpm        # RHEL / Rocky / SUSE — produces .rpm under bin/
```

Install the resulting package:

```bash
sudo dpkg -i bin/topograph-*.deb        # Debian / Ubuntu
# or
sudo rpm -ivh bin/topograph-*.rpm       # RHEL / Rocky / SUSE
```

The package installs the service but does not start it. Edit `/etc/topograph/topograph-config.yaml` to set at minimum:

```yaml
http:
  port: 49021
provider: <provider>                     # aws, gcp, oci, nebius, nscale, netq, infiniband-bm, ...
engine: slurm
requestAggregationDelay: 15s
```

Then enable and start the service:

```bash
sudo systemctl enable --now topograph.service
```

## Verify

Check that the service is running and the API is reachable:

```bash
curl http://localhost:49021/healthz
```

HTTP 200 means the API server is up.

## Where to go next

- **[Slurm engine reference](../engines/slurm.md)** — full configuration, tree vs block vs per-partition topology formats, `strigger` integration
- **[Provider documentation](../providers/)** — per-provider prerequisites and configuration
- **[Config and API reference](../api.md)** — `topograph-config.yaml` schema, `/v1/generate` and `/v1/topology` contract
- **[`scripts/create-topology-update-script.sh`](https://github.com/dsx-ai-factory/topograph/blob/main/scripts/create-topology-update-script.sh)** — generates the Slurm trigger that calls `/v1/generate` automatically when the cluster's node inventory changes
- **[Architecture](../architecture.md)** — how the API server, Provider, and Engine fit together
