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
	"mime"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"google.golang.org/genai"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// responsesIncludeReasoningEncryptedContent is the only `include` value the Vertex AI translation honors.
// Gemini thought signatures are always returned as reasoning items' encrypted_content, so the value is
// accepted whether or not the client asks for it.
const responsesIncludeReasoningEncryptedContent = "reasoning.encrypted_content"

// NewResponsesOpenAIToGCPVertexAITranslator implements [OpenAIResponsesTranslator] for OpenAI Responses
// to GCP Vertex AI Gemini translation. Requests are sent to the Gemini generateContent and
// streamGenerateContent methods, and their responses are converted back into Responses objects and events:
// https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/inference
// https://platform.openai.com/docs/api-reference/responses
func NewResponsesOpenAIToGCPVertexAITranslator(modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesTranslator {
	return &openAIToGCPVertexAITranslatorV1Responses{modelNameOverride: modelNameOverride}
}

// openAIToGCPVertexAITranslatorV1Responses translates the OpenAI Responses API to the GCP Vertex AI Gemini API.
//
// Vertex AI is stateless, so features that rely on OpenAI storing responses or conversations are rejected.
type openAIToGCPVertexAITranslatorV1Responses struct {
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
	// req is the original request. Its configuration is echoed back in the Response object.
	req *openai.ResponseRequest
	// buffered holds incomplete SSE bytes across streaming ResponseBody calls.
	buffered []byte
	// out accumulates the Responses output and, for streaming requests, the SSE events.
	out *responsesOutputBuilder
}

// RequestBody implements [OpenAIResponsesTranslator.RequestBody].
func (o *openAIToGCPVertexAITranslatorV1Responses) RequestBody(_ []byte, req *openai.ResponseRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	o.req = req
	o.requestModel = cmp.Or(o.modelNameOverride, req.Model)
	// RequestBody may run again on retry; start from a clean response state.
	o.buffered = nil
	o.out = newResponsesOutputBuilder(req, o.requestModel, req.Stream)

	gcpReq, err := responsesRequestToGemini(req, o.requestModel)
	if err != nil {
		return nil, nil, err
	}
	newBody, err = json.Marshal(gcpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling Gemini request: %w", err)
	}

	var p string
	if req.Stream {
		p = buildGCPModelPathSuffix(gcpModelPublisherGoogle, o.requestModel, gcpMethodStreamGenerateContent, "alt=sse")
	} else {
		p = buildGCPModelPathSuffix(gcpModelPublisherGoogle, o.requestModel, gcpMethodGenerateContent)
	}
	newHeaders = []internalapi.Header{
		{pathHeaderName, p},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseHeaders implements [OpenAIResponsesTranslator.ResponseHeaders].
func (o *openAIToGCPVertexAITranslatorV1Responses) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	if o.req != nil && o.req.Stream {
		return []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}, nil
	}
	return nil, nil
}

// ResponseBody implements [OpenAIResponsesTranslator.ResponseBody].
// Vertex AI resolves the model from the request path, so the response model is the
// response's modelVersion when present and the request model otherwise.
func (o *openAIToGCPVertexAITranslatorV1Responses) ResponseBody(_ map[string]string, body io.Reader, endOfStream bool, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	if o.req.Stream {
		return o.handleStreamingResponse(body, endOfStream, span)
	}

	gcr := &genai.GenerateContentResponse{}
	if err = json.NewDecoder(body).Decode(gcr); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("error decoding GCP response: %w", err)
	}
	o.out.addChunk(gcr)
	resp := o.out.finish()

	newBody, err = json.Marshal(resp)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("error marshaling Responses response: %w", err)
	}
	setTokenUsageFromResponse(&tokenUsage, resp)
	if span != nil {
		span.RecordResponse(resp)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return newHeaders, newBody, tokenUsage, resp.Model, nil
}

// handleStreamingResponse converts Gemini SSE chunks into Responses streaming events.
func (o *openAIToGCPVertexAITranslatorV1Responses) handleStreamingResponse(body io.Reader, endOfStream bool, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read streaming body: %w", err)
	}
	o.buffered = append(o.buffered, data...)
	for {
		event, remaining, ok := nextSSEEvent(o.buffered)
		if !ok {
			break
		}
		o.buffered = remaining
		for line := range bytes.SplitSeq(event, []byte("\n")) {
			payload, ok := cutSSEDataPrefix(bytes.TrimRight(line, "\r"))
			if !ok || len(payload) == 0 {
				continue
			}
			var chunk genai.GenerateContentResponse
			if err = json.Unmarshal(payload, &chunk); err != nil {
				return nil, nil, tokenUsage, "", fmt.Errorf("error decoding GCP streaming chunk: %w", err)
			}
			o.out.addChunk(&chunk)
		}
	}
	if endOfStream {
		o.out.finish()
	}

	if o.out.err != nil {
		return nil, nil, tokenUsage, "", o.out.err
	}
	newBody, events := o.out.takeEvents()
	if span != nil {
		for i := range events {
			span.RecordResponseChunk(&events[i])
		}
	}
	o.out.setTokenUsage(&tokenUsage)
	// Return a non-nil body even when nothing is ready so that Envoy does not forward
	// the raw Gemini bytes in STREAMED mode.
	if newBody == nil {
		newBody = []byte{}
	}
	return nil, newBody, tokenUsage, o.out.model(), nil
}

// ResponseError implements [OpenAIResponsesTranslator.ResponseError].
func (o *openAIToGCPVertexAITranslatorV1Responses) ResponseError(respHeaders map[string]string, body io.Reader) (
	[]internalapi.Header, []byte, error,
) {
	return convertGCPVertexAIErrorToOpenAI(respHeaders, body)
}

// ---------------------------------------------------------------------------------
// Request translation: OpenAI Responses -> Gemini GenerateContentRequest
// ---------------------------------------------------------------------------------

// responsesRequestToGemini converts a Responses request into a Gemini GenerateContentRequest.
// Every returned validation error wraps [internalapi.ErrInvalidRequestBody] so the client gets a 422.
func responsesRequestToGemini(req *openai.ResponseRequest, model internalapi.RequestModel) (*gcp.GenerateContentRequest, error) {
	if err := validateResponsesRequestForGemini(req); err != nil {
		return nil, err
	}
	contents, systemInstruction, err := responsesInputToGeminiContents(req, model)
	if err != nil {
		return nil, err
	}
	tools, toolConfig, err := responsesToolsToGemini(req.Tools, req.ToolChoice, model)
	if err != nil {
		return nil, err
	}
	generationConfig, err := responsesGenerationConfig(req, model)
	if err != nil {
		return nil, err
	}
	return &gcp.GenerateContentRequest{
		Contents:          contents,
		Tools:             tools,
		ToolConfig:        toolConfig,
		GenerationConfig:  generationConfig,
		SystemInstruction: systemInstruction,
	}, nil
}

