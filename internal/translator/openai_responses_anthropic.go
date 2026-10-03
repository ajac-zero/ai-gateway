// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	anthropicVertex "github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	// anthropicAPIVersion is the only stable version of the native Anthropic Messages API.
	// https://platform.claude.com/docs/en/api/versioning
	anthropicAPIVersion = "2023-06-01"

	// anthropicMinThinkingBudget is the smallest budget_tokens Anthropic accepts for extended thinking.
	anthropicMinThinkingBudget = 1024

	// Reasoning items carry Anthropic thinking state in encrypted_content, which clients treat as
	// opaque and send back on the next turn. The prefixes distinguish thinking signatures from
	// redacted_thinking data, and both from reasoning state produced by other providers.
	anthropicThinkingEncryptedPrefix         = "anthropic.thinking:"
	anthropicRedactedThinkingEncryptedPrefix = "anthropic.redacted_thinking:" // #nosec G101 -- Prefix of opaque reasoning state, not a credential.
)

// extendedThinkingOnlyModels lists Claude models that support thinking only through
// thinking.type=enabled with budget_tokens and reject adaptive thinking. Every other model is
// assumed to support adaptive thinking, which Claude 4.7 and later require.
// https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting
var extendedThinkingOnlyModels = []string{
	"claude-3",
	"opus-4-0", "opus-4-1", "opus-4-2025", "opus-4@",
	"sonnet-4-0", "sonnet-4-2025", "sonnet-4@",
	"opus-4-5", "sonnet-4-5", "haiku-4-5",
}

// responsesThinkingBudgets maps OpenAI reasoning effort to extended-thinking budget_tokens for
// models without adaptive thinking.
var responsesThinkingBudgets = map[string]int64{
	"minimal": anthropicMinThinkingBudget,
	"low":     4096,
	"medium":  8192,
	"high":    16384,
	"xhigh":   24576,
}

// NewResponsesOpenAIToAnthropicTranslator implements [OpenAIResponsesTranslator] for the native
// Anthropic Messages API. The prefix defaults to "v1", producing "/v1/messages".
func NewResponsesOpenAIToAnthropicTranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesTranslator {
	return &openAIToAnthropicTranslatorV1Responses{
		apiSchema:         filterapi.APISchemaAnthropic,
		path:              path.Join("/", prefix, "messages"),
		modelNameOverride: modelNameOverride,
		now:               time.Now,
	}
}

// NewResponsesOpenAIToGCPAnthropicTranslator implements [OpenAIResponsesTranslator] for Claude on
// GCP Vertex AI, which serves the Anthropic Messages API through rawPredict and streamRawPredict.
func NewResponsesOpenAIToGCPAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesTranslator {
	return &openAIToAnthropicTranslatorV1Responses{
		apiSchema:         filterapi.APISchemaGCPAnthropic,
		apiVersion:        cmp.Or(apiVersion, anthropicVertex.DefaultVersion),
		modelNameOverride: modelNameOverride,
		now:               time.Now,
	}
}

// openAIToAnthropicTranslatorV1Responses translates the OpenAI Responses API to the Anthropic
// Messages API and translates the Anthropic message or SSE stream back into a Responses object or
// Responses SSE events.
//
// Responses features that need server-side state or OpenAI-hosted tools are rejected with
// [internalapi.ErrInvalidRequestBody] rather than dropped.
type openAIToAnthropicTranslatorV1Responses struct {
	apiSchema filterapi.APISchemaName
	// path is the native Anthropic messages path. Unused for GCP, where the path names the model.
	path string
	// apiVersion is the anthropic_version body field for GCP.
	apiVersion        string
	modelNameOverride internalapi.ModelNameOverride
	now               func() time.Time

	// The following fields are reset by every RequestBody call.
	request      *openai.ResponseRequest
	requestModel internalapi.RequestModel
	stream       *responsesAnthropicStream
}

// RequestBody implements [OpenAIResponsesTranslator.RequestBody].
func (o *openAIToAnthropicTranslatorV1Responses) RequestBody(_ []byte, req *openai.ResponseRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	o.request = req
	o.requestModel = cmp.Or(o.modelNameOverride, req.Model)
	o.stream = nil

	params, err := responsesToAnthropicParams(req, o.requestModel)
	if err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Anthropic request: %w", err)
	}
	if req.Stream {
		if body, err = sjson.SetBytesOptions(body, "stream", true, sjsonOptionsInPlace); err != nil {
			return nil, nil, fmt.Errorf("failed to set stream: %w", err)
		}
		skeleton := o.responseSkeleton()
		o.stream = newResponsesAnthropicStream(&skeleton, o.now)
	}

	var reqPath string
	switch o.apiSchema {
	case filterapi.APISchemaGCPAnthropic:
		// Vertex takes the model from the path and the API version from the body.
		specifier := "rawPredict"
		if req.Stream {
			specifier = "streamRawPredict"
		}
		reqPath = buildGCPModelPathSuffix(gcpModelPublisherAnthropic, o.requestModel, specifier)
		if body, err = sjson.SetBytesOptions(body, anthropicVersionKey, o.apiVersion, sjsonOptionsInPlace); err != nil {
			return nil, nil, fmt.Errorf("failed to set %s: %w", anthropicVersionKey, err)
		}
	default:
		reqPath = o.path
		if body, err = sjson.SetBytesOptions(body, "model", o.requestModel, sjsonOptionsInPlace); err != nil {
			return nil, nil, fmt.Errorf("failed to set model: %w", err)
		}
		newHeaders = append(newHeaders, internalapi.Header{anthropicVersionHeaderName, anthropicAPIVersion})
	}

	newHeaders = append(newHeaders,
		internalapi.Header{pathHeaderName, reqPath},
		internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(body))},
	)
	return newHeaders, body, nil
}

// ResponseHeaders implements [OpenAIResponsesTranslator.ResponseHeaders].
func (o *openAIToAnthropicTranslatorV1Responses) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	if o.stream != nil {
		return []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}, nil
	}
	return nil, nil
}

// ResponseBody implements [OpenAIResponsesTranslator.ResponseBody].
func (o *openAIToAnthropicTranslatorV1Responses) ResponseBody(_ map[string]string, body io.Reader, endOfStream bool, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	if o.stream != nil {
		return o.stream.process(body, endOfStream, span)
	}

	var msg anthropic.Message
	if err = json.NewDecoder(body).Decode(&msg); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal Anthropic message: %w", err)
	}
	resp := o.responseSkeleton()
	tokenUsage = anthropicMessageToResponse(&resp, &msg, o.now())
	newBody, err = json.Marshal(resp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to marshal response: %w", err)
	}
	if span != nil {
		span.RecordResponse(&resp)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return newHeaders, newBody, tokenUsage, resp.Model, nil
}

