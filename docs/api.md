# Configuration and API

## Configuration

Topograph accepts its configuration file path using the `-c` command-line parameter. The configuration file is a YAML document. A sample configuration file is located at `config/topograph-config.yaml`.

The configuration file supports the following parameters:

```yaml
# serving topograph endpoint
http:
  # port: specifies the port on which the API server will listen (required).
  port: 49021
  # ssl: enables HTTPS protocol if set to `true` (optional).
  ssl: false

# provider: the provider that topograph will use (optional)
# Valid options include "aws", "oci", "gcp", "nebius", "nscale", "netq", "dra", "infiniband-k8s", "infiniband-bm" or "test".
# Can be overridden if the provider is specified in a topology request to topograph
provider: test

# engine: the engine that topograph will use (optional)
# Valid options include "slurm", "k8s", "slinky", or "graph".
# Can be overridden if the engine is specified in a topology request to topograph
engine: slurm

# requestAggregationDelay: defines the delay before processing a request (required).
# Topograph aggregates multiple sequential requests within this delay into a single request,
# processing only if no new requests arrive during the specified duration.
requestAggregationDelay: 15s

# pageSize: sets the page size for topology requests against a CSP API (optional).
# Non-positive values emit a warning and use the provider default.
pageSize: 100

# ssl: specifies the paths to the TLS certificate, private key,
# and CA certificate (required if `http.ssl=true`).
ssl:
  cert: /etc/topograph/ssl/server-cert.pem
  key: /etc/topograph/ssl/server-key.pem
  ca_cert: /etc/topograph/ssl/ca-cert.pem

# credentialsPath: specifies the path to a YAML file containing API credentials (optional).
# When using credentials in Kubernetes-based engines ("k8s" or "slinky"),
# the secret file must be named `credentials.yaml`. For example:
# `kubectl create secret generic <secret-name> --from-file=credentials.yaml=<path to credentials>`
# For more details about credential configuration, refer to the docs/providers section.
# credentialsPath:

# env: environment variable names and values to inject into Topograph's shell (optional).
# The `PATH` variable, if provided, will append the specified value to the existing `PATH`.
# env:
#  SLURM_CONF: /etc/slurm/slurm.conf
#  PATH:
```

## API

Topograph exposes five endpoints for interacting with the service. Below are the details of each endpoint:

### 1. Health Endpoint

- **URL:** `GET http://<server>:<port>/healthz`
- **Description:** This endpoint verifies the service status. It returns a "200 OK" HTTP response if the service is reachable.

### 2. Topology Request Endpoint