// errResponsesUnsupported builds a 422 error for a Responses feature that Gemini on Vertex AI cannot honor.
func errResponsesUnsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s is not supported for GCP Vertex AI backends", internalapi.ErrInvalidRequestBody, fmt.Sprintf(format, args...))
}

// validateResponsesRequestForGemini rejects request fields whose semantics Gemini cannot provide.
// Fields that only tune OpenAI-side caching or abuse detection (user, safety_identifier,
// prompt_cache_key, prompt_cache_retention, stream_options) do not change the generated output
// and are accepted without being forwarded.
func validateResponsesRequestForGemini(req *openai.ResponseRequest) error {
	switch {
	case req.PreviousResponseID != "":
		return errResponsesUnsupported("previous_response_id (Vertex AI does not store responses; send the full conversation in input)")
	case req.Conversation.OfString != nil || req.Conversation.OfConversationObject != nil:
		return errResponsesUnsupported("conversation (Vertex AI does not store conversations; send the full conversation in input)")
	case req.Store != nil && *req.Store:
		return errResponsesUnsupported("store=true (Vertex AI does not store responses; set store to false)")
	case req.Background != nil && *req.Background:
		return errResponsesUnsupported("background=true")
	case req.Prompt.ID != "":
		return errResponsesUnsupported("prompt templates")
	case len(req.ContextManagement) > 0:
		return errResponsesUnsupported("context_management")
	case req.Truncation != "" && req.Truncation != "disabled":
		return errResponsesUnsupported("truncation=%q", req.Truncation)
	case req.ServiceTier != "" && req.ServiceTier != "auto" && req.ServiceTier != "default":
		return errResponsesUnsupported("service_tier=%q", req.ServiceTier)
	case req.TopLogprobs != nil && *req.TopLogprobs > 0:
		return errResponsesUnsupported("top_logprobs")
	case req.Text.Verbosity != "" && req.Text.Verbosity != "medium":
		return errResponsesUnsupported("text.verbosity=%q", req.Text.Verbosity)
	}
	for _, inc := range req.Include {
		if inc != responsesIncludeReasoningEncryptedContent {
			return errResponsesUnsupported("include value %q", inc)
		}
	}
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls && len(req.Tools) > 0 {
		return errResponsesUnsupported("parallel_tool_calls=false (Gemini may return several function calls in one turn)")
	}
	return nil
}

// geminiContentsBuilder accumulates Gemini contents from Responses input items.
//
// Consecutive items of the same turn are merged into one Content: Gemini expects all parallel
// function calls of a model turn in one Content and all their responses in the next one. Function
// responses are kept in a different Content from user-authored parts.
type geminiContentsBuilder struct {
	contents          []genai.Content
	systemInstruction *genai.Content
	role              string
	functionResponses bool
	parts             []*genai.Part
	// signatures are the thought signatures carried by reasoning items in the current model turn.
	signatures [][]byte
	// callNames maps function call IDs to function names, since Gemini function responses are matched by name.
	callNames map[string]string
}

func (b *geminiContentsBuilder) addSystem(parts ...*genai.Part) {
	if len(parts) == 0 {
		return
	}
	if b.systemInstruction == nil {
		b.systemInstruction = &genai.Content{}
	}
	b.systemInstruction.Parts = append(b.systemInstruction.Parts, parts...)
}

// startTurn flushes the current turn when the next parts belong to a different one.
func (b *geminiContentsBuilder) startTurn(role string, functionResponses bool) {
	if b.role != role || b.functionResponses != functionResponses {
		b.flush()
		b.role, b.functionResponses = role, functionResponses
	}
}

func (b *geminiContentsBuilder) add(role string, functionResponses bool, parts ...*genai.Part) {
	b.startTurn(role, functionResponses)
	b.parts = append(b.parts, parts...)
}

func (b *geminiContentsBuilder) addSignature(signature []byte) {
	b.startTurn(genai.RoleModel, false)
	b.signatures = append(b.signatures, signature)
}

// flush appends the current turn to contents. For model turns, it attaches the first thought
// signature where Gemini returned it: the first function call part, or the last part otherwise.
// https://ai.google.dev/gemini-api/docs/thought-signatures
func (b *geminiContentsBuilder) flush() {
	if len(b.parts) > 0 {
		if b.role == genai.RoleModel {
			b.attachSignature()
		}
		b.contents = append(b.contents, genai.Content{Role: b.role, Parts: b.parts})
	}
	b.parts, b.signatures = nil, nil
}

func (b *geminiContentsBuilder) attachSignature() {
	var signature []byte
	if len(b.signatures) > 0 {
		signature = b.signatures[0]
	}
	for _, p := range b.parts {
		if p.FunctionCall != nil {
			p.ThoughtSignature = signature
			if signature == nil {
				// Gemini 3 rejects function calls without a signature in multi-turn requests. Fall back to
				// Google's documented escape when the client did not send a reasoning item back.
				p.ThoughtSignature = dummyThoughtSignature
			}
			return
		}
	}
	if signature != nil {
		b.parts[len(b.parts)-1].ThoughtSignature = signature
	}
}

// responsesInputToGeminiContents converts instructions and input items into Gemini contents and a system instruction.
func responsesInputToGeminiContents(req *openai.ResponseRequest, model internalapi.RequestModel) ([]genai.Content, *genai.Content, error) {
	b := &geminiContentsBuilder{callNames: map[string]string{}}
	if req.Instructions != "" {
		b.addSystem(genai.NewPartFromText(req.Instructions))
	}
	switch {
	case req.Input.OfString != nil:
		if *req.Input.OfString != "" {
			b.add(genai.RoleUser, false, genai.NewPartFromText(*req.Input.OfString))
		}
	case req.Input.OfInputItemList != nil:
		for i := range req.Input.OfInputItemList {
			if err := b.addInputItem(&req.Input.OfInputItemList[i], model); err != nil {
				return nil, nil, fmt.Errorf("invalid input item at index %d: %w", i, err)
			}
		}
	}
	b.flush()
	return b.contents, b.systemInstruction, nil
}

