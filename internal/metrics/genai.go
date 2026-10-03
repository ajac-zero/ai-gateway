// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"go.opentelemetry.io/otel/metric"
)

const (
	// Metric names, attributes and values according to the Semantic Conventions for Generative AI Metrics.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/

	genaiMetricClientTokenUsage         = "gen_ai.client.token.usage" //nolint:gosec // metric name, not credential
	genaiMetricServerRequestDuration    = "gen_ai.server.request.duration"
	genaiMetricServerTimeToFirstToken   = "gen_ai.server.time_to_first_token"   //nolint:gosec // metric name, not credential
	genaiMetricServerTimePerOutputToken = "gen_ai.server.time_per_output_token" //nolint:gosec // metric name, not credential

	// Current client-side GenAI metrics.
	// See: https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-metrics.md
	// and https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-token-metrics.md
	genaiMetricClientOperationDuration     = "gen_ai.client.operation.duration"
	genaiMetricClientTimeToFirstChunk      = "gen_ai.client.operation.time_to_first_chunk"
	genaiMetricClientTimePerOutputChunk    = "gen_ai.client.operation.time_per_output_chunk"
	genaiMetricClientInputTokensUsage      = "gen_ai.client.inference.usage.input_tokens"             //nolint:gosec // metric name
	genaiMetricClientOutputTokensUsage     = "gen_ai.client.inference.usage.output_tokens"            //nolint:gosec // metric name
	genaiMetricClientCacheReadInputTokens  = "gen_ai.client.inference.usage.cache_read.input_tokens"  //nolint:gosec // metric name
	genaiMetricClientCacheWriteInputTokens = "gen_ai.client.inference.usage.cache_write.input_tokens" //nolint:gosec // metric name
	genaiMetricClientReasoningOutputTokens = "gen_ai.client.inference.usage.reasoning.output_tokens"  //nolint:gosec // metric name
	genaiMetricClientOperationInputTokens  = "gen_ai.client.inference.operation.input_tokens"         //nolint:gosec // metric name
	genaiMetricClientOperationOutputTokens = "gen_ai.client.inference.operation.output_tokens"        //nolint:gosec // metric name
	genaiAttributeTokenModality            = "gen_ai.token.modality"                                  //nolint:gosec // attribute name
	genaiTokenModalityUnknown              = "unknown"
	genaiAttributeOperationName            = "gen_ai.operation.name"
	genaiAttributeProviderName             = "gen_ai.provider.name"
	genaiAttributeOriginalModel            = "gen_ai.original.model"
	genaiAttributeRequestModel             = "gen_ai.request.model"
	genaiAttributeResponseModel            = "gen_ai.response.model"
	genaiAttributeTokenType                = "gen_ai.token.type" //nolint:gosec // metric name, not credential
	genaiAttributeErrorType                = "error.type"
	genaiAttributeBackend                  = "gen_ai.backend"

	GenAIOperationChat                 GenAIOperation = "chat"
	GenAIOperationCompletion           GenAIOperation = "completion"
	GenAIOperationEmbedding            GenAIOperation = "embeddings"
	GenAIOperationMessages             GenAIOperation = "messages"
	GenAIOperationImageGeneration      GenAIOperation = "image_generation"
	GenAIOperationResponses            GenAIOperation = "responses"
	GenAIOperationDecisions            GenAIOperation = "decisions"
	GenAIOperationSpeech               GenAIOperation = "speech"
	GenAIOperationTranscription        GenAIOperation = "transcription"
	GenAIOperationTranslation          GenAIOperation = "translation"
	GenAIOperationRerank               GenAIOperation = "rerank"
	GenAIOperationSystemOne            GenAIOperation = "systemone"
	GenAIOperationTokenize             GenAIOperation = "tokenize"
	GenAIOperationResponsesInputTokens GenAIOperation = "responses_input_tokens"
	GenAIOperationCountTokens          GenAIOperation = "count_tokens"

	// Provider names according to the Semantic Conventions for Generative AI Metrics.
	// See: https://opentelemetry.io/docs/specs/semconv/attributes-registry/gen-ai/
	genaiProviderOpenAI       = "openai"
	genaiProviderAzureOpenAI  = "azure.openai"
	genaiProviderAWSBedrock   = "aws.bedrock"
	genaiProviderAWSAnthropic = "aws.anthropic"
	genaiProviderGCPVertexAI  = "gcp.vertex_ai"
	genaiProviderGCPAnthropic = "gcp.anthropic"
	genaiProviderAnthropic    = "anthropic"
	genaiProviderCohere       = "cohere"
	genaiProviderTypeSafe     = "typesafe"

	genaiTokenTypeInput  = "input"
	genaiTokenTypeOutput = "output"
	// "cached_input" is not yet part of the spec but has been proposed:
	// https://github.com/open-telemetry/semantic-conventions/issues/1959
	//
	// However, the spec says "a custom value MAY be used.", so we can use it now.
	genaiTokenTypeCachedInput        = "cached_input"
	genaiTokenTypeCacheCreationInput = "cache_creation_input"
	genaiTokenTypeReasoning          = "reasoning"
	genaiErrorTypeFallback           = "_OTHER"
)