// ResponseError implements [OpenAIResponsesTranslator.ResponseError].
// It converts an Anthropic error body ({"type":"error","error":{"type","message"}}) to the
// OpenAI error format.
func (o *openAIToAnthropicTranslatorV1Responses) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	statusCode := respHeaders[statusHeaderName]
	openaiError := openai.Error{Type: "error", Error: openai.ErrorType{Code: &statusCode}}
	if strings.Contains(respHeaders[contentTypeHeaderName], jsonContentType) {
		var anthropicError anthropic.ErrorResponse
		if err = json.NewDecoder(body).Decode(&anthropicError); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal JSON error body: %w", err)
		}
		openaiError.Error.Type = anthropicError.Error.Type
		openaiError.Error.Message = anthropicError.Error.Message
	} else {
		buf, readErr := io.ReadAll(body)
		if readErr != nil {
			return nil, nil, fmt.Errorf("failed to read raw error body: %w", readErr)
		}
		openaiError.Error.Type = "AnthropicBackendError"
		if o.apiSchema == filterapi.APISchemaGCPAnthropic {
			openaiError.Error.Type = gcpBackendError
		}
		openaiError.Error.Message = string(buf)
	}
	newBody, err = json.Marshal(openaiError)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal OpenAI error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return newHeaders, newBody, nil
}

// responseSkeleton returns the in-progress Response that echoes the request configuration,
// as the OpenAI Responses API does in every Response object.
func (o *openAIToAnthropicTranslatorV1Responses) responseSkeleton() openai.Response {
	req := o.request
	resp := openai.Response{
		CreatedAt:         openai.JSONUNIXTime(o.now()),
		Object:            "response",
		Model:             o.requestModel,
		Status:            "in_progress",
		Output:            []openai.ResponseOutputItemUnion{},
		ParallelToolCalls: cmp.Or(req.ParallelToolCalls, ptr.To(true)),
		Temperature:       1,
		TopP:              1,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		MaxOutputTokens:   req.MaxOutputTokens,
		Metadata:          req.Metadata,
		Reasoning:         req.Reasoning,
		Text:              openai.ResponseTextConfig{Format: req.Text.Format, Verbosity: req.Text.Verbosity},
		Truncation:        cmp.Or(req.Truncation, "disabled"),
		ServiceTier:       req.ServiceTier,
		PromptCacheKey:    req.PromptCacheKey,
		SafetyIdentifier:  req.SafetyIdentifier,
		User:              req.User,
		Store:             ptr.To(false),
		Background:        ptr.To(false),
	}
	if req.Temperature != nil {
		resp.Temperature = *req.Temperature
	}
	if req.TopP != nil {
		resp.TopP = *req.TopP
	}
	if resp.ToolChoice == (openai.ResponseToolChoiceUnion{}) {
		resp.ToolChoice.OfToolChoiceMode = ptr.To("auto")
	}
	if resp.Text.Format == (openai.ResponseFormatTextConfigUnionParam{}) {
		resp.Text.Format.OfText = &openai.ResponseFormatTextParam{Type: "text"}
	}
	if req.Instructions != "" {
		resp.Instructions.OfString = &req.Instructions
	}
	return resp
}

// ---------------------------------------------------------------------------------------------
// Request translation.
// ---------------------------------------------------------------------------------------------

// responsesToAnthropicParams translates a Responses request into Anthropic Messages parameters.
// The model is not set: the caller places it in the body or path depending on the backend.
func responsesToAnthropicParams(req *openai.ResponseRequest, model internalapi.RequestModel) (*anthropic.MessageNewParams, error) {
	if err := validateResponsesRequestForAnthropic(req); err != nil {
		return nil, err
	}

	messages, system, err := responsesInputToAnthropicMessages(req)
	if err != nil {
		return nil, err
	}
	params := &anthropic.MessageNewParams{Messages: messages, System: system}
	// max_tokens is required by Anthropic but max_output_tokens is optional in OpenAI. As in
	// buildAnthropicParams for Chat Completions, an unset value is sent as 0 so that Anthropic
	// rejects the request.
	if req.MaxOutputTokens != nil {
		params.MaxTokens = *req.MaxOutputTokens
	}

	if params.Tools, err = responsesToolsToAnthropic(req.Tools); err != nil {
		return nil, err
	}
	if params.ToolChoice, err = responsesToolChoiceToAnthropic(req.ToolChoice, req.ParallelToolCalls, len(params.Tools) > 0); err != nil {
		return nil, err
	}

	if req.Temperature != nil {
		if err = validateTemperatureForAnthropic(req.Temperature); err != nil {
			return nil, err
		}
		params.Temperature = anthropic.Float(*req.Temperature)
	}
	if req.TopP != nil {
		params.TopP = anthropic.Float(*req.TopP)
	}
	if user := cmp.Or(req.SafetyIdentifier, req.User); user != "" {
		params.Metadata.UserID = anthropic.String(user)
	}
	switch req.ServiceTier {
	case "auto":
		params.ServiceTier = anthropic.MessageNewParamsServiceTierAuto
	case "default":
		params.ServiceTier = anthropic.MessageNewParamsServiceTierStandardOnly
	}

	if f := req.Text.Format.OfJSONSchema; f != nil {
		params.OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: f.Schema}
	}
	if err = applyResponsesReasoningToAnthropic(params, req.Reasoning, model); err != nil {
		return nil, err
	}
	return params, nil
}

// unsupportedResponsesFeature returns the user-facing error for a Responses feature that the
// Anthropic Messages API cannot honor.
func unsupportedResponsesFeature(format string, args ...any) error {
	return fmt.Errorf("%w: "+format+" is not supported when the backend uses the Anthropic Messages API", append([]any{internalapi.ErrInvalidRequestBody}, args...)...)
}