func (b *geminiContentsBuilder) addInputItem(item *openai.ResponseInputItemUnionParam, model internalapi.RequestModel) error {
	switch {
	case item.OfMessage != nil:
		return b.addMessage(item.OfMessage.Role, item.OfMessage.Content.OfString, item.OfMessage.Content.OfInputItemContentList, model)
	case item.OfInputMessage != nil:
		return b.addMessage(item.OfInputMessage.Role, nil, item.OfInputMessage.Content, model)
	case item.OfOutputMessage != nil:
		parts, err := outputMessageToGeminiParts(item.OfOutputMessage)
		if err != nil {
			return err
		}
		b.add(genai.RoleModel, false, parts...)
	case item.OfFunctionCall != nil:
		fc := item.OfFunctionCall
		if fc.Namespace != "" {
			return errResponsesUnsupported("function_call with a namespace")
		}
		var args map[string]any
		if strings.TrimSpace(fc.Arguments) != "" {
			if err := json.Unmarshal([]byte(fc.Arguments), &args); err != nil {
				return fmt.Errorf("%w: function_call %q arguments must be a JSON object", internalapi.ErrInvalidRequestBody, fc.CallID)
			}
		}
		b.callNames[fc.CallID] = fc.Name
		b.add(genai.RoleModel, false, genai.NewPartFromFunctionCall(fc.Name, args))
	case item.OfFunctionCallOutput != nil:
		part, err := b.functionCallOutputToGeminiPart(item.OfFunctionCallOutput)
		if err != nil {
			return err
		}
		b.add(genai.RoleUser, true, part)
	case item.OfReasoning != nil:
		// Only the thought signature matters to Gemini; previous thoughts need not be sent back.
		if item.OfReasoning.EncryptedContent != "" {
			signature, err := base64.StdEncoding.DecodeString(item.OfReasoning.EncryptedContent)
			if err != nil {
				return fmt.Errorf("%w: reasoning encrypted_content must be a Gemini thought signature returned by this gateway", internalapi.ErrInvalidRequestBody)
			}
			b.addSignature(signature)
		}
	default:
		raw, _ := json.Marshal(item)
		return errResponsesUnsupported("input item type %q", gjson.GetBytes(raw, "type").String())
	}
	return nil
}

func (b *geminiContentsBuilder) addMessage(role string, text *string, content []openai.ResponseInputContentUnionParam, model internalapi.RequestModel) error {
	var parts []*genai.Part
	if text != nil {
		if *text != "" {
			parts = append(parts, genai.NewPartFromText(*text))
		}
	} else {
		for i := range content {
			part, err := inputContentToGeminiPart(&content[i], model)
			if err != nil {
				return err
			}
			if part != nil {
				parts = append(parts, part)
			}
		}
	}
	switch role {
	case "system", "developer":
		for _, p := range parts {
			if p.Text == "" {
				return fmt.Errorf("%w: %s messages may only contain text", internalapi.ErrInvalidRequestBody, role)
			}
		}
		b.addSystem(parts...)
	case "user":
		if len(parts) > 0 {
			b.add(genai.RoleUser, false, parts...)
		}
	case "assistant":
		if len(parts) > 0 {
			b.add(genai.RoleModel, false, parts...)
		}
	default:
		return fmt.Errorf("%w: unsupported message role %q", internalapi.ErrInvalidRequestBody, role)
	}
	return nil
}

// inputContentToGeminiPart converts one input content item. It returns nil for empty text.
func inputContentToGeminiPart(c *openai.ResponseInputContentUnionParam, model internalapi.RequestModel) (*genai.Part, error) {
	switch {
	case c.OfInputText != nil:
		if c.OfInputText.Text == "" {
			return nil, nil
		}
		return genai.NewPartFromText(c.OfInputText.Text), nil
	case c.OfInputImage != nil:
		return inputImageToGeminiPart(c.OfInputImage, model)
	case c.OfInputFile != nil:
		return inputFileToGeminiPart(c.OfInputFile)
	default:
		return nil, fmt.Errorf("%w: unsupported input content", internalapi.ErrInvalidRequestBody)
	}
}

// inputImageToGeminiPart reuses the Chat Completions image conversion, which handles data URLs,
// remote URLs and the detail-to-media-resolution mapping.
func inputImageToGeminiPart(img *openai.ResponseInputImageParam, model internalapi.RequestModel) (*genai.Part, error) {
	if img.FileID != "" {
		return nil, errResponsesUnsupported("input_image file_id (OpenAI Files)")
	}
	if img.ImageURL == "" {
		return nil, fmt.Errorf("%w: input_image requires image_url", internalapi.ErrInvalidRequestBody)
	}
	parts, err := userMsgToGeminiParts(openai.ChatCompletionUserMessageParam{
		Role: openai.ChatMessageRoleUser,
		Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{{
			OfImageURL: &openai.ChatCompletionContentPartImageParam{
				Type: openai.ChatCompletionContentPartImageTypeImageURL,
				ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
					URL:    img.ImageURL,
					Detail: openai.ChatCompletionContentPartImageImageURLDetail(img.Detail),
				},
			},
		}}},
	}, model)
	if err != nil {
		return nil, err
	}
	return parts[0], nil
}

// inputFileToGeminiPart converts an input_file with inline data or a URL into a Gemini part.
func inputFileToGeminiPart(f *openai.ResponseInputFileParam) (*genai.Part, error) {
	switch {
	case f.FileID != "":
		return nil, errResponsesUnsupported("input_file file_id (OpenAI Files)")
	case f.FileData != "":
		if strings.HasPrefix(f.FileData, "data:") {
			mimeType, data, err := parseDataURI(f.FileData)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid input_file file_data data URL", internalapi.ErrInvalidRequestBody)
			}
			return genai.NewPartFromBytes(data, mimeType), nil
		}
		mimeType := mime.TypeByExtension(path.Ext(f.Filename))
		if mimeType == "" {
			return nil, fmt.Errorf("%w: input_file file_data must be a data URL or have a filename with a known extension", internalapi.ErrInvalidRequestBody)
		}
		data, err := base64.StdEncoding.DecodeString(f.FileData)
		if err != nil {
			return nil, fmt.Errorf("%w: input_file file_data must be base64 encoded", internalapi.ErrInvalidRequestBody)
		}
		return genai.NewPartFromBytes(data, mimeType), nil
	case f.FileURL != "":
		u, err := url.Parse(f.FileURL)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid input_file file_url", internalapi.ErrInvalidRequestBody)
		}
		mimeType := mime.TypeByExtension(path.Ext(cmp.Or(f.Filename, u.Path)))
		if mimeType == "" {
			return nil, fmt.Errorf("%w: cannot determine the MIME type of input_file %q; add a filename with a known extension", internalapi.ErrInvalidRequestBody, f.FileURL)
		}
		return genai.NewPartFromURI(f.FileURL, mimeType), nil
	default:
		return nil, fmt.Errorf("%w: input_file requires file_data or file_url", internalapi.ErrInvalidRequestBody)
	}
}

