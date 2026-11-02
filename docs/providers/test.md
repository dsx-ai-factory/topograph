# Test Mode and Test Provider

Topograph test mode uses the `test` provider to simulate topology-generation responses without querying a cloud API, NetQ, InfiniBand fabric, or Kubernetes labels. It is intended for integration and regression testing of downstream components that consume Topograph output.

Use test mode when you need to verify how a client handles successful topology generation, delayed topology generation, unknown request IDs, malformed requests, and simulated provider failures. This is especially useful for preventing regressions where an unhandled Topograph error causes a downstream system to discard a previously valid topology configuration.

## Run Topograph in Test Mode

Set the default provider to `test` in `topograph-config.yaml`, and choose the engine whose output your client consumes:

```yaml
http:
  port: 49021
  ssl: false

provider: test
engine: slurm

requestAggregationDelay: 2s
```

Then start Topograph:

```sh
make build
bin/topograph -c config/topograph-config.yaml
```

You can also leave the default provider and engine unset and specify them in each `/v1/generate` request payload. This is useful when one regression suite needs to exercise multiple engines.

Model files can be referenced by basename, such as `small-tree.yaml`, to load one of the embedded fixtures from `tests/models/`. You can also provide an absolute or relative path to a YAML model file.

## API Flow

Topology generation uses two API endpoints.

### `/v1/generate`

`POST /v1/generate` starts a topology-generation request.

Possible responses:

| Response | Meaning | Client guidance |
|---|---|---|
| `202 Accepted` | The request was accepted. The response body contains the request ID. | Poll `/v1/topology?uid=<request-id>`. |
| `4xx` | The request is invalid or cannot be accepted. | Do not retry the same request without changing it. Investigate the payload and configuration. |
| `5xx` | Topograph returned a server-side failure. | Retry the generate request according to the client's retry policy. |

### `/v1/topology`

`GET /v1/topology?uid=<request-id>` retrieves the result for a previously accepted request.

Possible responses:

| Response | Meaning | Client guidance |
|---|---|---|
| `200 OK` | Topology generation completed. The response body contains the engine output. | Consume the returned topology. |
| `202 Accepted` | The request is still queued, still processing, or intentionally simulated as pending. | Retry for a bounded period. Topology discovery should normally finish within about 2 minutes. |
| `404 Not Found` | The request ID is unknown or no longer in the request history. | Do not retry the same request ID. Submit a new `/v1/generate` request. |
| Other errors | Topology generation failed. | Do not retry the same `/v1/topology` request indefinitely. Submit a new `/v1/generate` request if the client policy allows it. |

Topograph internally retries topology processing up to 5 attempts, with exponential backoff starting at 2 seconds, for these retryable HTTP status codes:

- `408 Request Timeout`
- `429 Too Many Requests`
- `500 Internal Server Error`
- `502 Bad Gateway`
- `503 Service Unavailable`
- `504 Gateway Timeout`

While those internal retries are running, `/v1/topology` continues to return `202 Accepted` for the request ID.

## Request Payload

The test provider is configured through `provider.params` in the `/v1/generate` request:

```json
{
  "provider": {
    "name": "test",
    "params": {
      "testcaseName": "optional short test case name",
      "description": "optional test case description",
      "generateResponseCode": 202,
      "topologyResponseCode": 200,
      "modelFileName": "small-tree.yaml",
      "errorMessage": "optional error message"
    }
  },
  "engine": {
    "name": "slurm"
  }
}
```

The `engine` object follows the normal Topograph engine configuration. For example, use `slurm` parameters to request `topology/tree` or `topology/block` output, use `k8s` parameters to write node labels, use `nfd` parameters to publish NFD custom resources, use `slinky` parameters to update a Slinky ConfigMap, or use `graph` to return per-instance topology (network layers and labels) as JSON.

Names declared in a model's `blocks[].nodes` are treated as hostnames. The test provider generates the corresponding instance IDs by adding the `i-` prefix; for example, hostname `node1` maps from instance ID `i-node1`.

### Test Provider Parameters