// validateResponsesRequestForAnthropic rejects request parameters that Anthropic cannot honor.
// Values equal to the OpenAI default are accepted because they request no behavior.
func validateResponsesRequestForAnthropic(req *openai.ResponseRequest) error {
	switch {
	case req.PreviousResponseID != "":
		return unsupportedResponsesFeature("previous_response_id (server-side conversation state)")
	case req.Conversation.OfString != nil || req.Conversation.OfConversationObject != nil:
		return unsupportedResponsesFeature("conversation (server-side conversation state)")
	case req.Store != nil && *req.Store:
		return unsupportedResponsesFeature("store=true (stored responses); set store to false")
	case req.Background != nil && *req.Background:
		return unsupportedResponsesFeature("background=true (background responses)")
	case req.Prompt.ID != "":
		return unsupportedResponsesFeature("prompt (stored prompt templates)")
	case len(req.ContextManagement) > 0:
		return unsupportedResponsesFeature("context_management")
	case req.Truncation != "" && req.Truncation != "disabled":
		return unsupportedResponsesFeature("truncation=%q", req.Truncation)
	case req.TopLogprobs != nil && *req.TopLogprobs > 0:
		return unsupportedResponsesFeature("top_logprobs")
	case req.PresencePenalty != nil && *req.PresencePenalty != 0:
		return unsupportedResponsesFeature("presence_penalty")
	case req.FrequencyPenalty != nil && *req.FrequencyPenalty != 0:
		return unsupportedResponsesFeature("frequency_penalty")
	case req.ServiceTier != "" && req.ServiceTier != "auto" && req.ServiceTier != "default":
		return unsupportedResponsesFeature("service_tier=%q", req.ServiceTier)
	case req.PromptCacheRetention != "" && req.PromptCacheRetention != "in-memory" && req.PromptCacheRetention != "in_memory":
		return unsupportedResponsesFeature("prompt_cache_retention=%q", req.PromptCacheRetention)
	case req.Text.Verbosity != "" && req.Text.Verbosity != "medium":
		return unsupportedResponsesFeature("text.verbosity=%q", req.Text.Verbosity)
	case req.Text.Format.OfJSONObject != nil:
		return unsupportedResponsesFeature("text.format type \"json_object\"; use \"json_schema\"")
	}
	for _, include := range req.Include {
		if include != responsesIncludeReasoningEncryptedContent {
			return unsupportedResponsesFeature("include value %q", include)
		}
	}
	return nil
}