// outputMessageToGeminiParts converts a previous assistant message into model parts.
func outputMessageToGeminiParts(m *openai.ResponseOutputMessage) ([]*genai.Part, error) {
	if m.Content.OfString != nil {
		if *m.Content.OfString == "" {
			return nil, nil
		}
		return []*genai.Part{genai.NewPartFromText(*m.Content.OfString)}, nil
	}
	var parts []*genai.Part
	for _, c := range m.Content.OfContentArray {
		var text string
		switch {
		case c.OfOutputText != nil:
			text = c.OfOutputText.Text
		case c.OfRefusal != nil:
			text = c.OfRefusal.Refusal
		default:
			return nil, fmt.Errorf("%w: unsupported assistant message content", internalapi.ErrInvalidRequestBody)
		}
		if text != "" {
			parts = append(parts, genai.NewPartFromText(text))
		}
	}
	return parts, nil
}

// functionCallOutputToGeminiPart converts a function_call_output item into a Gemini function response,
// using the same {"output": ...} shape as the Chat Completions translation.
func (b *geminiContentsBuilder) functionCallOutputToGeminiPart(fco *openai.ResponseInputItemFunctionCallOutputParam) (*genai.Part, error) {
	name, ok := b.callNames[fco.CallID]
	if !ok {
		return nil, fmt.Errorf("%w: function_call_output call_id %q does not match a preceding function_call item", internalapi.ErrInvalidRequestBody, fco.CallID)
	}
	var output strings.Builder
	if fco.Output.OfString != nil {
		output.WriteString(*fco.Output.OfString)
	}
	for _, item := range fco.Output.OfResponseFunctionCallOutputItemArray {
		if item.OfInputText == nil {
			return nil, errResponsesUnsupported("non-text function_call_output content")
		}
		output.WriteString(item.OfInputText.Text)
	}
	return genai.NewPartFromFunctionResponse(name, map[string]any{"output": output.String()}), nil
}

// responsesToolsToGemini converts function tools and tool_choice. OpenAI built-in tools are rejected.
func responsesToolsToGemini(tools []openai.ResponseToolUnion, choice openai.ResponseToolChoiceUnion, model internalapi.RequestModel) ([]genai.Tool, *genai.ToolConfig, error) {
	functions := make([]*openai.FunctionToolParam, 0, len(tools))
	for i := range tools {
		if tools[i].OfFunction == nil {
			raw, _ := json.Marshal(tools[i])
			return nil, nil, errResponsesUnsupported("tool type %q", gjson.GetBytes(raw, "type").String())
		}
		functions = append(functions, tools[i].OfFunction)
	}

	var toolConfig *genai.ToolConfig
	var err error
	switch {
	case choice.OfToolChoiceMode != nil:
		toolConfig, err = openAIToolChoiceToGeminiToolConfig(&openai.ChatCompletionToolChoiceUnion{Value: *choice.OfToolChoiceMode})
	case choice.OfFunctionTool != nil:
		toolConfig, err = openAIToolChoiceToGeminiToolConfig(&openai.ChatCompletionToolChoiceUnion{Value: openai.ChatCompletionNamedToolChoice{
			Type:     openai.ToolTypeFunction,
			Function: openai.ChatCompletionNamedToolChoiceFunction{Name: choice.OfFunctionTool.Name},
		}})
	case choice.OfAllowedTools != nil:
		functions, toolConfig, err = restrictToAllowedTools(functions, choice.OfAllowedTools)
	case choice.OfHostedTool != nil:
		return nil, nil, errResponsesUnsupported("tool_choice type %q", choice.OfHostedTool.Type)
	case choice.OfMcpTool != nil, choice.OfCustomTool != nil, choice.OfApplyPatchTool != nil, choice.OfShellTool != nil:
		raw, _ := json.Marshal(choice)
		return nil, nil, errResponsesUnsupported("tool_choice type %q", gjson.GetBytes(raw, "type").String())
	}
	if err != nil {
		return nil, nil, err
	}

	chatTools := make([]openai.Tool, 0, len(functions))
	for _, f := range functions {
		def := &openai.FunctionDefinition{Name: f.Name, Description: f.Description}
		if len(f.Parameters) > 0 {
			def.Parameters = f.Parameters
		}
		chatTools = append(chatTools, openai.Tool{Type: openai.ToolTypeFunction, Function: def})
	}
	geminiTools, err := openAIToolsToGeminiTools(chatTools, responseJSONSchemaAvailable(model))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid tools: %w", err)
	}
	return geminiTools, toolConfig, nil
}

// restrictToAllowedTools implements the allowed_tools tool choice by declaring only the allowed functions.
func restrictToAllowedTools(functions []*openai.FunctionToolParam, allowed *openai.ToolChoiceAllowed) ([]*openai.FunctionToolParam, *genai.ToolConfig, error) {
	var mode genai.FunctionCallingConfigMode
	switch allowed.Mode {
	case "auto":
		mode = genai.FunctionCallingConfigModeAuto
	case "required":
		mode = genai.FunctionCallingConfigModeAny
	default:
		return nil, nil, fmt.Errorf("%w: unsupported allowed_tools mode %q", internalapi.ErrInvalidRequestBody, allowed.Mode)
	}
	var restricted []*openai.FunctionToolParam
	for _, t := range allowed.Tools {
		if typ, _ := t["type"].(string); typ != "function" {
			return nil, nil, errResponsesUnsupported("allowed_tools entry of type %q", typ)
		}
		name, _ := t["name"].(string)
		i := slices.IndexFunc(functions, func(f *openai.FunctionToolParam) bool { return f.Name == name })
		if i < 0 {
			return nil, nil, fmt.Errorf("%w: allowed_tools references unknown function %q", internalapi.ErrInvalidRequestBody, name)
		}
		restricted = append(restricted, functions[i])
	}
	return restricted, &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: mode}}, nil
}

