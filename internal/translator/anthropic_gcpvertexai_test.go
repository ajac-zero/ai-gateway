// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestAnthropicToGCPVertexAI_RequestBody(t *testing.T) {
	temperature := 0.1
	topK := 40
	req := &anthropicschema.MessagesRequest{
		Model:         "claude-ignored",
		MaxTokens:     100,
		Temperature:   &temperature,
		TopK:          &topK,
		StopSequences: []string{"stop1", "stop2"},
		System:        &anthropicschema.SystemPrompt{Text: "You are a helpful assistant"},
		Thinking:      &anthropicschema.Thinking{Enabled: &anthropicschema.ThinkingEnabled{Type: "enabled", BudgetTokens: 1024}},
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "Tell me about AI Gateways"},
			},
		},
	}

	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	headers, body, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.Equal(t, internalapi.RequestModel("gemini-1.5-pro"), tr.(*anthropicToGCPVertexAITranslator).requestModel)
	require.Equal(t, []internalapi.Header{
		{pathHeaderName, "publishers/google/models/gemini-1.5-pro:generateContent"},
		{contentLengthHeaderName, stringLength(body)},
	}, headers)
	require.JSONEq(t, `{
		"contents":[{"parts":[{"text":"Tell me about AI Gateways"}],"role":"user"}],
		"tools":null,
		"generation_config":{"maxOutputTokens":100,"stopSequences":["stop1","stop2"],"temperature":0.1,"topK":40,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":1024}},
		"system_instruction":{"parts":[{"text":"You are a helpful assistant"}]}
	}`, string(body))
}

func TestAnthropicToGCPVertexAI_RequestBodyWithImage(t *testing.T) {
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-1.5-pro",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{
					{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "What is in this image?"}},
					{Image: &anthropicschema.ImageBlockParam{Type: "image", Source: anthropicschema.ImageSource{Base64: &anthropicschema.Base64ImageSource{Type: "base64", MediaType: "image/png", Data: "aW1hZ2U="}}}},
				}},
			},
		},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, body, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"contents":[{"parts":[{"text":"What is in this image?"},{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}],"role":"user"}],
		"tools":null,
		"generation_config":{"maxOutputTokens":100}
	}`, string(body))
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsUnsupportedContent(t *testing.T) {
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-1.5-pro",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{
					{Document: &anthropicschema.DocumentBlockParam{Type: "document"}},
				}},
			},
		},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, req, false)
	require.ErrorContains(t, err, "document content is not supported")
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsAdaptiveThinking(t *testing.T) {
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-1.5-pro",
		MaxTokens: 100,
		Thinking:  &anthropicschema.Thinking{Adaptive: &anthropicschema.ThinkingAdaptive{Type: "adaptive"}},
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, req, false)
	require.ErrorContains(t, err, "thinking type adaptive is not supported")
}

func TestAnthropicToGCPVertexAI_RequestBodyWithTools(t *testing.T) {
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-1.5-pro",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "What's the weather in Paris?"},
			},
		},
		Tools: []anthropicschema.ToolUnion{
			{
				Tool: &anthropicschema.Tool{
					Type:        "custom",
					Name:        "get_weather",
					Description: "Get weather",
					InputSchema: anthropicschema.ToolInputSchema{
						Type: "object",
						Properties: map[string]any{
							"location": map[string]any{"type": "string"},
						},
						Required: []string{"location"},
					},
				},
			},
		},
		ToolChoice: &anthropicschema.ToolChoice{Tool: &anthropicschema.ToolChoiceTool{Type: "tool", Name: "get_weather"}},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, body, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"contents":[{"parts":[{"text":"What's the weather in Paris?"}],"role":"user"}],
		"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}]}],
		"tool_config":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}},
		"generation_config":{"maxOutputTokens":100}
	}`, string(body))
}

func TestAnthropicToGCPVertexAI_ResponseBodyNonStreaming(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	_, _, err := tr.RequestBody(nil, &anthropicschema.MessagesRequest{
		Model:     "ignored",
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
	}, false)
	require.NoError(t, err)

	responseBody := `{
		"modelVersion":"gemini-1.5-pro-002",
		"candidates":[{"content":{"parts":[{"text":"Hello from Gemini"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":15,"totalTokenCount":25,"cachedContentTokenCount":3}
	}`
	headers, body, tokenUsage, responseModel, err := tr.ResponseBody(nil, strings.NewReader(responseBody), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gemini-1.5-pro-002", responseModel)
	require.Equal(t, []internalapi.Header{{contentLengthHeaderName, stringLength(body)}}, headers)
	require.JSONEq(t, `{
		"id":"",
		"type":"message",
		"role":"assistant",
		"content":[{"type":"text","text":"Hello from Gemini"}],
		"model":"gemini-1.5-pro-002",
		"stop_reason":"end_turn",
		"usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":3,"input_tokens":10,"output_tokens":15}
	}`, string(body))
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(13), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(15), outputTokens)
}

func TestAnthropicToGCPVertexAI_ResponseHeadersStreaming(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	_, _, err := tr.RequestBody(nil, &anthropicschema.MessagesRequest{
		Model:     "ignored",
		Stream:    true,
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
	}, false)
	require.NoError(t, err)
	headers, err := tr.ResponseHeaders(nil)
	require.NoError(t, err)
	require.Equal(t, []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}, headers)
}

func TestAnthropicToGCPVertexAI_ResponseBodyStreaming(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	_, _, err := tr.RequestBody(nil, &anthropicschema.MessagesRequest{
		Model:     "ignored",
		Stream:    true,
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
	}, false)
	require.NoError(t, err)

	streamBody := `data: {"responseId":"resp-1","candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":18,"cachedContentTokenCount":3}}

`
	_, body, tokenUsage, responseModel, err := tr.ResponseBody(nil, strings.NewReader(streamBody), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gemini-1.5-pro", responseModel)
	out := string(body)
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "event: content_block_start")
	require.Contains(t, out, `"type":"text_delta","text":"Hello"`)
	require.Contains(t, out, `"stop_reason":"end_turn"`)
	require.Contains(t, out, "event: message_stop")
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(13), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), outputTokens)
}

func TestAnthropicToGCPVertexAI_ResponseErrorUnknownJSON(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	headers, body, err := tr.ResponseError(map[string]string{statusHeaderName: "503"}, strings.NewReader(`{"unexpected":"shape"}`))
	require.NoError(t, err)
	require.Equal(t, []internalapi.Header{{contentTypeHeaderName, jsonContentType}, {contentLengthHeaderName, stringLength(body)}}, headers)
	require.JSONEq(t, `{
		"type":"error",
		"request_id":"",
		"error":{"type":"GCPVertexAIBackendError","message":"{\"unexpected\":\"shape\"}"}
	}`, string(body))
}

func TestAnthropicToGCPVertexAI_RedactAnthropicBody(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro").(*anthropicToGCPVertexAITranslator)
	require.Nil(t, tr.RedactAnthropicBody(nil))

	resp := &anthropicschema.MessagesResponse{
		ID:    "msg-1",
		Model: "gemini-1.5-pro",
		Content: []anthropicschema.MessagesContentBlock{
			{Text: &anthropicschema.TextBlock{Type: "text", Text: "sensitive output"}},
		},
	}
	redacted := tr.RedactAnthropicBody(resp)
	require.NotNil(t, redacted)
	require.Equal(t, "sensitive output", resp.Content[0].Text.Text)
	require.NotEqual(t, resp.Content[0].Text.Text, redacted.Content[0].Text.Text)
}

func stringLength(body []byte) string {
	return strconv.Itoa(len(body))
}