// responsesToolsToAnthropic converts Responses function tools to Anthropic custom tools.
// OpenAI-hosted tools (web search, file search, MCP, code interpreter, ...) are rejected.
func responsesToolsToAnthropic(tools []openai.ResponseToolUnion) ([]anthropic.ToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i := range tools {
		f := tools[i].OfFunction
		if f == nil {
			return nil, unsupportedResponsesFeature("tool type %q (only \"function\" tools can be translated)", responseToolType(&tools[i]))
		}
		schema, err := openAIToolParamsToAnthropicInputSchema(f.Parameters)
		if err != nil {
			return nil, fmt.Errorf("%w: tool %q: %w", internalapi.ErrInvalidRequestBody, f.Name, err)
		}
		tool := anthropic.ToolParam{Name: f.Name, InputSchema: schema}
		if f.Description != "" {
			tool.Description = anthropic.String(f.Description)
		}
		if f.Strict != nil && *f.Strict {
			tool.Strict = anthropic.Bool(true)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

// responseToolType returns the "type" of a tool for error messages.
func responseToolType(t *openai.ResponseToolUnion) string {
	raw, err := json.Marshal(t)
	if err != nil {
		return "unknown"
	}
	var typed struct {
		Type string `json:"type"`
	}
	if err = json.Unmarshal(raw, &typed); err != nil || typed.Type == "" {
		return "unknown"
	}
	return typed.Type
}

// responsesToolChoiceToAnthropic converts tool_choice and parallel_tool_calls.
func responsesToolChoiceToAnthropic(choice openai.ResponseToolChoiceUnion, parallelToolCalls *bool, hasTools bool) (anthropic.ToolChoiceUnionParam, error) {
	disableParallel := parallelToolCalls != nil && !*parallelToolCalls
	var out anthropic.ToolChoiceUnionParam
	switch {
	case choice.OfToolChoiceMode != nil:
		switch mode := *choice.OfToolChoiceMode; mode {
		case "auto":
			out.OfAuto = &anthropic.ToolChoiceAutoParam{}
		case "required":
			out.OfAny = &anthropic.ToolChoiceAnyParam{}
		case "none":
			out.OfNone = &anthropic.ToolChoiceNoneParam{}
		default:
			return out, fmt.Errorf("%w: unknown tool_choice %q", internalapi.ErrInvalidRequestBody, mode)
		}
	case choice.OfFunctionTool != nil:
		out.OfTool = &anthropic.ToolChoiceToolParam{Name: choice.OfFunctionTool.Name}
	case choice.OfAllowedTools != nil:
		return out, unsupportedResponsesFeature("tool_choice type \"allowed_tools\"")
	case choice != (openai.ResponseToolChoiceUnion{}):
		return out, unsupportedResponsesFeature("tool_choice for non-function tools")
	default:
		if !disableParallel || !hasTools {
			return out, nil
		}
		out.OfAuto = &anthropic.ToolChoiceAutoParam{}
	}
	if !hasTools && out.OfNone == nil && out.OfAuto == nil {
		return out, fmt.Errorf("%w: tool_choice requires at least one tool", internalapi.ErrInvalidRequestBody)
	}
	if !hasTools {
		// Anthropic rejects tool_choice without tools; auto and none are no-ops without tools.
		return anthropic.ToolChoiceUnionParam{}, nil
	}
	if disableParallel {
		switch {
		case out.OfAuto != nil:
			out.OfAuto.DisableParallelToolUse = anthropic.Bool(true)
		case out.OfAny != nil:
			out.OfAny.DisableParallelToolUse = anthropic.Bool(true)
		case out.OfTool != nil:
			out.OfTool.DisableParallelToolUse = anthropic.Bool(true)
		}
	}
	return out, nil
}

// applyResponsesReasoningToAnthropic maps reasoning.effort and reasoning.summary.
//
// Models with adaptive thinking get thinking.type=adaptive plus output_config.effort; when a
// summary is requested, thinking.display=summarized, because newer models omit thinking text by
// default. Extended-thinking-only models get thinking.type=enabled with a budget derived from the
// effort. Effort "none" disables thinking.
func applyResponsesReasoningToAnthropic(params *anthropic.MessageNewParams, reasoning openai.ReasoningParam, model internalapi.RequestModel) error {
	effort := reasoning.Effort
	wantSummary := reasoning.Summary != "" || reasoning.GenerateSummary != ""
	extendedOnly := modelContainsAny(model, extendedThinkingOnlyModels)

	switch {
	case effort == "" && !wantSummary:
		return nil
	case effort == "none":
		if !extendedOnly {
			// Extended-thinking-only models do not think unless asked to.
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
		}
		return nil
	case extendedOnly:
		if effort == "" {
			return nil
		}
		budget, ok := responsesThinkingBudgets[effort]
		if !ok {
			return fmt.Errorf("%w: unsupported reasoning.effort %q", internalapi.ErrInvalidRequestBody, effort)
		}
		// budget_tokens must be below max_tokens. Clamp only a valid max_tokens: when it is unset
		// (0) or invalid, keep the effort budget and let Anthropic reject max_tokens itself.
		if params.MaxTokens > 0 {
			budget = min(budget, params.MaxTokens-1)
		}
		if budget < anthropicMinThinkingBudget {
			return fmt.Errorf("%w: max_output_tokens must be greater than %d to use reasoning with model %q",
				internalapi.ErrInvalidRequestBody, anthropicMinThinkingBudget, model)
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfEnabled: &anthropic.ThinkingConfigEnabledParam{BudgetTokens: budget}}
		if effortAvailable(model) {
			return setResponsesEffort(params, effort)
		}
		return nil
	default:
		adaptive := &anthropic.ThinkingConfigAdaptiveParam{}
		if wantSummary {
			adaptive.Display = anthropic.ThinkingConfigAdaptiveDisplaySummarized
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: adaptive}
		if effort == "" {
			return nil
		}
		return setResponsesEffort(params, effort)
	}
}

func setResponsesEffort(params *anthropic.MessageNewParams, effort string) error {
	if effort == "minimal" {
		effort = string(openai.ReasoningEffortLow)
	}
	mapped, err := mapReasoningEffortToOutputConfigEffort(openai.ReasoningEffort(effort))
	if err != nil {
		return err
	}
	params.OutputConfig.Effort = mapped
	return nil
}

// anthropicMessagesBuilder accumulates Anthropic messages, merging consecutive blocks with the
// same role into one message as the Anthropic API requires alternating roles.
type anthropicMessagesBuilder struct {
	messages []anthropic.MessageParam
	system   []anthropic.TextBlockParam
}

func (b *anthropicMessagesBuilder) add(role anthropic.MessageParamRole, blocks ...anthropic.ContentBlockParamUnion) {
	if len(blocks) == 0 {
		return
	}
	if n := len(b.messages); n > 0 && b.messages[n-1].Role == role {
		b.messages[n-1].Content = append(b.messages[n-1].Content, blocks...)
		return
	}
	b.messages = append(b.messages, anthropic.MessageParam{Role: role, Content: blocks})
}

func (b *anthropicMessagesBuilder) addSystem(text string) {
	if text != "" {
		b.system = append(b.system, anthropic.TextBlockParam{Text: text})
	}
}

// responsesInputToAnthropicMessages converts instructions and input items into Anthropic system
// blocks and messages. System and developer messages become system blocks because Anthropic
// accepts system content only at the top level.
func responsesInputToAnthropicMessages(req *openai.ResponseRequest) ([]anthropic.MessageParam, []anthropic.TextBlockParam, error) {
	var b anthropicMessagesBuilder
	b.addSystem(req.Instructions)

	if req.Input.OfString != nil {
		b.add(anthropic.MessageParamRoleUser, textBlocks(*req.Input.OfString)...)
	}
	for i := range req.Input.OfInputItemList {
		if err := addResponsesInputItem(&b, &req.Input.OfInputItemList[i]); err != nil {
			return nil, nil, fmt.Errorf("input[%d]: %w", i, err)
		}
	}
	return b.messages, b.system, nil
}

func addResponsesInputItem(b *anthropicMessagesBuilder, item *openai.ResponseInputItemUnionParam) error {
	switch {
	case item.OfMessage != nil:
		m := item.OfMessage
		if m.Content.OfString != nil {
			return addResponsesMessage(b, m.Role, textBlocks(*m.Content.OfString))
		}
		blocks, err := responsesInputContentToAnthropic(m.Content.OfInputItemContentList)
		if err != nil {
			return err
		}
		return addResponsesMessage(b, m.Role, blocks)
	case item.OfInputMessage != nil:
		blocks, err := responsesInputContentToAnthropic(item.OfInputMessage.Content)
		if err != nil {
			return err
		}
		return addResponsesMessage(b, item.OfInputMessage.Role, blocks)
	case item.OfOutputMessage != nil:
		return addResponsesMessage(b, "assistant", responsesOutputContentToAnthropic(item.OfOutputMessage.Content))
	case item.OfFunctionCall != nil:
		fc := item.OfFunctionCall
		args := cmp.Or(strings.TrimSpace(fc.Arguments), "{}")
		if !gjson.Valid(args) {
			return fmt.Errorf("%w: function_call %q arguments are not valid JSON", internalapi.ErrInvalidRequestBody, fc.CallID)
		}
		b.add(anthropic.MessageParamRoleAssistant, anthropic.NewToolUseBlock(fc.CallID, json.RawMessage(args), fc.Name))
		return nil
	case item.OfFunctionCallOutput != nil:
		block, err := responsesFunctionCallOutputToAnthropic(item.OfFunctionCallOutput)
		if err != nil {
			return err
		}
		b.add(anthropic.MessageParamRoleUser, block)
		return nil
	case item.OfReasoning != nil:
		if block, ok := responsesReasoningToAnthropic(item.OfReasoning); ok {
			b.add(anthropic.MessageParamRoleAssistant, block)
		}
		return nil
	default:
		return unsupportedResponsesFeature("input item type %q", responseInputItemType(item))
	}
}

// responseInputItemType returns the "type" of an input item for error messages.
func responseInputItemType(item *openai.ResponseInputItemUnionParam) string {
	raw, err := json.Marshal(item)
	if err != nil {
		return "unknown"
	}
	var typed struct {
		Type string `json:"type"`
	}
	if err = json.Unmarshal(raw, &typed); err != nil || typed.Type == "" {
		return "unknown"
	}
	return typed.Type
}

func addResponsesMessage(b *anthropicMessagesBuilder, role string, blocks []anthropic.ContentBlockParamUnion) error {
	switch role {
	case "user":
		b.add(anthropic.MessageParamRoleUser, blocks...)
	case "assistant":
		b.add(anthropic.MessageParamRoleAssistant, blocks...)
	case "system", "developer":
		for i := range blocks {
			if blocks[i].OfText == nil {
				return fmt.Errorf("%w: %s messages may contain only text", internalapi.ErrInvalidRequestBody, role)
			}
			b.addSystem(blocks[i].OfText.Text)
		}
	default:
		return fmt.Errorf("%w: unknown message role %q", internalapi.ErrInvalidRequestBody, role)
	}
	return nil
}

// textBlocks returns a single text block, or none for empty text because Anthropic rejects
// empty text blocks.
func textBlocks(text string) []anthropic.ContentBlockParamUnion {
	if text == "" {
		return nil
	}
	return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(text)}
}

func responsesInputContentToAnthropic(parts []openai.ResponseInputContentUnionParam) ([]anthropic.ContentBlockParamUnion, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, part := range parts {
		var (
			block anthropic.ContentBlockParamUnion
			err   error
		)
		switch {
		case part.OfInputText != nil:
			blocks = append(blocks, textBlocks(part.OfInputText.Text)...)
			continue
		case part.OfInputImage != nil:
			block, err = responsesImageToAnthropic(part.OfInputImage.ImageURL, part.OfInputImage.FileID)
		case part.OfInputFile != nil:
			f := part.OfInputFile
			block, err = responsesFileToAnthropic(f.FileData, f.FileURL, f.FileID, f.Filename)
		default:
			err = fmt.Errorf("%w: empty input content part", internalapi.ErrInvalidRequestBody)
		}
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func responsesImageToAnthropic(imageURL, fileID string) (anthropic.ContentBlockParamUnion, error) {
	if imageURL == "" {
		if fileID != "" {
			return anthropic.ContentBlockParamUnion{}, unsupportedResponsesFeature("input_image with file_id (OpenAI Files)")
		}
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("%w: input_image requires image_url", internalapi.ErrInvalidRequestBody)
	}
	block, err := convertImageContentToAnthropic(imageURL, nil)
	if err != nil {
		return block, fmt.Errorf("%w: %w", internalapi.ErrInvalidRequestBody, err)
	}
	return block, nil
}

// responsesFileToAnthropic converts an input_file to an Anthropic PDF document block. Anthropic
// accepts only PDFs as base64 or URL document sources.
func responsesFileToAnthropic(fileData, fileURL, fileID, filename string) (anthropic.ContentBlockParamUnion, error) {
	switch {
	case fileData != "":
		data := fileData
		if strings.HasPrefix(fileData, "data:") {
			mediaType, raw, err := parseDataURI(fileData)
			if err != nil {
				return anthropic.ContentBlockParamUnion{}, fmt.Errorf("%w: input_file: %w", internalapi.ErrInvalidRequestBody, err)
			}
			if mediaType != string(constant.ValueOf[constant.ApplicationPDF]()) {
				return anthropic.ContentBlockParamUnion{}, unsupportedResponsesFeature("input_file media type %q (only application/pdf)", mediaType)
			}
			data = base64.StdEncoding.EncodeToString(raw)
		} else if !strings.HasSuffix(strings.ToLower(filename), ".pdf") {
			return anthropic.ContentBlockParamUnion{}, unsupportedResponsesFeature("input_file %q (only PDF files)", filename)
		}
		return anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{Data: data}), nil
	case fileURL != "":
		return anthropic.NewDocumentBlock(anthropic.URLPDFSourceParam{URL: fileURL}), nil
	case fileID != "":
		return anthropic.ContentBlockParamUnion{}, unsupportedResponsesFeature("input_file with file_id (OpenAI Files)")
	default:
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("%w: input_file requires file_data or file_url", internalapi.ErrInvalidRequestBody)
	}
}

func responsesOutputContentToAnthropic(content openai.ResponseOutputMessageContentUnion) []anthropic.ContentBlockParamUnion {
	if content.OfString != nil {
		return textBlocks(*content.OfString)
	}
	var blocks []anthropic.ContentBlockParamUnion
	for _, part := range content.OfContentArray {
		switch {
		case part.OfOutputText != nil:
			blocks = append(blocks, textBlocks(part.OfOutputText.Text)...)
		case part.OfRefusal != nil:
			blocks = append(blocks, textBlocks(part.OfRefusal.Refusal)...)
		}
	}
	return blocks
}

func responsesFunctionCallOutputToAnthropic(out *openai.ResponseInputItemFunctionCallOutputParam) (anthropic.ContentBlockParamUnion, error) {
	result := anthropic.ToolResultBlockParam{ToolUseID: out.CallID}
	if out.Output.OfString != nil {
		if *out.Output.OfString != "" {
			result.Content = []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: *out.Output.OfString}}}
		}
		return anthropic.ContentBlockParamUnion{OfToolResult: &result}, nil
	}
	for _, item := range out.Output.OfResponseFunctionCallOutputItemArray {
		switch {
		case item.OfInputText != nil:
			if item.OfInputText.Text != "" {
				result.Content = append(result.Content, anthropic.ToolResultBlockParamContentUnion{OfText: &anthropic.TextBlockParam{Text: item.OfInputText.Text}})
			}
		case item.OfInputImage != nil:
			block, err := responsesImageToAnthropic(item.OfInputImage.ImageURL, item.OfInputImage.FileID)
			if err != nil {
				return block, err
			}
			if block.OfImage != nil {
				result.Content = append(result.Content, anthropic.ToolResultBlockParamContentUnion{OfImage: block.OfImage})
			} else {
				result.Content = append(result.Content, anthropic.ToolResultBlockParamContentUnion{OfDocument: block.OfDocument})
			}
		case item.OfInputFile != nil:
			f := item.OfInputFile
			block, err := responsesFileToAnthropic(f.FileData, f.FileURL, f.FileID, f.Filename)
			if err != nil {
				return block, err
			}
			result.Content = append(result.Content, anthropic.ToolResultBlockParamContentUnion{OfDocument: block.OfDocument})
		default:
			return anthropic.ContentBlockParamUnion{}, unsupportedResponsesFeature("function_call_output content other than input_text, input_image, and input_file")
		}
	}
	return anthropic.ContentBlockParamUnion{OfToolResult: &result}, nil
}

