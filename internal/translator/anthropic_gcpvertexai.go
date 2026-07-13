// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"google.golang.org/genai"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewAnthropicToGCPVertexAITranslator implements [Factory] for Anthropic Messages to GCP Gemini translation.
func NewAnthropicToGCPVertexAITranslator(modelNameOverride internalapi.ModelNameOverride) AnthropicMessagesTranslator {
	return &anthropicToGCPVertexAITranslator{
		modelNameOverride: modelNameOverride,
		geminiTranslator: &openAIToGCPVertexAITranslatorV1ChatCompletion{
			modelNameOverride: modelNameOverride,
		},
	}
}

// anthropicToGCPVertexAITranslator translates Anthropic Messages API requests to GCP Vertex AI Gemini API.
type anthropicToGCPVertexAITranslator struct {
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
	stream            bool
	streamState       *openAIStreamToAnthropicState
	geminiTranslator  *openAIToGCPVertexAITranslatorV1ChatCompletion
	debugLogEnabled   bool
	enableRedaction   bool
	logger            *slog.Logger
}

// RequestBody implements [AnthropicMessagesTranslator.RequestBody].
func (a *anthropicToGCPVertexAITranslator) RequestBody(_ []byte, body *anthropic.MessagesRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if err = validateAnthropicToGCPVertexAIRequest(body); err != nil {
		return nil, nil, err
	}

	a.stream = body.Stream
	a.requestModel = cmp.Or(a.modelNameOverride, body.Model)

	var path string
	if a.stream {
		path = buildGCPModelPathSuffix(gcpModelPublisherGoogle, a.requestModel, gcpMethodStreamGenerateContent, "alt=sse")
	} else {
		path = buildGCPModelPathSuffix(gcpModelPublisherGoogle, a.requestModel, gcpMethodGenerateContent)
	}

	openAIReq := buildOpenAIChatCompletionRequest(body, a.modelNameOverride)
	openAIReq.Tools = normalizeAnthropicToolParametersForGemini(openAIReq.Tools)
	gcpReq, err := a.geminiTranslator.openAIMessageToGeminiMessage(openAIReq, a.requestModel)
	if err != nil {
		return nil, nil, err
	}
	if len(body.StopSequences) > 0 {
		if gcpReq.GenerationConfig == nil {
			gcpReq.GenerationConfig = &genai.GenerationConfig{}
		}
		gcpReq.GenerationConfig.StopSequences = body.StopSequences
	}
	if body.TopK != nil {
		if gcpReq.GenerationConfig == nil {
			gcpReq.GenerationConfig = &genai.GenerationConfig{}
		}
		topK := float32(*body.TopK)
		gcpReq.GenerationConfig.TopK = &topK
	}
	if body.Thinking != nil {
		if gcpReq.GenerationConfig == nil {
			gcpReq.GenerationConfig = &genai.GenerationConfig{}
		}
		gcpReq.GenerationConfig.ThinkingConfig = anthropicThinkingToGemini(body.Thinking)
	}

	a.geminiTranslator.requestModel = a.requestModel
	a.geminiTranslator.stream = a.stream
	a.geminiTranslator.toolCallIndex = 0
	if a.stream {
		a.streamState = &openAIStreamToAnthropicState{
			activeTools:       make(map[int64]*streamToolCall),
			requestModel:      a.requestModel,
			includeInputUsage: true,
		}
	}

	newBody, err = json.Marshal(gcpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling Gemini request: %w", err)
	}
	newHeaders = []internalapi.Header{
		{pathHeaderName, path},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

func validateAnthropicToGCPVertexAIRequest(body *anthropic.MessagesRequest) error {
	const maxInt32 = float64(1<<31 - 1)

	if body == nil {
		return fmt.Errorf("%w: request body is required", internalapi.ErrInvalidRequestBody)
	}
	if body.Container != nil {
		return fmt.Errorf("%w: container is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	}
	if body.ContextManagement != nil {
		return fmt.Errorf("%w: context_management is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	}
	if len(body.MCPServers) > 0 {
		return fmt.Errorf("%w: mcp_servers are not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	}
	if body.ServiceTier != nil {
		return fmt.Errorf("%w: service_tier is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	}
	if body.MaxTokens <= 0 || body.MaxTokens > maxInt32 || math.Trunc(body.MaxTokens) != body.MaxTokens {
		return fmt.Errorf("%w: max_tokens must be a positive integer within the GCP Vertex AI supported range", internalapi.ErrInvalidRequestBody)
	}
	if body.TopK != nil && (*body.TopK < 0 || float64(float32(*body.TopK)) != float64(*body.TopK)) {
		return fmt.Errorf("%w: top_k must be a non-negative integer exactly representable by GCP Vertex AI", internalapi.ErrInvalidRequestBody)
	}
	if body.ToolChoice != nil {
		var disableParallelToolUse *bool
		switch {
		case body.ToolChoice.Auto != nil:
			disableParallelToolUse = body.ToolChoice.Auto.DisableParallelToolUse
		case body.ToolChoice.Any != nil:
			disableParallelToolUse = body.ToolChoice.Any.DisableParallelToolUse
		case body.ToolChoice.Tool != nil:
			disableParallelToolUse = body.ToolChoice.Tool.DisableParallelToolUse
		}
		if disableParallelToolUse != nil && *disableParallelToolUse {
			return fmt.Errorf("%w: disable_parallel_tool_use is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
		}
	}
	if body.System != nil {
		for i := range body.System.Texts {
			if body.System.Texts[i].CacheControl != nil {
				return fmt.Errorf("%w: system block %d cache_control is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody, i)
			}
		}
	}
	if body.Thinking != nil {
		if body.Thinking.Adaptive != nil {
			return fmt.Errorf("%w: Anthropic thinking type adaptive is not supported for GCP Vertex AI", internalapi.ErrInvalidRequestBody)
		}
		if body.Thinking.Enabled != nil {
			budget := body.Thinking.Enabled.BudgetTokens
			if budget < 0 || budget > maxInt32 || math.Trunc(budget) != budget {
				return fmt.Errorf("%w: thinking budget_tokens must be a non-negative integer within the GCP Vertex AI supported range", internalapi.ErrInvalidRequestBody)
			}
		}
	}
	for i := range body.Tools {
		if body.Tools[i].Tool == nil {
			return fmt.Errorf("%w: tool %d uses an Anthropic built-in tool unsupported by GCP Vertex AI translation", internalapi.ErrInvalidRequestBody, i)
		}
		if body.Tools[i].Tool.CacheControl != nil {
			return fmt.Errorf("%w: tool %d cache_control is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody, i)
		}
	}
	for i, msg := range body.Messages {
		for j, block := range msg.Content.Array {
			if err := validateAnthropicContentBlockForGCPVertexAI(msg.Role, &block); err != nil {
				return fmt.Errorf("message %d content block %d: %w", i, j, err)
			}
		}
	}
	return nil
}

func validateAnthropicContentBlockForGCPVertexAI(role anthropic.MessageRole, block *anthropic.ContentBlockParam) error {
	switch {
	case block.Text != nil:
		if block.Text.CacheControl != nil {
			return fmt.Errorf("%w: text cache_control is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
		}
		return nil
	case block.Image != nil:
		if role != anthropic.MessageRoleUser {
			return fmt.Errorf("%w: image content is only supported in user messages for GCP Vertex AI", internalapi.ErrInvalidRequestBody)
		}
		if block.Image.CacheControl != nil {
			return fmt.Errorf("%w: image cache_control is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
		}
		switch {
		case block.Image.Source.URL != nil:
			if strings.TrimSpace(block.Image.Source.URL.URL) == "" {
				return fmt.Errorf("%w: image URL must not be empty for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
			}
		case block.Image.Source.Base64 != nil:
			if block.Image.Source.Base64.MediaType == "" || block.Image.Source.Base64.Data == "" {
				return fmt.Errorf("%w: base64 image media_type and data are required for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
			}
		default:
			return fmt.Errorf("%w: image source type is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
		}
		return nil
	case block.ToolUse != nil:
		if role != anthropic.MessageRoleAssistant {
			return fmt.Errorf("%w: tool_use content is only supported in assistant messages", internalapi.ErrInvalidRequestBody)
		}
		if block.ToolUse.CacheControl != nil {
			return fmt.Errorf("%w: tool_use cache_control is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
		}
		return nil
	case block.ToolResult != nil:
		if role != anthropic.MessageRoleUser {
			return fmt.Errorf("%w: tool_result content is only supported in user messages", internalapi.ErrInvalidRequestBody)
		}
		return validateAnthropicToolResultForGCPVertexAI(block.ToolResult)
	case block.Document != nil:
		return fmt.Errorf("%w: document content is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	case block.SearchResult != nil:
		return fmt.Errorf("%w: search_result content is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	case block.Thinking != nil:
		if role != anthropic.MessageRoleAssistant {
			return fmt.Errorf("%w: thinking content is only supported in assistant messages", internalapi.ErrInvalidRequestBody)
		}
		return nil
	case block.RedactedThinking != nil:
		return fmt.Errorf("%w: redacted_thinking content blocks are not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	case block.ServerToolUse != nil:
		return fmt.Errorf("%w: server_tool_use content is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	case block.WebSearchToolResult != nil:
		return fmt.Errorf("%w: web_search_tool_result content is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	default:
		return fmt.Errorf("%w: unknown content block type is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	}
}

func validateAnthropicToolResultForGCPVertexAI(toolResult *anthropic.ToolResultBlockParam) error {
	if toolResult.IsError {
		return fmt.Errorf("%w: tool_result is_error is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	}
	if toolResult.CacheControl != nil {
		return fmt.Errorf("%w: tool_result cache_control is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
	}
	if toolResult.Content == nil {
		return nil
	}
	for _, item := range toolResult.Content.Array {
		if item.Text != nil && item.Text.CacheControl != nil {
			return fmt.Errorf("%w: tool_result text cache_control is not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
		}
		if item.Image != nil || item.Document != nil || item.SearchResult != nil {
			return fmt.Errorf("%w: tool_result image, document, and search_result content are not supported for GCP Vertex AI translation", internalapi.ErrInvalidRequestBody)
		}
	}
	return nil
}

func anthropicThinkingToGemini(thinking *anthropic.Thinking) *genai.ThinkingConfig {
	if thinking == nil {
		return nil
	}
	if thinking.Enabled != nil {
		budget := int32(thinking.Enabled.BudgetTokens) //nolint:gosec // validated before conversion.
		return &genai.ThinkingConfig{IncludeThoughts: true, ThinkingBudget: &budget}
	}
	if thinking.Disabled != nil {
		return &genai.ThinkingConfig{IncludeThoughts: false}
	}
	return nil
}

func normalizeAnthropicToolParametersForGemini(tools []openai.Tool) []openai.Tool {
	for i := range tools {
		if tools[i].Function == nil || tools[i].Function.Parameters == nil {
			continue
		}
		switch schema := tools[i].Function.Parameters.(type) {
		case anthropic.ToolInputSchema:
			tools[i].Function.Parameters = anthropicToolInputSchemaToMap(schema)
		case *anthropic.ToolInputSchema:
			if schema != nil {
				tools[i].Function.Parameters = anthropicToolInputSchemaToMap(*schema)
			}
		}
	}
	return tools
}

func anthropicToolInputSchemaToMap(schema anthropic.ToolInputSchema) map[string]any {
	params := map[string]any{"type": schema.Type}
	if len(schema.Properties) > 0 {
		params["properties"] = schema.Properties
	}
	if len(schema.Required) > 0 {
		params["required"] = schema.Required
	}
	return params
}

// ResponseHeaders implements [AnthropicMessagesTranslator.ResponseHeaders].
func (a *anthropicToGCPVertexAITranslator) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	if a.stream {
		newHeaders = []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}
	}
	return
}

// ResponseBody implements [AnthropicMessagesTranslator.ResponseBody].
func (a *anthropicToGCPVertexAITranslator) ResponseBody(_ map[string]string, body io.Reader, endOfStream bool, span tracingapi.MessageSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	if a.stream {
		return a.responseBodyStreaming(body, endOfStream)
	}
	return a.responseBodyNonStreaming(body, span)
}

func (a *anthropicToGCPVertexAITranslator) responseBodyNonStreaming(body io.Reader, span tracingapi.MessageSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	gcpResp := &genai.GenerateContentResponse{}
	if err = json.NewDecoder(body).Decode(gcpResp); err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error decoding GCP response: %w", err)
	}

	responseModel = a.requestModel
	if gcpResp.ModelVersion != "" {
		responseModel = gcpResp.ModelVersion
	}

	openAIResp, err := a.geminiTranslator.geminiResponseToOpenAIMessage(gcpResp, responseModel)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error converting GCP response to OpenAI format: %w", err)
	}
	anthropicResp := openAIResponseToAnthropic(openAIResp, responseModel)
	anthropicResp.Usage = geminiUsageToAnthropicUsage(gcpResp.UsageMetadata)
	if a.debugLogEnabled && a.enableRedaction && a.logger != nil {
		redactedResp := a.RedactAnthropicBody(anthropicResp)
		if jsonBody, marshalErr := json.Marshal(redactedResp); marshalErr == nil {
			a.logger.Debug("response body processing", slog.Any("response", string(jsonBody)))
		}
	}

	if anthropicResp.Usage != nil {
		tokenUsage = tokenUsageFromAnthropicUsage(anthropicResp.Usage)
	}

	if span != nil {
		span.RecordResponse(anthropicResp)
	}

	newBody, err = json.Marshal(anthropicResp)
	if err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to marshal Anthropic response: %w", err)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

func tokenUsageFromAnthropicUsage(usage *anthropic.Usage) metrics.TokenUsage {
	if usage == nil {
		return metrics.TokenUsage{}
	}
	return metrics.ExtractTokenUsageFromExplicitCaching(
		int64(usage.InputTokens),
		int64(usage.OutputTokens),
		ptr.To(int64(usage.CacheCreationInputTokens)),
		ptr.To(int64(usage.CacheReadInputTokens)),
	)
}

func geminiUsageToAnthropicUsage(metadata *genai.GenerateContentResponseUsageMetadata) *anthropic.Usage {
	if metadata == nil {
		return nil
	}
	inputTokens := max(metadata.PromptTokenCount-metadata.CachedContentTokenCount, 0)
	return &anthropic.Usage{
		InputTokens:          float64(inputTokens),
		OutputTokens:         float64(metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount),
		CacheReadInputTokens: float64(metadata.CachedContentTokenCount),
	}
}

func (a *anthropicToGCPVertexAITranslator) responseBodyStreaming(body io.Reader, endOfStream bool) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	responseModel = a.requestModel
	if a.streamState == nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("stream state not initialized")
	}

	chunks, err := a.geminiTranslator.parseGCPStreamingChunks(body)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error parsing GCP streaming chunks: %w", err)
	}

	var openAIStream []byte
	var usage *anthropic.Usage
	for i := range chunks {
		chunk := &chunks[i]
		openAIChunk := a.geminiTranslator.convertGCPChunkToOpenAI(chunk)
		if err = serializeOpenAIChatCompletionChunk(openAIChunk, &openAIStream); err != nil {
			return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error marshaling OpenAI chunk: %w", err)
		}

		if chunk.UsageMetadata != nil && chunk.UsageMetadata.PromptTokenCount > 0 {
			usage = geminiUsageToAnthropicUsage(chunk.UsageMetadata)
			usageChunk := &openai.ChatCompletionResponseChunk{
				ID:      chunk.ResponseID,
				Created: openai.JSONUNIXTime(chunk.CreateTime),
				Object:  "chat.completion.chunk",
				Choices: []openai.ChatCompletionResponseChunkChoice{},
				Usage:   ptr.To(geminiUsageToOpenAIUsage(chunk.UsageMetadata)),
				Model:   a.requestModel,
			}
			if err = serializeOpenAIChatCompletionChunk(usageChunk, &openAIStream); err != nil {
				return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error marshaling OpenAI usage chunk: %w", err)
			}
		}
	}

	if _, err = a.streamState.buffer.ReadFrom(bytes.NewReader(openAIStream)); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to read converted stream body: %w", err)
	}
	out := make([]byte, 0)
	if err = a.streamState.processBuffer(&out, endOfStream); err != nil {
		return nil, nil, tokenUsage, responseModel, err
	}

	responseModel = cmp.Or(a.streamState.model, a.requestModel)
	tokenUsage = a.streamState.tokenUsage
	if usage != nil {
		tokenUsage = tokenUsageFromAnthropicUsage(usage)
	}
	newBody = out
	return
}

// ResponseError implements [AnthropicMessagesTranslator.ResponseError].
func (a *anthropicToGCPVertexAITranslator) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	var buf []byte
	buf, err = io.ReadAll(body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read error body: %w", err)
	}

	anthropicError := anthropic.ErrorResponse{
		Type: "error",
		Error: anthropic.ErrorResponseMessage{
			Type:    gcpVertexAIBackendError,
			Message: string(buf),
		},
	}

	var gcpError gcpVertexAIError
	if err = json.Unmarshal(buf, &gcpError); err == nil && (gcpError.Error.Status != "" || gcpError.Error.Message != "") {
		errMsg := gcpError.Error.Message
		if len(gcpError.Error.Details) > 0 {
			errMsg = fmt.Sprintf("Error: %s\nDetails: %s", errMsg, string(gcpError.Error.Details))
		}
		if gcpError.Error.Status != "" {
			anthropicError.Error.Type = gcpError.Error.Status
		}
		anthropicError.Error.Message = errMsg
	} else if strings.TrimSpace(string(buf)) == "" {
		anthropicError.Error.Message = respHeaders[statusHeaderName]
	}

	newBody, err = json.Marshal(anthropicError)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// SetRedactionConfig implements [AnthropicResponseRedactor.SetRedactionConfig].
func (a *anthropicToGCPVertexAITranslator) SetRedactionConfig(debugLogEnabled, enableRedaction bool, logger *slog.Logger) {
	a.debugLogEnabled = debugLogEnabled
	a.enableRedaction = enableRedaction
	a.logger = logger
}

// RedactAnthropicBody implements [AnthropicResponseRedactor.RedactAnthropicBody].
func (a *anthropicToGCPVertexAITranslator) RedactAnthropicBody(resp *anthropic.MessagesResponse) *anthropic.MessagesResponse {
	if resp == nil {
		return nil
	}
	redacted := *resp
	if len(resp.Content) > 0 {
		redacted.Content = make([]anthropic.MessagesContentBlock, len(resp.Content))
		for i := range resp.Content {
			redacted.Content[i] = redactAnthropicContent(&resp.Content[i])
		}
	}
	return &redacted
}