// responsesGenerationConfig maps sampling, output length, text format and reasoning settings.
func responsesGenerationConfig(req *openai.ResponseRequest, model internalapi.RequestModel) (*genai.GenerationConfig, error) {
	gc := &genai.GenerationConfig{
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
	}
	if req.Temperature != nil {
		gc.Temperature = ptr.To(float32(*req.Temperature))
	}
	if req.TopP != nil {
		gc.TopP = ptr.To(float32(*req.TopP))
	}
	if req.MaxOutputTokens != nil {
		gc.MaxOutputTokens = int32(*req.MaxOutputTokens) // nolint:gosec
	}

	switch format := req.Text.Format; {
	case format.OfJSONObject != nil:
		gc.ResponseMIMEType = mimeTypeApplicationJSON
	case format.OfJSONSchema != nil:
		schema := format.OfJSONSchema.Schema
		if schema == nil {
			return nil, fmt.Errorf("%w: text.format json_schema requires a schema", internalapi.ErrInvalidRequestBody)
		}
		gc.ResponseMIMEType = mimeTypeApplicationJSON
		if responseJSONSchemaAvailable(model) {
			gc.ResponseJsonSchema = schema
		} else {
			converted, err := jsonSchemaToGemini(schema)
			if err != nil {
				return nil, fmt.Errorf("invalid text.format JSON schema: %w", err)
			}
			gc.ResponseSchema = converted
		}
	}

	thinkingConfig, err := responsesReasoningToThinkingConfig(req.Reasoning, model)
	if err != nil {
		return nil, err
	}
	gc.ThinkingConfig = thinkingConfig
	return gc, nil
}

// geminiThinkingBudgets maps reasoning effort to Gemini 2.5 thinking budgets, following Google's
// OpenAI compatibility table: https://ai.google.dev/gemini-api/docs/openai#thinking
var geminiThinkingBudgets = map[string]int32{
	"none":    0,
	"minimal": 1024,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
}

// responsesReasoningToThinkingConfig maps reasoning.effort to a thinking level (Gemini 3) or budget
// (earlier models), following https://ai.google.dev/gemini-api/docs/openai#thinking. A requested
// reasoning summary turns on includeThoughts so thoughts can be returned as reasoning summaries.
func responsesReasoningToThinkingConfig(r openai.ReasoningParam, model internalapi.RequestModel) (*genai.ThinkingConfig, error) {
	summary := cmp.Or(r.Summary, r.GenerateSummary)
	if r.Effort == "" && summary == "" {
		return nil, nil
	}
	tc := &genai.ThinkingConfig{IncludeThoughts: summary != ""}
	if r.Effort == "" {
		return tc, nil
	}
	if reasoningEffortAvailable(model) {
		switch r.Effort {
		case "minimal":
			tc.ThinkingLevel = genai.ThinkingLevelLow
			if isGeminiFlashModel(model) {
				tc.ThinkingLevel = genai.ThinkingLevelMinimal
			}
		case "low":
			tc.ThinkingLevel = genai.ThinkingLevelLow
		case "medium":
			tc.ThinkingLevel = genai.ThinkingLevelMedium
		case "high":
			tc.ThinkingLevel = genai.ThinkingLevelHigh
		case "none":
			return nil, errResponsesUnsupported("reasoning.effort=\"none\" on Gemini 3 models (thinking cannot be turned off)")
		default:
			return nil, errResponsesUnsupported("reasoning.effort=%q", r.Effort)
		}
		return tc, nil
	}
	budget, ok := geminiThinkingBudgets[r.Effort]
	if !ok {
		return nil, errResponsesUnsupported("reasoning.effort=%q", r.Effort)
	}
	tc.ThinkingBudget = &budget
	return tc, nil
}

// ---------------------------------------------------------------------------------
// Response translation: Gemini GenerateContentResponse -> OpenAI Responses
// ---------------------------------------------------------------------------------

// responsesOutputKind is the kind of output item currently being streamed.
type responsesOutputKind int

const (
	responsesOutputNone responsesOutputKind = iota
	responsesOutputReasoning
	responsesOutputMessage
)

// responsesOutputBuilder turns Gemini response chunks into Responses output items and, when
// streaming, into the Responses event sequence. Non-streaming responses go through the same
// builder with event emission disabled, so both paths produce the same output items.
//
// Gemini thought parts become a reasoning item with a summary. Gemini thought signatures become a
// reasoning item's encrypted_content so that clients can send them back on the next turn.
type responsesOutputBuilder struct {
	req          *openai.ResponseRequest
	requestModel internalapi.RequestModel
	emitEvents   bool

	started      bool
	finished     bool
	id           string
	createdAt    time.Time
	modelVersion string
	usage        *genai.GenerateContentResponseUsageMetadata
	finishReason genai.FinishReason
	blockReason  genai.BlockedReason

	output   []openai.ResponseOutputItemUnion
	open     responsesOutputKind
	openID   string
	openText strings.Builder
	// openSignature is the thought signature for the open reasoning item.
	openSignature []byte
	// pendingSignature is a signature from a message text part. It is emitted as a
	// reasoning item once the message is closed.
	pendingSignature []byte

	seq    int64
	buf    []byte
	events []openai.ResponseStreamEventUnion
	// err is the first event serialization error.
	err error
}

func newResponsesOutputBuilder(req *openai.ResponseRequest, requestModel internalapi.RequestModel, emitEvents bool) *responsesOutputBuilder {
	return &responsesOutputBuilder{req: req, requestModel: requestModel, emitEvents: emitEvents}
}

func (b *responsesOutputBuilder) model() string {
	return cmp.Or(b.modelVersion, b.requestModel)
}

// addChunk processes one Gemini response (a whole non-streaming response or one streaming chunk).
func (b *responsesOutputBuilder) addChunk(chunk *genai.GenerateContentResponse) {
	if !b.started {
		b.start(chunk)
	}
	if chunk.ModelVersion != "" {
		b.modelVersion = chunk.ModelVersion
	}
	if chunk.UsageMetadata != nil {
		b.usage = chunk.UsageMetadata
	}
	if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" {
		b.blockReason = chunk.PromptFeedback.BlockReason
	}
	// The Responses API returns a single candidate; the request never asks Gemini for more.
	if len(chunk.Candidates) == 0 || chunk.Candidates[0] == nil {
		return
	}
	candidate := chunk.Candidates[0]
	if candidate.FinishReason != "" {
		b.finishReason = candidate.FinishReason
	}
	if candidate.Content == nil {
		return
	}
	for _, part := range candidate.Content.Parts {
		if part != nil {
			b.addPart(part)
		}
	}
}