// responsesReasoningToAnthropic restores a thinking or redacted_thinking block from a reasoning
// item produced by this translator. Reasoning items without Anthropic state (for example from an
// OpenAI model earlier in the conversation) cannot be replayed to Claude and are skipped.
func responsesReasoningToAnthropic(r *openai.ResponseReasoningItem) (anthropic.ContentBlockParamUnion, bool) {
	if data, ok := strings.CutPrefix(r.EncryptedContent, anthropicRedactedThinkingEncryptedPrefix); ok {
		return anthropic.NewRedactedThinkingBlock(data), true
	}
	signature, ok := strings.CutPrefix(r.EncryptedContent, anthropicThinkingEncryptedPrefix)
	if !ok {
		return anthropic.ContentBlockParamUnion{}, false
	}
	var thinking strings.Builder
	for _, s := range r.Summary {
		thinking.WriteString(s.Text)
	}
	for _, c := range r.Content {
		thinking.WriteString(c.Text)
	}
	return anthropic.NewThinkingBlock(signature, thinking.String()), true
}

// ---------------------------------------------------------------------------------------------
// Response translation.
// ---------------------------------------------------------------------------------------------

// Output item IDs are derived from the Anthropic message ID and block index so that streaming
// and non-streaming translations of the same message agree.
func responsesIDSuffix(anthropicMessageID string) string {
	return strings.TrimPrefix(anthropicMessageID, "msg_")
}

