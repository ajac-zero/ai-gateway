---
id: metrics
title: AI/LLM Metrics
sidebar_position: 6
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

When using the Agent Router, it will collect AI specific metrics and expose them to Prometheus for monitoring by default.
This guide provides an overview of the metrics collected by the AI Gateway and how to monitor them using Prometheus.

## Overview

Agent Router intercepts model-provider requests and exports metrics to
Prometheus and configured OpenTelemetry metric exporters. It emits the existing
gateway GenAI metrics alongside the current client-side metrics from the
[OpenTelemetry GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-metrics.md).
The GenAI conventions are still under development, so names and requirements
may evolve.

### Supported Endpoints

Metrics are collected for the following LLM endpoints:

- **`/v1/chat/completions`** - Chat completions (streaming and non-streaming)
- **`/v1/completions`** - Legacy text completions (streaming and non-streaming)
- **`/v1/embeddings`** - Text embeddings
- **`/v1/responses`** - Responses (streaming and non-streaming)
- **`/v1/images/generations`**, `/v1/audio/speech`, `/v1/audio/transcriptions`, and `/v1/audio/translations` - Image/audio operations
- **`/cohere/v2/rerank`** - Rerank
- **`/typesafe/v1/systemone`** - TypeSafe System One (Jev)
- **`/anthropic/v1/messages`** and Anthropic `count_tokens` - Messages and token counting

For example, the Agent Router collects metrics such as:

- [**`gen_ai.client.token.usage`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiclienttokenusage): Number of tokens processed. The attribute `gen_ai.token.type` can be used to differentiate between input, output, and total tokens.
- **`gen_ai.client.operation.duration`**: Duration of each provider operation, with a low-cardinality error type on failures.
- **`gen_ai.client.operation.time_to_first_chunk`** and **`gen_ai.client.operation.time_per_output_chunk`**: Streaming response chunk timing.
- **`gen_ai.client.inference.usage.input_tokens`** and **`gen_ai.client.inference.usage.output_tokens`**: Provider-reported inference token counters; modality is `unknown` when the provider does not report it.
- **`gen_ai.client.inference.usage.cache_read.input_tokens`**, **`gen_ai.client.inference.usage.cache_write.input_tokens`**, and **`gen_ai.client.inference.usage.reasoning.output_tokens`**: Available cache/reasoning subsets, recorded in addition to their input/output totals.
- **`gen_ai.client.inference.operation.input_tokens`** and **`gen_ai.client.inference.operation.output_tokens`**: Per-operation input/output token histograms.
- [**`gen_ai.server.request.duration`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiserverrequestduration): Measured from the start of the received request headers in the Agent Router filter to the end of the processed response body processing.
- [**`gen_ai.server.time_to_first_token`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_to_first_token): Measured from the start of the received request headers in the Agent Router filter to the receiving of the first token in the response body handling.
- [**`gen_ai.server.time_per_output_token`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_per_output_token): The latency between consecutive tokens, if supported, or by chunks/tokens otherwise.

The established gateway-side metrics include request attributes such as:

- `gen_ai.operation.name`
  - `chat`: For `/v1/chat/completions` endpoint.
  - `completion`: For `/v1/completions` endpoint.
  - `embeddings`: For `/v1/embeddings` endpoint.
  - `responses`: For `/v1/responses` endpoint.
  - `rerank`: For `/cohere/v2/rerank` endpoint.
  - `systemone`: For `/typesafe/v1/systemone` endpoint.
  - `image_generation`: For `/v1/images/generations` endpoint.
  - `messages`: For `/anthropic/v1/messages` endpoint.
- `gen_ai.original.model` - The original model name from the request body
- `gen_ai.request.model` - The model name requested (may be overridden)
- `gen_ai.response.model` - The model name returned in the response
- `gen_ai.provider.name` - The provider name (e.g., `openai`, `anthropic`)
- `gen_ai.backend` - The `AIServiceBackend` that served the request, as `namespace/name`

The current client-side metric series use normalized provider and operation
names (`chat`, `text_completion`, `embeddings`, and the corresponding provider
registry values). They include `gen_ai.request.model` and
`gen_ai.response.model` when known, but intentionally omit gateway-specific
`gen_ai.original.model`, `gen_ai.backend`, and custom request-header labels.
The older `gen_ai.client.token.usage` and gateway-side
`gen_ai.server.*` series remain available; they are not replacements for the
new client-side series. Token metrics require usage reported by the provider,
and chunk timing is recorded only for streams. Input/output token totals already
include cache and reasoning subsets; do not add those subset counters again
when calculating total consumption.

MCP requests are measured separately with **`mcp.client.operation.duration`**
for gateway-to-backend operations and **`mcp.server.operation.duration`** for
client-to-gateway operations. These use MCP method/tool/prompt names and
low-cardinality error attributes; they do not include gateway-specific backend
or request-header labels.

:::tip

You can enrich the metrics with custom labels extracted from HTTP request headers. Use `controller.requestHeaderAttributes` for a base mapping shared with spans and access logs, and `controller.metricsRequestHeaderAttributes` for metrics-only mappings. Metrics never default to `session.id` because it is high-cardinality. See [values.yaml](https://github.com/theagentrouter/agent-router/blob/main/manifests/charts/ai-gateway-helm/values.yaml) for more details including other configurations.

:::

## Trying it out

Before you begin, you'll need to complete the basic setup from the [Basic Usage](/docs/getting-started/basic-usage) guide.

Then, you can install the prometheus using the following commands:

<CodeBlock language="shell">
{`kubectl apply -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/monitoring/monitoring.yaml`}
</CodeBlock>

Let's wait for a while until the Prometheus is up and running.

```shell
kubectl wait --for=condition=ready pod -l app=prometheus -n monitoring
```

To access the Prometheus dashboard, you need to port-forward the Prometheus service to your local machine like this:

```shell
kubectl port-forward -n monitoring svc/prometheus 9090:9090
```

Now open your browser and navigate to `http://localhost:9090` to access the Prometheus dashboard to explore the metrics.

Alternatively, you can make the following requests to see the raw metrics:

```shell
curl http://localhost:9090/api/v1/query --data-urlencode \
  'query=sum(gen_ai_client_token_usage_sum{gateway_envoyproxy_io_owning_gateway_name = "envoy-ai-gateway-basic"}) by (gen_ai_request_model, gen_ai_token_type)' \
  | jq '.data.result[]'
```

and then you would get the response like this, assuming you have made some requests with the model `gpt-4o-mini`:

```json lines
{
  "metric": {
    "gen_ai_request_model": "gpt-4o-mini",
    "gen_ai_token_type": "input"
  },
  "value": [
    1743105857.684,
    "12"
  ]
}
{
  "metric": {
    "gen_ai_request_model": "gpt-4o-mini",
    "gen_ai_token_type": "output"
  },
  "value": [
    1743105857.684,
    "13"
  ]
}
{
  "metric": {
    "gen_ai_request_model": "gpt-4o-mini",
    "gen_ai_token_type": "total"
  },
  "value": [
    1743105857.684,
    "25"
  ]
}
```