// GenAIOperation represents the type of generative AI operation i.e. the endpoint being called.
type GenAIOperation string

// genAI holds metrics according to the Semantic Conventions for Generative AI Metrics.
// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/.
type genAI struct {
	// Number of tokens processed.
	// Note: We record gen_ai.client.token.usage because the AI Gateway acts as a client to upstream AI services.
	// Since we pass through token counts without manipulation, this metric accurately reflects token usage
	// from our perspective as a client of the upstream AI provider.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiclienttokenusage
	tokenUsage metric.Float64Histogram
	// requestLatency is the total latency of the request.
	// Measured from the start of the received request headers in extproc to the end of the processed response body in extproc.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiserverrequestduration
	requestLatency metric.Float64Histogram
	// firstTokenLatency is the latency to receive the first token.
	// Measured from the start of the received request headers in extproc to the receiving of the first token in the response body in extproc.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_to_first_token
	firstTokenLatency metric.Float64Histogram
	// outputTokenLatency is the time per output token generated after the first token.
	// Calculated by: (request_duration - time_to_first_token) / (output_tokens - 1)
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_per_output_token
	outputTokenLatency metric.Float64Histogram

	// Current semconv client-side metrics (see constants above).
	clientOperationDuration   metric.Float64Histogram
	clientTimeToFirstChunk    metric.Float64Histogram
	clientTimePerOutputChunk  metric.Float64Histogram
	clientInputTokens         metric.Int64Counter
	clientOutputTokens        metric.Int64Counter
	clientCacheReadTokens     metric.Int64Counter
	clientCacheWriteTokens    metric.Int64Counter
	clientReasoningTokens     metric.Int64Counter
	clientOperationInputToks  metric.Float64Histogram
	clientOperationOutputToks metric.Float64Histogram
}

func mustRegisterInt64Counter(meter metric.Meter, name, desc string) metric.Int64Counter {
	c, err := meter.Int64Counter(name, metric.WithDescription(desc), metric.WithUnit("{token}"))
	if err != nil {
		panic(err)
	}
	return c
}

// normalizeClientOperation maps a gateway operation to a gen_ai.operation.name well-known value for the
// current client metrics. Operations without a predefined value keep their custom name, which the spec allows.
func normalizeClientOperation(op string) string {
	switch GenAIOperation(op) {
	case GenAIOperationChat, GenAIOperationMessages, GenAIOperationResponses:
		return "chat"
	case GenAIOperationCompletion:
		return "text_completion"
	default:
		return op
	}
}

// operationReportsTokens reports whether the operation performs inference that consumes tokens. Token counting
// endpoints do not, and the spec says not to report usage for them.
func operationReportsTokens(op string) bool {
	switch GenAIOperation(op) {
	case GenAIOperationCountTokens, GenAIOperationResponsesInputTokens, GenAIOperationTokenize:
		return false
	}
	return true
}