func responsesOutputItemID(prefix, anthropicMessageID string, index int64) string {
	return prefix + "_" + responsesIDSuffix(anthropicMessageID) + "_" + strconv.FormatInt(index, 10)
}

func responsesFunctionCallItemID(toolUseID string) string { return "fc_" + toolUseID }

func newResponsesTextPart(text string) openai.ResponseOutputMessageContentArrayUnion {
	return openai.ResponseOutputMessageContentArrayUnion{OfOutputText: &openai.ResponseOutputTextParam{
		Type: "output_text", Text: text, Annotations: []openai.ResponseOutputTextAnnotationUnionParam{},
	}}
}

func newResponsesMessageItem(id, status string, parts []openai.ResponseOutputMessageContentArrayUnion) openai.ResponseOutputItemUnion {
	return openai.ResponseOutputItemUnion{OfOutputMessage: &openai.ResponseOutputMessage{
		ID: id, Type: "message", Role: "assistant", Status: status,
		Content: openai.ResponseOutputMessageContentUnion{OfContentArray: parts},
	}}
}

func newResponsesReasoningItem(id, thinking, encryptedContent string) openai.ResponseOutputItemUnion {
	summary := []openai.ResponseReasoningItemSummaryParam{}
	if thinking != "" {
		summary = append(summary, openai.ResponseReasoningItemSummaryParam{Type: "summary_text", Text: thinking})
	}
	return openai.ResponseOutputItemUnion{OfReasoning: &openai.ResponseReasoningItem{
		ID: id, Type: "reasoning", Summary: summary, EncryptedContent: encryptedContent,
	}}
}

func newResponsesFunctionCallItem(toolUseID, name, arguments, status string) openai.ResponseOutputItemUnion {
	return openai.ResponseOutputItemUnion{OfFunctionCall: &openai.ResponseFunctionToolCall{
		ID: responsesFunctionCallItemID(toolUseID), Type: "function_call", CallID: toolUseID,
		Name: name, Arguments: arguments, Status: status,
	}}
}

// anthropicUsageCounts holds the Anthropic usage fields. Anthropic reports input_tokens excluding
// cache reads and writes, whereas OpenAI input_tokens includes cached tokens.
type anthropicUsageCounts struct {
	input, output, cacheRead, cacheCreation, thinking int64
}

func (u anthropicUsageCounts) responseUsage() *openai.ResponseUsage {
	input := u.input + u.cacheRead + u.cacheCreation
	return &openai.ResponseUsage{
		InputTokens:         input,
		InputTokensDetails:  openai.ResponseUsageInputTokensDetails{CachedTokens: u.cacheRead, CacheWriteTokens: u.cacheCreation},
		OutputTokens:        u.output,
		OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{ReasoningTokens: u.thinking},
		TotalTokens:         input + u.output,
	}
}

func (u anthropicUsageCounts) tokenUsage() metrics.TokenUsage {
	usage := metrics.ExtractTokenUsageFromExplicitCaching(u.input, u.output, &u.cacheRead, &u.cacheCreation)
	usage.SetReasoningTokens(uint32(u.thinking)) //nolint:gosec
	return usage
}

// finishResponse sets the terminal status of resp from the Anthropic stop reason.
func finishResponse(resp *openai.Response, stopReason string, completedAt time.Time) {
	switch anthropic.StopReason(stopReason) {
	case anthropic.StopReasonMaxTokens, "model_context_window_exceeded":
		resp.Status = "incomplete"
		resp.IncompleteDetails = openai.ResponseIncompleteDetails{Reason: "max_output_tokens"}
	case anthropic.StopReasonRefusal:
		resp.Status = "incomplete"
		resp.IncompleteDetails = openai.ResponseIncompleteDetails{Reason: "content_filter"}
	default:
		resp.Status = "completed"
		resp.CompletedAt = ptr.To(openai.JSONUNIXTime(completedAt))
	}
}

// anthropicMessageToResponse fills resp, which echoes the request, from a non-streaming Anthropic message.
func anthropicMessageToResponse(resp *openai.Response, msg *anthropic.Message, completedAt time.Time) metrics.TokenUsage {
	resp.ID = "resp_" + responsesIDSuffix(msg.ID)
	resp.Model = cmp.Or(msg.Model, resp.Model)
	for i := range msg.Content {
		block := &msg.Content[i]
		index := int64(i)
		switch block.Type {
		case "text":
			resp.Output = append(resp.Output, newResponsesMessageItem(
				responsesOutputItemID("msg", msg.ID, index), "completed",
				[]openai.ResponseOutputMessageContentArrayUnion{newResponsesTextPart(block.Text)}))
		case "thinking":
			resp.Output = append(resp.Output, newResponsesReasoningItem(
				responsesOutputItemID("rs", msg.ID, index), block.Thinking, anthropicThinkingEncryptedPrefix+block.Signature))
		case "redacted_thinking":
			resp.Output = append(resp.Output, newResponsesReasoningItem(
				responsesOutputItemID("rs", msg.ID, index), "", anthropicRedactedThinkingEncryptedPrefix+block.Data))
		case "tool_use":
			args := cmp.Or(string(bytes.TrimSpace(block.Input)), "{}")
			resp.Output = append(resp.Output, newResponsesFunctionCallItem(block.ID, block.Name, args, "completed"))
			// Other block types come from Anthropic server tools, which this translator never requests.
		}
	}
	finishResponse(resp, string(msg.StopReason), completedAt)
	usage := anthropicUsageCounts{
		input:         msg.Usage.InputTokens,
		output:        msg.Usage.OutputTokens,
		cacheRead:     msg.Usage.CacheReadInputTokens,
		cacheCreation: msg.Usage.CacheCreationInputTokens,
		thinking:      msg.Usage.OutputTokensDetails.ThinkingTokens,
	}
	resp.Usage = usage.responseUsage()
	return usage.tokenUsage()
}

// ---------------------------------------------------------------------------------------------
// Streaming translation.
// ---------------------------------------------------------------------------------------------

// responsesStreamBlock tracks one Anthropic content block that is being streamed as one
// Responses output item.
type responsesStreamBlock struct {
	kind        string // "text", "thinking", "redacted_thinking", or "tool_use".
	outputIndex int64
	itemID      string
	text        strings.Builder // Text, thinking text, or tool arguments.
	signature   string          // Thinking signature or redacted thinking data.
	toolUseID   string
	name        string
}

// responsesAnthropicStream converts an Anthropic Messages SSE stream into Responses SSE events:
// response.created, response.in_progress, output item and content part events with their
// deltas, and finally response.completed, response.incomplete, or response.failed.
type responsesAnthropicStream struct {
	buffered   []byte
	sequence   int64
	response   openai.Response
	blocks     map[int64]*responsesStreamBlock
	usage      anthropicUsageCounts
	stopReason string
	messageID  string
	finished   bool
	now        func() time.Time
}