// start emits response.created and response.in_progress.
func (b *responsesOutputBuilder) start(chunk *genai.GenerateContentResponse) {
	b.started = true
	b.id = "resp_" + uuid.NewString()
	if chunk != nil && chunk.ResponseID != "" {
		b.id = "resp_" + chunk.ResponseID
	}
	b.createdAt = time.Now()
	if chunk != nil && !chunk.CreateTime.IsZero() {
		b.createdAt = chunk.CreateTime
	}
	if chunk != nil && chunk.ModelVersion != "" {
		b.modelVersion = chunk.ModelVersion
	}
	if !b.emitEvents {
		return
	}
	resp := b.response("in_progress")
	b.emit(&openai.ResponseStreamEventUnion{OfResponseCreated: &openai.ResponseCreatedEvent{Type: "response.created", Response: *resp}})
	b.emit(&openai.ResponseStreamEventUnion{OfResponseInProgress: &openai.ResponseInProgressEvent{Type: "response.in_progress", Response: *resp}})
}

func (b *responsesOutputBuilder) addPart(part *genai.Part) {
	switch {
	case part.Thought:
		if part.Text == "" && part.ThoughtSignature == nil {
			return
		}
		if b.open != responsesOutputReasoning {
			b.closeOpen()
			b.openReasoning()
		}
		b.reasoningDelta(part.Text)
		if b.openSignature == nil {
			b.openSignature = part.ThoughtSignature
		}
	case part.FunctionCall != nil:
		if part.ThoughtSignature != nil && b.open == responsesOutputReasoning && b.openSignature == nil {
			b.openSignature = part.ThoughtSignature
		} else if part.ThoughtSignature != nil {
			b.closeOpen()
			b.openReasoning()
			b.openSignature = part.ThoughtSignature
		}
		b.closeOpen()
		b.functionCall(part.FunctionCall)
	case part.Text != "":
		if b.open != responsesOutputMessage {
			b.closeOpen()
			b.openMessage()
		}
		b.textDelta(part.Text)
		if b.pendingSignature == nil {
			b.pendingSignature = part.ThoughtSignature
		}
	case part.ThoughtSignature != nil:
		// An empty part that only carries the signature, typically the last streaming chunk.
		switch {
		case b.open == responsesOutputMessage && b.pendingSignature == nil:
			b.pendingSignature = part.ThoughtSignature
		case b.open == responsesOutputReasoning && b.openSignature == nil:
			b.openSignature = part.ThoughtSignature
		default:
			b.closeOpen()
			b.openReasoning()
			b.openSignature = part.ThoughtSignature
			b.closeOpen()
		}
	}
	// Other part kinds (inline data, code execution) are only produced for features this
	// translation never enables, so there is nothing to map them to.
}

func (b *responsesOutputBuilder) openReasoning() {
	b.open, b.openID = responsesOutputReasoning, "rs_"+uuid.NewString()
	b.openText.Reset()
	b.openSignature = nil
	b.emit(&openai.ResponseStreamEventUnion{OfResponseOutputItemAdded: &openai.ResponseOutputItemAddedEvent{
		Type: "response.output_item.added", OutputIndex: b.outputIndex(),
		Item: openai.ResponseOutputItemUnion{OfReasoning: &openai.ResponseReasoningItem{
			ID: b.openID, Type: "reasoning", Summary: []openai.ResponseReasoningItemSummaryParam{},
		}},
	}})
}

func (b *responsesOutputBuilder) reasoningDelta(text string) {
	if text == "" {
		return
	}
	if b.openText.Len() == 0 {
		b.emit(&openai.ResponseStreamEventUnion{OfResponseReasoningSummaryPartAdded: &openai.ResponseReasoningSummaryPartAddedEvent{
			Type: "response.reasoning_summary_part.added", ItemID: b.openID, OutputIndex: b.outputIndex(),
			Part: openai.ResponseReasoningSummaryPartAddedEventPart{Type: "summary_text"},
		}})
	}
	b.openText.WriteString(text)
	b.emit(&openai.ResponseStreamEventUnion{OfResponseReasoningSummaryTextDelta: &openai.ResponseReasoningSummaryTextDeltaEvent{
		Type: "response.reasoning_summary_text.delta", ItemID: b.openID, OutputIndex: b.outputIndex(), Delta: text,
	}})
}

func (b *responsesOutputBuilder) openMessage() {
	b.open, b.openID = responsesOutputMessage, "msg_"+uuid.NewString()
	b.openText.Reset()
	b.emit(&openai.ResponseStreamEventUnion{OfResponseOutputItemAdded: &openai.ResponseOutputItemAddedEvent{
		Type: "response.output_item.added", OutputIndex: b.outputIndex(),
		Item: openai.ResponseOutputItemUnion{OfOutputMessage: &openai.ResponseOutputMessage{
			ID: b.openID, Type: "message", Role: "assistant", Status: "in_progress",
			Content: openai.ResponseOutputMessageContentUnion{OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{}},
		}},
	}})
	b.emit(&openai.ResponseStreamEventUnion{OfResponseContentPartAdded: &openai.ResponseContentPartAddedEvent{
		Type: "response.content_part.added", ItemID: b.openID, OutputIndex: b.outputIndex(),
		Part: openai.ResponseContentPartAddedEventPartUnion{OfResponseOutputText: newOutputText("")},
	}})
}

func (b *responsesOutputBuilder) textDelta(text string) {
	b.openText.WriteString(text)
	b.emit(&openai.ResponseStreamEventUnion{OfResponseTextDelta: &openai.ResponseTextDeltaEvent{
		Type: "response.output_text.delta", ItemID: b.openID, OutputIndex: b.outputIndex(), Delta: text,
	}})
}

func newOutputText(text string) *openai.ResponseOutputTextParam {
	return &openai.ResponseOutputTextParam{Type: "output_text", Text: text, Annotations: []openai.ResponseOutputTextAnnotationUnionParam{}}
}