| Parameter | Required | Default | Description |
|---|---:|---|---|
| `testcaseName` | No | Empty | Human-readable name for the scenario. Topograph does not interpret this value. |
| `description` | No | Empty | Longer scenario description. Topograph does not interpret this value. |
| `generateResponseCode` | No | `202` | Status code to return from `/v1/generate`. Valid values are `202` and HTTP error codes from `400` through `599`. Any other value returns `400 Bad Request`. |
| `topologyResponseCode` | No | `200` | Status code to return from `/v1/topology` after the request finishes queueing and processing. Valid values are `200`, `202`, and HTTP error codes from `400` through `599`. Any other value returns `400 Bad Request`. |
| `modelFileName` | No | Built-in test tree | Model file used when `topologyResponseCode` is `200`. Ignored for error responses. If the model cannot be loaded, Topograph returns `400 Bad Request`. |
| `errorMessage` | No | Empty | Response body used for simulated error responses. |

## Processing Behavior

When `/v1/generate` receives a request for provider `test`, Topograph decodes the test parameters before putting the request into the async queue.

- If `generateResponseCode` is between `400` and `599`, Topograph immediately returns that status code and `errorMessage`.
- If `generateResponseCode` is `202`, Topograph accepts the request and returns a request ID.
- If `generateResponseCode` is any other value, Topograph returns `400 Bad Request`.

For accepted requests, `/v1/topology` behaves like the normal asynchronous Topograph flow.

- If the request ID is unknown, Topograph returns `404 Not Found`.
- If the request is still waiting for `requestAggregationDelay` to expire, or is still processing, Topograph returns `202 Accepted`.
- If `topologyResponseCode` is `202`, Topograph keeps returning `202 Accepted` after processing. This simulates a topology request that never completes.
- If `topologyResponseCode` is between `400` and `599`, Topograph returns that status code and `errorMessage` after processing. Retryable codes are retried internally first.
- If `topologyResponseCode` is `200`, Topograph loads the requested model, translates it through the selected engine, and returns the generated output.

## Examples

### Successful Topology Discovery

```json
{
  "provider": {
    "name": "test",
    "params": {
      "testcaseName": "success-case-01",
      "description": "Return 202 for generate and then a valid topology.",
      "generateResponseCode": 202,
      "topologyResponseCode": 200,
      "modelFileName": "small-tree.yaml"
    }
  },
  "engine": {
    "name": "slurm"
  }
}
```

Expected behavior:

- `/v1/generate` returns `202 Accepted` with a request ID.
- `/v1/topology` returns `202 Accepted` until the aggregation delay and processing complete.
- `/v1/topology` then returns `200 OK` with the generated Slurm topology configuration.

### Generate Request Failure

```json
{
  "provider": {
    "name": "test",
    "params": {
      "testcaseName": "failure-case-01",
      "description": "Return 500 from generate.",
      "generateResponseCode": 500,
      "errorMessage": "Internal Server Error"
    }
  },
  "engine": {
    "name": "slurm"
  }
}
```

Expected behavior:

- `/v1/generate` returns `500 Internal Server Error`.
- No request ID is created.
- The client should not call `/v1/topology` for this request.

### Topology Request Failure

```json
{
  "provider": {
    "name": "test",
    "params": {
      "testcaseName": "failure-case-02",
      "description": "Return 408 from topology after processing.",
      "generateResponseCode": 202,
      "topologyResponseCode": 408,
      "errorMessage": "Request to AWS timed out"
    }
  },
  "engine": {
    "name": "slurm"
  }
}
```

Expected behavior:

- `/v1/generate` returns `202 Accepted` with a request ID.
- `/v1/topology` returns `202 Accepted` while the request is queued and while Topograph performs its internal retries.
- `/v1/topology` eventually returns `408 Request Timeout` with the configured error message.

## Curl Workflow

Save a test payload to `payload.json`, submit it, and poll the result:

```sh
uid=$(curl -sS -X POST \
  -H "Content-Type: application/json" \
  -d @payload.json \
  http://localhost:49021/v1/generate)

curl -i "http://localhost:49021/v1/topology?uid=${uid}"
```

For ready-made regression payloads, see `tests/integration/` and `tests/payloads/`.