- **URL:** `POST http://<server>:<port>/v1/generate`
- **Description:** This endpoint is used to request a new cluster topology.
- **Payload:** The request body is a JSON object organized into three top-level sections:

  - **provider**: (optional) Selects the topology source and provides any provider-specific authentication or parameters.
    - **name**: (optional) A string specifying the Service Provider, such as `aws`, `oci`, `gcp`, `nebius`, `nscale`, `netq`, `dra`, `infiniband-k8s`, `infiniband-bm` or `test`. This parameter will override the provider set in the topograph config.
    - **creds**: (optional) A key-value map with provider-specific parameters for authentication.
    - **params**: (optional) A key-value map with provider-specific parameters. The `test` provider uses these parameters for response simulation; for complete behavior and examples, see [Test Mode and Test Provider](./providers/test.md).
      - **accelerator**: (optional) Used in: [`dra`, `infiniband-k8s`, `infiniband-bm`]. Configures accelerator-domain discovery independently of network-fabric discovery. For InfiniBand, omitting this section or setting it to an empty object disables accelerator-domain discovery. DRA supports only `kubernetes-label` and retains its legacy `nvidia.com/gpu.clique` default when the section is omitted.
        - **source**: (required when `accelerator` is non-empty) `nvidia-smi`, `kubernetes-label` (`dra` and `infiniband-k8s`), or `none`. DRA accepts only `kubernetes-label`.
        - **kubernetesLabel.key**: (required for `kubernetes-label`) Kubernetes Node label read as the accelerator-domain ID. No default is assumed for an explicit section.
        - For `infiniband-k8s`, a request with `source: nvidia-smi` reads accelerator-domain annotations previously collected by the node-data-broker. The request does not run `nvidia-smi` or reconfigure the broker. Deploy the broker with the same accelerator source before sending the request; see [Helm node-data-broker settings](./providers/infiniband.md#helm-node-data-broker-settings).
  - **engine**: (optional) Selects the topology output and provides any engine-specific parameters.
    - **name**: (optional) A string specifying the topology output, either `slurm`, `k8s`, `nfd`, `slinky`, or `graph`. This parameter will override the engine set in the topograph config.
    - **params**: (optional) A key-value map with engine-specific parameters.
      - **plugin**: (optional) Used in: [`slurm`, `slinky`]. A string specifying the cluster-wide topology plugin: `topology/tree` or `topology/block`. For `slurm`, this defaults to `topology/tree` when neither `plugin` nor `topologies` is set. Do not set `plugin` together with `topologies`.
      - **blockSizes**: (optional) Used in: [`slurm`, `slinky`]. An array of block sizes for `topology/block`.
      - **blockName**: (optional) Used in: [`slurm`, `slinky`]. Derives names for `topology/block` blocks from their node names. If specified, both nested fields are required. A block whose nodes don't all match `nodeNameRegexp`, or whose nodes expand to different or empty names, is dropped from the output with a warning naming the domain; the rest of the topology is still generated. Two different blocks that expand to the same name is a hard failure, since it's ambiguous which one is misconfigured. Empty complemented blocks retain their generated names.
        - **nodeNameRegexp**: (required with `blockName`) A Go regular expression applied to every node name in the block. The expression is not implicitly anchored.
        - **format**: (required with `blockName`) A Go regexp expansion template used to construct the block name from capture groups, for example `domain${1}_rack${2}`.
      - **topologyConfigPath**: Used in: [`slurm`, `slinky`, `graph`]. Optional for `slurm` and `graph`; required for `slinky`. For `slurm`, a file path for the topology configuration; if omitted, the topology config content is returned in the HTTP response. For `slinky`, the key for the topology config in the ConfigMap. For `graph`, an existing path on the Topograph host where instance JSON should be written; if omitted, the JSON is returned in the topology response.
      - **topologies**: (optional) Used in: [`slurm`, `slinky`]. A map of named per-partition topology settings. Do not set top-level `plugin` together with `topologies`.
        - **plugin**: Used in: [`slurm`, `slinky`]. A required string specifying the per-partition topology plugin: `topology/tree`, `topology/block`, or `topology/flat`.
        - **blockSizes**: (optional) Used in: [`slurm`, `slinky`]. An array of block sizes for `topology/block`.
        - **blockName**: (optional) Used in: [`slurm`, `slinky`]. Per-topology form of `blockName`, with the required nested `nodeNameRegexp` and `format` fields.
        - **nodes**: (optional) Used in: [`slurm`, `slinky`]. An explicit list of SLURM nodes for this topology. If omitted, Topograph can discover membership from `podSelector` (`slinky` only) or `partition`.
        - **partition**: (optional) Used in: [`slurm`, `slinky`]. A SLURM partition name used to discover nodes with `scontrol show partition` when `nodes` is not set. For `slinky`, this fallback is used only when the topology entry does not set `podSelector`.
        - **podSelector**: (optional) Used in: [`slinky`]. A Kubernetes label selector for slurmd pods in this partition. `nodes` and `podSelector` are mutually exclusive on the same topology entry.
        - **clusterDefault**: (optional) Used in: [`slurm`, `slinky`]. If `true`, marks this topology as the default for nodes not assigned to another topology; commonly used with `plugin: topology/flat`.
      - **reconfigure**: (optional) Used in: [`slurm`]. If `true`, invoke `scontrol reconfigure` after topology config is generated. Default `false`.
      - **namespace**: Used in: [`slinky`]. The required namespace where the SLURM cluster is running. The NFD namespace is deployment-scoped and cannot be supplied in a topology request; Helm deployments configure it with the top-level `nfdNamespace` value.
      - **podSelector**: Used in: [`slinky`]. A required Kubernetes label selector for pods running SLURM nodes.
      - **nodeSelector**: (optional) Used in: [`k8s`, `nfd`, `slinky`]. A Kubernetes node label map that filters which nodes participate in topology generation.
      - **fabricLabels**: (optional) Used in: [`k8s`]. Closest-first array of Kubernetes label keys for fabric tiers. If omitted, every discovered fabric tier uses its default numbered key; if provided, tiers beyond the array are omitted.
      - **acceleratorLabel**: (optional) Used in: [`k8s`]. Kubernetes label key for the accelerator domain. Defaults to `accelerator.topograph.run/domain`. The optional accelerator sub-domain always uses `accelerator.topograph.run/sub-domain`.
      - **acceleratorDomainSourceLabel**: (optional) Used in: [`k8s`, `nfd`, `slinky`]. Existing Kubernetes Node label used as the authoritative accelerator-domain source. There is no default. Source-label overrides suppress provider accelerator sub-domains. For `k8s` and `nfd`, nodes without a non-empty source value retain their provider-derived accelerator domains. For Slinky block topology, those nodes are excluded from label-backed domains; generation fails if no usable label-backed domains can be constructed. For `k8s`, this parameter cannot be set together with `acceleratorLabel`, and the source label is never overwritten.
      - **topologyConfigmapName**: Used in: [`slinky`]. The required name of the ConfigMap containing the topology config.
      - **useDynamicNodes**: (optional) Used in: [`slinky`]. If `true`, Kubernetes nodes matched by the Node Selector will be annotated with the topology spec.
      - **configUpdateMode**: (optional) Used in: [`slinky`]. By default, the full topology YAML is written in the Slurm ConfigMap. `skeleton-only` overrides to include switches or blocks only (no node lines); `none` skips updating the topology key in the ConfigMap.
  - **nodes**: (optional) Supplies the cluster nodes used for topology generation as an array of regions mapping instance IDs to node names.

Kubernetes client rate limits are deployment settings, not topology request
parameters. Helm deployments configure them with `kubeClient.qps` and
`kubeClient.burst`; the chart exposes those values to Topograph as `KUBE_QPS`
and `KUBE_BURST`.

  Example:

```json
{
  "provider": {
    "name": "aws",
    "creds": {
      "accessKeyId": "id",
      "secretAccessKey": "secret"
    }
  },
  "engine": {
    "name": "slurm",
    "params": {
      "plugin": "topology/block",
      "blockSizes": [30, 120]
    }
  },
  "nodes": [
    {
      "region": "region1",
      "instances": {
        "instance1": "node1",
        "instance2": "node2",
        "instance3": "node3"
      }
    },
    {
      "region": "region2",
      "instances": {
        "instance4": "node4",
        "instance5": "node5",
        "instance6": "node6"
      }
    }
  ]
}
```

- **Response:** This endpoint immediately returns a "202 Accepted" status with a unique request ID if the request is valid. If not, it returns an appropriate error code.

### 3. Topology Result Endpoint

- **URL:** `GET http://<server>:<port>/v1/topology`
- **Description:** This endpoint retrieves the result of a topology request.
- **URL Query Parameters:**
  - **uid**: Specifies the request ID returned by the topology request endpoint.
- **Response:** Depending on the request's execution stage, this endpoint can return:
  - "200 OK" - The request has completed successfully.
  - "202 Accepted" - The request is still in progress and has not completed yet.
  - "404 Not Found" - The specified request ID does not exist.
  - Other error responses encountered by Topograph during request execution.

Example usage:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)