func newResponsesAnthropicStream(skeleton *openai.Response, now func() time.Time) *responsesAnthropicStream {
	return &responsesAnthropicStream{response: *skeleton, blocks: map[int64]*responsesStreamBlock{}, now: now}
}

func (s *responsesAnthropicStream) process(body io.Reader, endOfStream bool, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	chunk, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read body: %w", err)
	}
	s.buffered = append(s.buffered, chunk...)
	out := []byte{}
	for {
		event, remaining, ok := nextSSEEvent(s.buffered)
		if !ok {
			if !endOfStream || len(bytes.TrimSpace(s.buffered)) == 0 {
				break
			}
			// Flush a final event that lacks the trailing blank line.
			event, remaining = s.buffered, nil
		}
		s.buffered = remaining
		if err = s.handleSSEEvent(event, &out, span); err != nil {
			return nil, nil, tokenUsage, "", err
		}
	}
	if endOfStream && !s.finished {
		// The upstream closed the stream before message_stop.
		s.fail(&out, span, "server_error", "the Anthropic stream ended before the message was complete")
	}
	return nil, out, s.usage.tokenUsage(), s.response.Model, nil
}

// handleSSEEvent converts one Anthropic SSE event. Anthropic repeats the event type inside the
// JSON data, so only the data lines are needed.
func (s *responsesAnthropicStream) handleSSEEvent(event []byte, out *[]byte, span tracingapi.ResponsesSpan) error {
	var data []byte
	for line := range bytes.SplitSeq(event, []byte("\n")) {
		if d, ok := cutSSEDataPrefix(bytes.TrimRight(line, "\r")); ok {
			data = append(data, d...)
		}
	}
	if len(data) == 0 || s.finished {
		return nil
	}
	var ev anthropic.MessageStreamEventUnion
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("failed to unmarshal Anthropic stream event: %w", err)
	}
	switch ev.Type {
	case "message_start":
		s.messageID = ev.Message.ID
		s.response.ID = "resp_" + responsesIDSuffix(ev.Message.ID)
		s.response.Model = cmp.Or(ev.Message.Model, s.response.Model)
		u := ev.Message.Usage
		s.usage = anthropicUsageCounts{
			input: u.InputTokens, output: u.OutputTokens,
			cacheRead: u.CacheReadInputTokens, cacheCreation: u.CacheCreationInputTokens,
			thinking: u.OutputTokensDetails.ThinkingTokens,
		}
		created := s.response
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseCreated: &openai.ResponseCreatedEvent{
			Type: "response.created", SequenceNumber: s.nextSequence(), Response: created,
		}})
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseInProgress: &openai.ResponseInProgressEvent{
			Type: "response.in_progress", SequenceNumber: s.nextSequence(), Response: created,
		}})
	case "content_block_start":
		s.startBlock(ev.Index, &ev.ContentBlock, out, span)
	case "content_block_delta":
		s.blockDelta(ev.Index, &ev.Delta, out, span)
	case "content_block_stop":
		s.stopBlock(ev.Index, out, span)
	case "message_delta":
		if ev.Delta.StopReason != "" {
			s.stopReason = string(ev.Delta.StopReason)
		}
		// message_delta usage is cumulative; fields absent from the event keep their values.
		u := ev.Usage
		if u.JSON.InputTokens.Valid() {
			s.usage.input = u.InputTokens
		}
		if u.JSON.OutputTokens.Valid() {
			s.usage.output = u.OutputTokens
		}
		if u.JSON.CacheReadInputTokens.Valid() {
			s.usage.cacheRead = u.CacheReadInputTokens
		}
		if u.JSON.CacheCreationInputTokens.Valid() {
			s.usage.cacheCreation = u.CacheCreationInputTokens
		}
		if u.OutputTokensDetails.JSON.ThinkingTokens.Valid() {
			s.usage.thinking = u.OutputTokensDetails.ThinkingTokens
		}
	case "message_stop":
		s.finish(out, span)
	case "error":
		var errResp anthropic.ErrorResponse
		if err := json.Unmarshal(data, &errResp); err != nil {
			return fmt.Errorf("failed to unmarshal Anthropic stream error: %w", err)
		}
		code := "server_error"
		if errResp.Error.Type == "rate_limit_error" {
			code = "rate_limit_exceeded"
		}
		s.fail(out, span, code, errResp.Error.Message)
	}
	// ping and unknown events carry nothing for the client.
	return nil
}

func (s *responsesAnthropicStream) nextSequence() int64 {
	seq := s.sequence
	s.sequence++
	return seq
}

// emit writes ev as a Responses SSE event and records it on the span.
func (s *responsesAnthropicStream) emit(out *[]byte, span tracingapi.ResponsesSpan, ev *openai.ResponseStreamEventUnion) {
	data, err := json.Marshal(ev)
	if err != nil {
		// Every emitted union has exactly one member set, so marshaling cannot fail.
		panic(fmt.Sprintf("BUG: failed to marshal Responses stream event: %v", err))
	}
	*out = append(*out, "event: "...)
	*out = append(*out, ev.GetEventType()...)
	*out = append(*out, "\ndata: "...)
	*out = append(*out, data...)
	*out = append(*out, "\n\n"...)
	if span != nil {
		span.RecordResponseChunk(ev)
	}
}