// normalizeClientProvider maps the legacy gen_ai.provider.name value to a well-known semconv provider value.
// Providers hosting another vendor's models use the discriminator of the hosting API format.
func normalizeClientProvider(legacy string) string {
	switch legacy {
	case genaiProviderAzureOpenAI:
		return "azure.ai.openai"
	case genaiProviderAWSAnthropic:
		return "aws.bedrock"
	case genaiProviderGCPAnthropic:
		return "gcp.vertex_ai"
	default:
		return legacy
	}
}

// newGenAI creates a new genAI metrics instance.
func newGenAI(meter metric.Meter) *genAI {
	durationBuckets := metric.WithExplicitBucketBoundaries(0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92)
	tokenBuckets := metric.WithExplicitBucketBoundaries(1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864)
	return &genAI{
		clientOperationDuration: mustRegisterHistogram(meter, genaiMetricClientOperationDuration,
			metric.WithDescription("GenAI operation duration."), metric.WithUnit("s"), durationBuckets),
		clientTimeToFirstChunk: mustRegisterHistogram(meter, genaiMetricClientTimeToFirstChunk,
			metric.WithDescription("Time to receive the first chunk in a streaming response."), metric.WithUnit("s"), durationBuckets),
		clientTimePerOutputChunk: mustRegisterHistogram(meter, genaiMetricClientTimePerOutputChunk,
			metric.WithDescription("Time per output chunk, recorded for each chunk received after the first one."), metric.WithUnit("s"), durationBuckets),
		clientInputTokens:  mustRegisterInt64Counter(meter, genaiMetricClientInputTokensUsage, "The number of input (prompt) tokens used, including cached tokens."),
		clientOutputTokens: mustRegisterInt64Counter(meter, genaiMetricClientOutputTokensUsage, "The number of output (completion) tokens used, including reasoning tokens."),
		clientCacheReadTokens: mustRegisterInt64Counter(meter, genaiMetricClientCacheReadInputTokens,
			"The number of input tokens served from a provider-managed cache."),
		clientCacheWriteTokens: mustRegisterInt64Counter(meter, genaiMetricClientCacheWriteInputTokens,
			"The number of input tokens written to a provider-managed cache."),
		clientReasoningTokens: mustRegisterInt64Counter(meter, genaiMetricClientReasoningOutputTokens,
			"The number of output tokens used for reasoning."),
		clientOperationInputToks: mustRegisterHistogram(meter, genaiMetricClientOperationInputTokens,
			metric.WithDescription("The number of input (prompt) tokens used per inference operation."), metric.WithUnit("{token}"), tokenBuckets),
		clientOperationOutputToks: mustRegisterHistogram(meter, genaiMetricClientOperationOutputTokens,
			metric.WithDescription("The number of output (completion) tokens used per inference operation."), metric.WithUnit("{token}"), tokenBuckets),
		tokenUsage: mustRegisterHistogram(meter,
			genaiMetricClientTokenUsage,
			metric.WithDescription("Number of tokens processed."),
			metric.WithUnit("token"),
			metric.WithExplicitBucketBoundaries(1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864),
		),
		requestLatency: mustRegisterHistogram(meter,
			genaiMetricServerRequestDuration,
			metric.WithDescription("Generative AI server request duration such as time-to-last byte or last output token."),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92),
		),
		firstTokenLatency: mustRegisterHistogram(meter,
			genaiMetricServerTimeToFirstToken,
			metric.WithDescription("Time to receive first token in streaming responses."),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.02, 0.04, 0.06, 0.08, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 15.0, 20.0, 30.0, 45.0, 60.0),
		),
		outputTokenLatency: mustRegisterHistogram(meter,
			genaiMetricServerTimePerOutputToken,
			metric.WithDescription("Time per output token generated after the first token for successful responses."),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1.0, 2.5),
		),
	}
}