curl -s "http://localhost:49021/v1/topology?uid=$id"
```

### 4. Topology Lookup Endpoint

- **URL:** `POST http://<server>:<port>/v1/lookup`
- **Description:** This endpoint retrieves the result of a topology request identified by its payload rather than by request ID. Topograph derives the request ID from the provider name and parameters and the engine name and parameters; credentials and the node list are not part of it. A client that still holds the payload can therefore read the result without storing the ID returned by the topology request endpoint. This endpoint never starts a new topology generation.
- **Payload:** The same JSON object accepted by the topology request endpoint. Provider and engine names omitted from the payload fall back to the values in the Topograph config before the request ID is computed, exactly as they do for `/v1/generate`.
- **Response:** The same status codes as the topology result endpoint:
  - "200 OK" - The matching request has completed successfully, and the topology is returned in the body.
  - "202 Accepted" - The matching request is still in progress.
  - "404 Not Found" - No request with this payload has been submitted.
  - Other error responses encountered by Topograph during request execution.

Example usage:

```bash
curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/lookup
```

### 5. Metrics Endpoint

- **URL:** `GET http://<server>:<port>/metrics`
- **Description:** This endpoint serves Prometheus metrics for the API server on the same port as the rest of the API. Exported metrics are `topograph_version`, `topograph_http_request_duration_seconds`, `topograph_request_duration_seconds`, `topograph_missing_topology`, and `topograph_validation_error_total`.