func (s *responsesAnthropicStream) startBlock(index int64, cb *anthropic.ContentBlockStartEventContentBlockUnion, out *[]byte, span tracingapi.ResponsesSpan) {
	block := &responsesStreamBlock{kind: cb.Type, outputIndex: int64(len(s.response.Output))}
	var item openai.ResponseOutputItemUnion
	switch cb.Type {
	case "text":
		block.itemID = responsesOutputItemID("msg", s.messageID, index)
		item = newResponsesMessageItem(block.itemID, "in_progress", []openai.ResponseOutputMessageContentArrayUnion{})
	case "thinking":
		block.itemID = responsesOutputItemID("rs", s.messageID, index)
		item = newResponsesReasoningItem(block.itemID, "", "")
	case "redacted_thinking":
		block.itemID = responsesOutputItemID("rs", s.messageID, index)
		block.signature = cb.Data
		item = newResponsesReasoningItem(block.itemID, "", "")
	case "tool_use":
		block.toolUseID, block.name = cb.ID, cb.Name
		block.itemID = responsesFunctionCallItemID(cb.ID)
		item = newResponsesFunctionCallItem(cb.ID, cb.Name, "", "in_progress")
	default:
		// Server tool blocks are never requested by this translator.
		return
	}
	s.blocks[index] = block
	// Reserve the output slot; the final item replaces it at content_block_stop.
	s.response.Output = append(s.response.Output, item)
	s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseOutputItemAdded: &openai.ResponseOutputItemAddedEvent{
		Type: "response.output_item.added", SequenceNumber: s.nextSequence(), OutputIndex: block.outputIndex, Item: item,
	}})
	switch cb.Type {
	case "text":
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseContentPartAdded: &openai.ResponseContentPartAddedEvent{
			Type: "response.content_part.added", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, ContentIndex: 0,
			Part: openai.ResponseContentPartAddedEventPartUnion{OfResponseOutputText: newResponsesTextPart("").OfOutputText},
		}})
	case "thinking":
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseReasoningSummaryPartAdded: &openai.ResponseReasoningSummaryPartAddedEvent{
			Type: "response.reasoning_summary_part.added", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, SummaryIndex: 0,
			Part: openai.ResponseReasoningSummaryPartAddedEventPart{Type: "summary_text"},
		}})
	case "tool_use":
		// Anthropic streams tool input as input_json_delta events after an empty "input":{}, but
		// some Anthropic-compatible backends send the complete input in content_block_start.
		if input, ok := cb.Input.(map[string]any); ok && len(input) > 0 {
			if args, err := json.Marshal(input); err == nil {
				s.blockDelta(index, &anthropic.MessageStreamEventUnionDelta{Type: "input_json_delta", PartialJSON: string(args)}, out, span)
			}
		}
	}
}

func (s *responsesAnthropicStream) blockDelta(index int64, d *anthropic.MessageStreamEventUnionDelta, out *[]byte, span tracingapi.ResponsesSpan) {
	block := s.blocks[index]
	if block == nil {
		return
	}
	switch d.Type {
	case "text_delta":
		block.text.WriteString(d.Text)
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseTextDelta: &openai.ResponseTextDeltaEvent{
			Type: "response.output_text.delta", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, ContentIndex: 0, Delta: d.Text, Logprobs: []openai.ResponseTextDeltaEventLogprob{},
		}})
	case "thinking_delta":
		if d.Thinking == "" {
			// Models with display=omitted send one empty thinking delta.
			return
		}
		block.text.WriteString(d.Thinking)
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseReasoningSummaryTextDelta: &openai.ResponseReasoningSummaryTextDeltaEvent{
			Type: "response.reasoning_summary_text.delta", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, SummaryIndex: 0, Delta: d.Thinking,
		}})
	case "signature_delta":
		block.signature += d.Signature
	case "input_json_delta":
		if d.PartialJSON == "" {
			return
		}
		block.text.WriteString(d.PartialJSON)
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseFunctionCallArgumentsDelta: &openai.ResponseFunctionCallArgumentsDeltaEvent{
			Type: "response.function_call_arguments.delta", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, Delta: d.PartialJSON,
		}})
	}
}

func (s *responsesAnthropicStream) stopBlock(index int64, out *[]byte, span tracingapi.ResponsesSpan) {
	block := s.blocks[index]
	if block == nil {
		return
	}
	delete(s.blocks, index)
	text := block.text.String()
	var item openai.ResponseOutputItemUnion
	switch block.kind {
	case "text":
		part := newResponsesTextPart(text)
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseTextDone: &openai.ResponseTextDoneEvent{
			Type: "response.output_text.done", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, ContentIndex: 0, Text: text, Logprobs: []openai.ResponseTextDoneEventLogprob{},
		}})
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseContentPartDone: &openai.ResponseContentPartDoneEvent{
			Type: "response.content_part.done", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, ContentIndex: 0,
			Part: openai.ResponseContentPartDoneEventPartUnion{OfResponseOutputText: part.OfOutputText},
		}})
		item = newResponsesMessageItem(block.itemID, "completed", []openai.ResponseOutputMessageContentArrayUnion{part})
	case "thinking":
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseReasoningSummaryTextDone: &openai.ResponseReasoningSummaryTextDoneEvent{
			Type: "response.reasoning_summary_text.done", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, SummaryIndex: 0, Text: text,
		}})
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseReasoningSummaryPartDone: &openai.ResponseReasoningSummaryPartDoneEvent{
			Type: "response.reasoning_summary_part.done", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, SummaryIndex: 0,
			Part: openai.ResponseReasoningSummaryPartDoneEventPart{Type: "summary_text", Text: text},
		}})
		item = newResponsesReasoningItem(block.itemID, text, anthropicThinkingEncryptedPrefix+block.signature)
	case "redacted_thinking":
		item = newResponsesReasoningItem(block.itemID, "", anthropicRedactedThinkingEncryptedPrefix+block.signature)
	case "tool_use":
		args := cmp.Or(strings.TrimSpace(text), "{}")
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseFunctionCallArgumentsDone: &openai.ResponseFunctionCallArgumentsDoneEvent{
			Type: "response.function_call_arguments.done", SequenceNumber: s.nextSequence(), ItemID: block.itemID,
			OutputIndex: block.outputIndex, Name: block.name, Arguments: args,
		}})
		item = newResponsesFunctionCallItem(block.toolUseID, block.name, args, "completed")
	}
	s.response.Output[block.outputIndex] = item
	s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseOutputItemDone: &openai.ResponseOutputItemDoneEvent{
		Type: "response.output_item.done", SequenceNumber: s.nextSequence(), OutputIndex: block.outputIndex, Item: item,
	}})
}

// finish emits response.completed or response.incomplete with the full output and usage.
func (s *responsesAnthropicStream) finish(out *[]byte, span tracingapi.ResponsesSpan) {
	s.finished = true
	finishResponse(&s.response, s.stopReason, s.now())
	s.response.Usage = s.usage.responseUsage()
	if s.response.Status == "completed" {
		s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseCompleted: &openai.ResponseCompletedEvent{
			Type: "response.completed", SequenceNumber: s.nextSequence(), Response: s.response,
		}})
		return
	}
	s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseIncomplete: &openai.ResponseIncompleteEvent{
		Type: "response.incomplete", SequenceNumber: s.nextSequence(), Response: s.response,
	}})
}

// fail emits response.failed with the usage reported so far.
func (s *responsesAnthropicStream) fail(out *[]byte, span tracingapi.ResponsesSpan, code, message string) {
	s.finished = true
	s.response.Status = "failed"
	s.response.Error = openai.ResponseError{Code: code, Message: message}
	s.response.Usage = s.usage.responseUsage()
	s.emit(out, span, &openai.ResponseStreamEventUnion{OfResponseFailed: &openai.ResponseFailedEvent{
		Type: "response.failed", SequenceNumber: s.nextSequence(), Response: s.response,
	}})
}