// closeOpen finishes the open reasoning or message item and appends it to the output.
func (b *responsesOutputBuilder) closeOpen() {
	idx, text := b.outputIndex(), b.openText.String()
	switch b.open {
	case responsesOutputNone:
		return
	case responsesOutputReasoning:
		item := &openai.ResponseReasoningItem{ID: b.openID, Type: "reasoning", Summary: []openai.ResponseReasoningItemSummaryParam{}}
		if text != "" {
			b.emit(&openai.ResponseStreamEventUnion{OfResponseReasoningSummaryTextDone: &openai.ResponseReasoningSummaryTextDoneEvent{
				Type: "response.reasoning_summary_text.done", ItemID: b.openID, OutputIndex: idx, Text: text,
			}})
			b.emit(&openai.ResponseStreamEventUnion{OfResponseReasoningSummaryPartDone: &openai.ResponseReasoningSummaryPartDoneEvent{
				Type: "response.reasoning_summary_part.done", ItemID: b.openID, OutputIndex: idx,
				Part: openai.ResponseReasoningSummaryPartDoneEventPart{Type: "summary_text", Text: text},
			}})
			item.Summary = []openai.ResponseReasoningItemSummaryParam{{Type: "summary_text", Text: text}}
		}
		if b.openSignature != nil {
			item.EncryptedContent = base64.StdEncoding.EncodeToString(b.openSignature)
		}
		b.closeItem(&openai.ResponseOutputItemUnion{OfReasoning: item})
	case responsesOutputMessage:
		b.emit(&openai.ResponseStreamEventUnion{OfResponseTextDone: &openai.ResponseTextDoneEvent{
			Type: "response.output_text.done", ItemID: b.openID, OutputIndex: idx, Text: text,
		}})
		b.emit(&openai.ResponseStreamEventUnion{OfResponseContentPartDone: &openai.ResponseContentPartDoneEvent{
			Type: "response.content_part.done", ItemID: b.openID, OutputIndex: idx,
			Part: openai.ResponseContentPartDoneEventPartUnion{OfResponseOutputText: newOutputText(text)},
		}})
		b.closeItem(&openai.ResponseOutputItemUnion{OfOutputMessage: &openai.ResponseOutputMessage{
			ID: b.openID, Type: "message", Role: "assistant", Status: "completed",
			Content: openai.ResponseOutputMessageContentUnion{OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
				{OfOutputText: newOutputText(text)},
			}},
		}})
		if signature := b.pendingSignature; signature != nil {
			b.pendingSignature = nil
			b.openReasoning()
			b.openSignature = signature
			b.closeOpen()
		}
	}
}

// closeItem emits response.output_item.done and appends the item to the output.
func (b *responsesOutputBuilder) closeItem(item *openai.ResponseOutputItemUnion) {
	b.emit(&openai.ResponseStreamEventUnion{OfResponseOutputItemDone: &openai.ResponseOutputItemDoneEvent{
		Type: "response.output_item.done", OutputIndex: b.outputIndex(), Item: *item,
	}})
	b.output = append(b.output, *item)
	b.open, b.openID = responsesOutputNone, ""
	b.openText.Reset()
}

// functionCall emits a complete function_call item. Gemini returns whole function calls, so the
// arguments are sent as a single delta.
func (b *responsesOutputBuilder) functionCall(fc *genai.FunctionCall) {
	args := "{}"
	if len(fc.Args) > 0 {
		if raw, err := json.Marshal(fc.Args); err == nil {
			args = string(raw)
		}
	}
	item := &openai.ResponseFunctionToolCall{
		Type: "function_call", ID: "fc_" + uuid.NewString(), CallID: cmp.Or(fc.ID, "call_"+uuid.NewString()),
		Name: fc.Name, Status: "in_progress",
	}
	idx := b.outputIndex()
	added := *item
	b.emit(&openai.ResponseStreamEventUnion{OfResponseOutputItemAdded: &openai.ResponseOutputItemAddedEvent{
		Type: "response.output_item.added", OutputIndex: idx, Item: openai.ResponseOutputItemUnion{OfFunctionCall: &added},
	}})
	b.emit(&openai.ResponseStreamEventUnion{OfResponseFunctionCallArgumentsDelta: &openai.ResponseFunctionCallArgumentsDeltaEvent{
		Type: "response.function_call_arguments.delta", ItemID: item.ID, OutputIndex: idx, Delta: args,
	}})
	b.emit(&openai.ResponseStreamEventUnion{OfResponseFunctionCallArgumentsDone: &openai.ResponseFunctionCallArgumentsDoneEvent{
		Type: "response.function_call_arguments.done", ItemID: item.ID, OutputIndex: idx, Name: fc.Name, Arguments: args,
	}})
	item.Arguments, item.Status = args, "completed"
	b.closeItem(&openai.ResponseOutputItemUnion{OfFunctionCall: item})
}

// finish closes open items and emits the terminal event. It returns the final response.
func (b *responsesOutputBuilder) finish() *openai.Response {
	if !b.started {
		b.start(nil)
	}
	b.closeOpen()
	status, incomplete, respErr := b.status()
	resp := b.response(status)
	resp.IncompleteDetails, resp.Error = incomplete, respErr
	resp.Usage = responsesUsageFromGemini(b.usage)
	if !b.finished {
		b.finished = true
		switch status {
		case "completed":
			b.emit(&openai.ResponseStreamEventUnion{OfResponseCompleted: &openai.ResponseCompletedEvent{Type: "response.completed", Response: *resp}})
		case "incomplete":
			b.emit(&openai.ResponseStreamEventUnion{OfResponseIncomplete: &openai.ResponseIncompleteEvent{Type: "response.incomplete", Response: *resp}})
		default:
			b.emit(&openai.ResponseStreamEventUnion{OfResponseFailed: &openai.ResponseFailedEvent{Type: "response.failed", Response: *resp}})
		}
	}
	return resp
}

// status maps the Gemini finish or prompt block reason to the Responses status.
func (b *responsesOutputBuilder) status() (string, openai.ResponseIncompleteDetails, openai.ResponseError) {
	if b.blockReason != "" {
		return "incomplete", openai.ResponseIncompleteDetails{Reason: "content_filter"}, openai.ResponseError{}
	}
	switch b.finishReason {
	case "", genai.FinishReasonStop:
		return "completed", openai.ResponseIncompleteDetails{}, openai.ResponseError{}
	case genai.FinishReasonMaxTokens:
		return "incomplete", openai.ResponseIncompleteDetails{Reason: "max_output_tokens"}, openai.ResponseError{}
	case genai.FinishReasonSafety, genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent, genai.FinishReasonSPII,
		genai.FinishReasonRecitation, genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent, genai.FinishReasonImageRecitation:
		return "incomplete", openai.ResponseIncompleteDetails{Reason: "content_filter"}, openai.ResponseError{}
	default:
		return "failed", openai.ResponseIncompleteDetails{}, openai.ResponseError{
			Code: "server_error", Message: fmt.Sprintf("Gemini finished with reason %s", b.finishReason),
		}
	}
}

// response builds the Response object, echoing the request configuration as OpenAI does.
func (b *responsesOutputBuilder) response(status string) *openai.Response {
	req := b.req
	resp := &openai.Response{
		ID:                b.id,
		Object:            "response",
		CreatedAt:         openai.JSONUNIXTime(b.createdAt),
		Status:            status,
		Model:             b.model(),
		Output:            slices.Clone(b.output),
		MaxOutputTokens:   req.MaxOutputTokens,
		Metadata:          req.Metadata,
		ParallelToolCalls: cmp.Or(req.ParallelToolCalls, ptr.To(true)),
		Temperature:       ptr.Deref(req.Temperature, 1),
		TopP:              ptr.Deref(req.TopP, 1),
		ToolChoice:        req.ToolChoice,
		Tools:             req.Tools,
		Reasoning:         req.Reasoning,
		Text:              openai.ResponseTextConfig{Format: req.Text.Format, Verbosity: req.Text.Verbosity},
		Truncation:        "disabled",
		Store:             ptr.To(false),
		User:              req.User,
		SafetyIdentifier:  req.SafetyIdentifier,
		PromptCacheKey:    req.PromptCacheKey,
		PresencePenalty:   req.PresencePenalty,
		FrequencyPenalty:  req.FrequencyPenalty,
	}
	if resp.Output == nil {
		resp.Output = []openai.ResponseOutputItemUnion{}
	}
	if req.Instructions != "" {
		resp.Instructions = openai.ResponseInstructionsUnion{OfString: ptr.To(req.Instructions)}
	}
	if resp.ToolChoice == (openai.ResponseToolChoiceUnion{}) {
		resp.ToolChoice = openai.ResponseToolChoiceUnion{OfToolChoiceMode: ptr.To("auto")}
	}
	if resp.Text.Format == (openai.ResponseFormatTextConfigUnionParam{}) {
		resp.Text.Format = openai.ResponseFormatTextConfigUnionParam{OfText: &openai.ResponseFormatTextParam{Type: "text"}}
	}
	return resp
}

// responsesUsageFromGemini converts Gemini usage. Output tokens include thinking tokens, as
// OpenAI output tokens include reasoning tokens.
func responsesUsageFromGemini(m *genai.GenerateContentResponseUsageMetadata) *openai.ResponseUsage {
	if m == nil {
		return nil
	}
	input := int64(m.PromptTokenCount)
	output := int64(m.CandidatesTokenCount) + int64(m.ThoughtsTokenCount)
	return &openai.ResponseUsage{
		InputTokens:         input,
		InputTokensDetails:  openai.ResponseUsageInputTokensDetails{CachedTokens: int64(m.CachedContentTokenCount)},
		OutputTokens:        output,
		OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{ReasoningTokens: int64(m.ThoughtsTokenCount)},
		TotalTokens:         cmp.Or(int64(m.TotalTokenCount), input+output),
	}
}

// setTokenUsage reports the latest cumulative usage seen in the stream.
func (b *responsesOutputBuilder) setTokenUsage(tokenUsage *metrics.TokenUsage) {
	if usage := responsesUsageFromGemini(b.usage); usage != nil {
		setTokenUsageFromResponse(tokenUsage, &openai.Response{Usage: usage})
	}
}

func (b *responsesOutputBuilder) outputIndex() int64 {
	return int64(len(b.output))
}

// emit records a streaming event with the next sequence number.
func (b *responsesOutputBuilder) emit(event *openai.ResponseStreamEventUnion) {
	if !b.emitEvents {
		return
	}
	setResponsesEventSequenceNumber(event, b.seq)
	b.seq++
	data, err := json.Marshal(event)
	if err != nil {
		b.err = cmp.Or(b.err, fmt.Errorf("failed to marshal Responses event: %w", err))
		return
	}
	b.buf = append(b.buf, "event: "...)
	b.buf = append(b.buf, gjson.GetBytes(data, "type").String()...)
	b.buf = append(b.buf, "\ndata: "...)
	b.buf = append(b.buf, data...)
	b.buf = append(b.buf, '\n', '\n')
	b.events = append(b.events, *event)
}

// takeEvents returns the serialized and structured events emitted since the last call.
func (b *responsesOutputBuilder) takeEvents() ([]byte, []openai.ResponseStreamEventUnion) {
	buf, events := b.buf, b.events
	b.buf, b.events = nil, nil
	return buf, events
}

// setResponsesEventSequenceNumber sets the sequence number on the variants this builder emits.
func setResponsesEventSequenceNumber(e *openai.ResponseStreamEventUnion, seq int64) {
	switch {
	case e.OfResponseCreated != nil:
		e.OfResponseCreated.SequenceNumber = seq
	case e.OfResponseInProgress != nil:
		e.OfResponseInProgress.SequenceNumber = seq
	case e.OfResponseOutputItemAdded != nil:
		e.OfResponseOutputItemAdded.SequenceNumber = seq
	case e.OfResponseOutputItemDone != nil:
		e.OfResponseOutputItemDone.SequenceNumber = seq
	case e.OfResponseContentPartAdded != nil:
		e.OfResponseContentPartAdded.SequenceNumber = seq
	case e.OfResponseContentPartDone != nil:
		e.OfResponseContentPartDone.SequenceNumber = seq
	case e.OfResponseTextDelta != nil:
		e.OfResponseTextDelta.SequenceNumber = seq
	case e.OfResponseTextDone != nil:
		e.OfResponseTextDone.SequenceNumber = seq
	case e.OfResponseReasoningSummaryPartAdded != nil:
		e.OfResponseReasoningSummaryPartAdded.SequenceNumber = seq
	case e.OfResponseReasoningSummaryPartDone != nil:
		e.OfResponseReasoningSummaryPartDone.SequenceNumber = seq
	case e.OfResponseReasoningSummaryTextDelta != nil:
		e.OfResponseReasoningSummaryTextDelta.SequenceNumber = seq
	case e.OfResponseReasoningSummaryTextDone != nil:
		e.OfResponseReasoningSummaryTextDone.SequenceNumber = seq
	case e.OfResponseFunctionCallArgumentsDelta != nil:
		e.OfResponseFunctionCallArgumentsDelta.SequenceNumber = seq
	case e.OfResponseFunctionCallArgumentsDone != nil:
		e.OfResponseFunctionCallArgumentsDone.SequenceNumber = seq
	case e.OfResponseCompleted != nil:
		e.OfResponseCompleted.SequenceNumber = seq
	case e.OfResponseIncomplete != nil:
		e.OfResponseIncomplete.SequenceNumber = seq
	case e.OfResponseFailed != nil:
		e.OfResponseFailed.SequenceNumber = seq
	}
}
