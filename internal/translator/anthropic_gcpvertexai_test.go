// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
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
		"generationConfig":{"maxOutputTokens":100,"stopSequences":["stop1","stop2"],"temperature":0.1,"topK":40,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":1024}},
		"systemInstruction":{"parts":[{"text":"You are a helpful assistant"}]}
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
		"generationConfig":{"maxOutputTokens":100}
	}`, string(body))
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsInvalidImageSource(t *testing.T) {
	tests := []struct {
		name        string
		source      anthropicschema.ImageSource
		wantMessage string
	}{
		{name: "unknown source", source: anthropicschema.ImageSource{}, wantMessage: "image source type is not supported"},
		{name: "empty URL", source: anthropicschema.ImageSource{URL: &anthropicschema.URLImageSource{Type: "url"}}, wantMessage: "image URL must not be empty"},
		{name: "empty base64 data", source: anthropicschema.ImageSource{Base64: &anthropicschema.Base64ImageSource{Type: "base64", MediaType: "image/png"}}, wantMessage: "base64 image media_type and data are required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &anthropicschema.MessagesRequest{
				Model:     "gemini-1.5-pro",
				MaxTokens: 100,
				Messages: []anthropicschema.MessageParam{{
					Role: anthropicschema.MessageRoleUser,
					Content: anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{{
						Image: &anthropicschema.ImageBlockParam{Type: "image", Source: tt.source},
					}}},
				}},
			}
			tr := NewAnthropicToGCPVertexAITranslator("")
			_, _, err := tr.RequestBody(nil, req, false)
			require.ErrorContains(t, err, tt.wantMessage)
		})
	}
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

func TestAnthropicToGCPVertexAI_RequestBodyThinkingConfig(t *testing.T) {
	adaptive := &anthropicschema.Thinking{Adaptive: &anthropicschema.ThinkingAdaptive{Type: "adaptive"}}
	disabled := &anthropicschema.Thinking{Disabled: &anthropicschema.ThinkingDisabled{Type: "disabled"}}
	enabledWith := func(budget float64) *anthropicschema.Thinking {
		return &anthropicschema.Thinking{Enabled: &anthropicschema.ThinkingEnabled{Type: "enabled", BudgetTokens: budget}}
	}
	enabled := enabledWith(2048)
	effort := func(e anthropicschema.OutputConfigEffort) *anthropicschema.OutputConfig {
		return &anthropicschema.OutputConfig{Effort: e}
	}

	tests := []struct {
		name     string
		model    string
		thinking *anthropicschema.Thinking
		output   *anthropicschema.OutputConfig
		want     string // expected thinkingConfig JSON; empty means the field is absent.
		wantErr  string
	}{
		{name: "effort low", model: "gemini-4-neon", output: effort("low"), want: `{"thinkingLevel":"LOW"}`},
		{name: "effort medium", model: "gemini-4-neon", output: effort("medium"), want: `{"thinkingLevel":"MEDIUM"}`},
		{name: "effort high", model: "gemini-4-neon", output: effort("high"), want: `{"thinkingLevel":"HIGH"}`},
		{name: "effort xhigh maps to HIGH", model: "gemini-4-neon", output: effort("xhigh"), want: `{"thinkingLevel":"HIGH"}`},
		{name: "effort max maps to HIGH", model: "gemini-4-neon", output: effort("max"), want: `{"thinkingLevel":"HIGH"}`},
		{name: "effort xhigh on gemini 3 flash", model: "gemini-3-flash-preview", output: effort("xhigh"), want: `{"thinkingLevel":"HIGH"}`},
		{name: "effort medium on gemini 3 pro has no MEDIUM", model: "gemini-3-pro-preview", output: effort("medium"), want: `{"thinkingLevel":"HIGH"}`},
		{name: "empty effort", model: "gemini-4-neon", output: effort(""), want: ""},
		{name: "output_config without effort", model: "gemini-4-neon", output: &anthropicschema.OutputConfig{}, want: ""},
		{name: "adaptive with effort", model: "gemini-4-neon", thinking: adaptive, output: effort("low"), want: `{"includeThoughts":true,"thinkingLevel":"LOW"}`},
		{name: "adaptive with max", model: "gemini-4-neon", thinking: adaptive, output: effort("max"), want: `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
		{name: "adaptive without effort", model: "gemini-4-neon", thinking: adaptive, want: `{"includeThoughts":true}`},
		// Gemini 3+ rejects thinkingBudget, so budget_tokens is bucketed: <=1024 LOW, <=8192 MEDIUM, else HIGH.
		{name: "budget 1024 -> LOW", model: "gemini-4-neon", thinking: enabledWith(1024), want: `{"includeThoughts":true,"thinkingLevel":"LOW"}`},
		{name: "budget 1025 -> MEDIUM", model: "gemini-4-neon", thinking: enabledWith(1025), want: `{"includeThoughts":true,"thinkingLevel":"MEDIUM"}`},
		{name: "budget 8192 -> MEDIUM", model: "gemini-4-neon", thinking: enabledWith(8192), want: `{"includeThoughts":true,"thinkingLevel":"MEDIUM"}`},
		{name: "budget 8193 -> HIGH", model: "gemini-4-neon", thinking: enabledWith(8193), want: `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
		{name: "budget 32000 -> HIGH", model: "gemini-3.1-pro-preview", thinking: enabledWith(32000), want: `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
		{name: "budget medium on gemini 3 pro has no MEDIUM", model: "gemini-3-pro-preview", thinking: enabledWith(4096), want: `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
		{name: "enabled with budget on gemini 2.5 keeps thinkingBudget", model: "gemini-2.5-flash", thinking: enabled, want: `{"includeThoughts":true,"thinkingBudget":2048}`},
		{name: "enabled with budget and effort prefers level", model: "gemini-4-neon", thinking: enabled, output: effort("medium"), want: `{"includeThoughts":true,"thinkingLevel":"MEDIUM"}`},
		{name: "enabled with budget and effort on gemini 2.5 keeps budget", model: "gemini-2.5-flash", thinking: enabled, output: effort("high"), want: `{"includeThoughts":true,"thinkingBudget":2048}`},
		{name: "disabled", model: "gemini-4-neon", thinking: disabled, want: `{}`},
		{name: "disabled ignores effort", model: "gemini-4-neon", thinking: disabled, output: effort("high"), want: `{}`},
		{name: "effort ignored before gemini 3", model: "gemini-2.5-pro", output: effort("high"), want: ""},
		{name: "adaptive on gemini 2.5 has no level", model: "gemini-2.5-pro", thinking: adaptive, output: effort("high"), want: `{"includeThoughts":true}`},
		{name: "unknown effort is a 400", model: "gemini-4-neon", output: effort("extreme"), wantErr: `unsupported output_config.effort "extreme"`},
		{name: "unknown effort is a 400 even when disabled", model: "gemini-4-neon", thinking: disabled, output: effort("extreme"), wantErr: "unsupported output_config.effort"},
	}
	for _, tc := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				req := &anthropicschema.MessagesRequest{
					Model:        tc.model,
					MaxTokens:    100,
					Stream:       stream,
					Thinking:     tc.thinking,
					OutputConfig: tc.output,
					Messages:     []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
				}
				_, body, err := NewAnthropicToGCPVertexAITranslator("").RequestBody(nil, req, false)
				if tc.wantErr != "" {
					require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
					require.ErrorContains(t, err, tc.wantErr)
					return
				}
				require.NoError(t, err)
				var got struct {
					GenerationConfig struct {
						ThinkingConfig json.RawMessage `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				require.NoError(t, json.Unmarshal(body, &got))
				if tc.want == "" {
					require.Empty(t, got.GenerationConfig.ThinkingConfig)
					return
				}
				require.JSONEq(t, tc.want, string(got.GenerationConfig.ThinkingConfig))
			})
		}
	}
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsUnsupportedOptions(t *testing.T) {
	container := anthropicschema.Container("container-1")
	contextManagement := anthropicschema.ContextManagement(map[string]any{"edits": []any{}})
	serviceTier := anthropicschema.MessageServiceTierStandardOnly

	tests := []struct {
		name        string
		configure   func(*anthropicschema.MessagesRequest)
		wantMessage string
	}{
		{name: "container", configure: func(req *anthropicschema.MessagesRequest) { req.Container = &container }, wantMessage: "container is not supported"},
		{name: "context management", configure: func(req *anthropicschema.MessagesRequest) { req.ContextManagement = &contextManagement }, wantMessage: "context_management is not supported"},
		{name: "MCP servers", configure: func(req *anthropicschema.MessagesRequest) {
			req.MCPServers = []anthropicschema.MCPServer{map[string]any{}}
		}, wantMessage: "mcp_servers are not supported"},
		{name: "service tier", configure: func(req *anthropicschema.MessagesRequest) { req.ServiceTier = &serviceTier }, wantMessage: "service_tier is not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &anthropicschema.MessagesRequest{
				Model:     "gemini-1.5-pro",
				MaxTokens: 100,
				Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
			}
			tt.configure(req)
			tr := NewAnthropicToGCPVertexAITranslator("")
			_, _, err := tr.RequestBody(nil, req, false)
			require.ErrorContains(t, err, tt.wantMessage)
		})
	}
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsInvalidNumericValues(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*anthropicschema.MessagesRequest)
		wantMessage string
	}{
		{name: "zero max tokens", configure: func(req *anthropicschema.MessagesRequest) { req.MaxTokens = 0 }, wantMessage: "max_tokens must be a positive integer"},
		{name: "fractional max tokens", configure: func(req *anthropicschema.MessagesRequest) { req.MaxTokens = 1.5 }, wantMessage: "max_tokens must be a positive integer"},
		{name: "overflowing max tokens", configure: func(req *anthropicschema.MessagesRequest) { req.MaxTokens = 1 << 31 }, wantMessage: "max_tokens must be a positive integer"},
		{name: "negative temperature", configure: func(req *anthropicschema.MessagesRequest) { value := -0.1; req.Temperature = &value }, wantMessage: "temperature must be between 0 and 1"},
		{name: "temperature over one", configure: func(req *anthropicschema.MessagesRequest) { value := 1.1; req.Temperature = &value }, wantMessage: "temperature must be between 0 and 1"},
		{name: "negative top p", configure: func(req *anthropicschema.MessagesRequest) { value := -0.1; req.TopP = &value }, wantMessage: "top_p must be between 0 and 1"},
		{name: "top p over one", configure: func(req *anthropicschema.MessagesRequest) { value := 1.1; req.TopP = &value }, wantMessage: "top_p must be between 0 and 1"},
		{name: "negative top k", configure: func(req *anthropicschema.MessagesRequest) { value := -1; req.TopK = &value }, wantMessage: "top_k must be a non-negative integer"},
		{name: "imprecise top k", configure: func(req *anthropicschema.MessagesRequest) { value := 1<<24 + 1; req.TopK = &value }, wantMessage: "top_k must be a non-negative integer"},
		{name: "negative thinking budget", configure: func(req *anthropicschema.MessagesRequest) {
			req.Thinking = &anthropicschema.Thinking{Enabled: &anthropicschema.ThinkingEnabled{BudgetTokens: -1}}
		}, wantMessage: "thinking budget_tokens must be a non-negative integer"},
		{name: "fractional thinking budget", configure: func(req *anthropicschema.MessagesRequest) {
			req.Thinking = &anthropicschema.Thinking{Enabled: &anthropicschema.ThinkingEnabled{BudgetTokens: 1024.5}}
		}, wantMessage: "thinking budget_tokens must be a non-negative integer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &anthropicschema.MessagesRequest{
				Model:     "gemini-1.5-pro",
				MaxTokens: 100,
				Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
			}
			tt.configure(req)
			tr := NewAnthropicToGCPVertexAITranslator("")
			_, _, err := tr.RequestBody(nil, req, false)
			require.ErrorContains(t, err, tt.wantMessage)
		})
	}
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
					InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`),
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
		"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get weather","parametersJsonSchema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}},
		"generationConfig":{"maxOutputTokens":100}
	}`, string(body))
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsNonObjectToolSchema(t *testing.T) {
	for _, schema := range []string{`["not","an","object"]`, `null`, `"string"`, `42`, `true`, `{"type":`} {
		t.Run(schema, func(t *testing.T) {
			req := &anthropicschema.MessagesRequest{
				Model:     "gemini-1.5-pro",
				MaxTokens: 100,
				Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}}},
				Tools: []anthropicschema.ToolUnion{{Tool: &anthropicschema.Tool{
					Type:        "custom",
					Name:        "get_weather",
					InputSchema: json.RawMessage(schema),
				}}},
			}

			tr := NewAnthropicToGCPVertexAITranslator("")
			_, _, err := tr.RequestBody(nil, req, false)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
			require.ErrorContains(t, err, "tool get_weather parameters must be a JSON object")
		})
	}
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsBuiltInTools(t *testing.T) {
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-1.5-pro",
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Search the web"}}},
		Tools: []anthropicschema.ToolUnion{{
			WebSearchTool: &anthropicschema.WebSearchTool{Type: "web_search_20250305", Name: "web_search"},
		}},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, req, false)
	require.ErrorContains(t, err, "Anthropic built-in tool unsupported")
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsDisabledParallelToolUse(t *testing.T) {
	disableParallelToolUse := true
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-1.5-pro",
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Use a tool"}}},
		ToolChoice: &anthropicschema.ToolChoice{Auto: &anthropicschema.ToolChoiceAuto{
			Type:                   "auto",
			DisableParallelToolUse: &disableParallelToolUse,
		}},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, req, false)
	require.ErrorContains(t, err, "disable_parallel_tool_use is not supported")
}

func TestAnthropicToGCPVertexAI_RequestBodyRejectsErrorToolResult(t *testing.T) {
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-1.5-pro",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{{
			Role: anthropicschema.MessageRoleUser,
			Content: anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{{
				ToolResult: &anthropicschema.ToolResultBlockParam{
					Type:      "tool_result",
					ToolUseID: "tool-1",
					Content:   &anthropicschema.ToolResultContent{Text: "tool failed"},
					IsError:   true,
				},
			}}},
		}},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, req, false)
	require.ErrorContains(t, err, "tool_result is_error is not supported")
}

func TestAnthropicToGCPVertexAI_RequestBodyIgnoresCacheControl(t *testing.T) {
	cacheControl := &anthropicschema.CacheControl{Ephemeral: &anthropicschema.CacheControlEphemeral{Type: "ephemeral"}}
	tests := []struct {
		name      string
		configure func(*anthropicschema.MessagesRequest)
	}{
		{name: "system", configure: func(req *anthropicschema.MessagesRequest) {
			req.System = &anthropicschema.SystemPrompt{Texts: []anthropicschema.TextBlockParam{{Type: "text", Text: "system", CacheControl: cacheControl}}}
		}},
		{name: "text", configure: func(req *anthropicschema.MessagesRequest) {
			req.Messages[0].Content = anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "Hello", CacheControl: cacheControl}}}}
		}},
		{name: "image", configure: func(req *anthropicschema.MessagesRequest) {
			req.Messages[0].Content = anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{{Image: &anthropicschema.ImageBlockParam{Type: "image", Source: anthropicschema.ImageSource{URL: &anthropicschema.URLImageSource{Type: "url", URL: "https://example.com/image.png"}}, CacheControl: cacheControl}}}}
		}},
		{name: "tool use", configure: func(req *anthropicschema.MessagesRequest) {
			req.Messages[0] = anthropicschema.MessageParam{Role: anthropicschema.MessageRoleAssistant, Content: anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{{ToolUse: &anthropicschema.ToolUseBlockParam{Type: "tool_use", ID: "tool-1", Name: "tool", Input: map[string]any{}, CacheControl: cacheControl}}}}}
		}},
		{name: "tool result", configure: func(req *anthropicschema.MessagesRequest) {
			req.Messages[0].Content = anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{{ToolResult: &anthropicschema.ToolResultBlockParam{Type: "tool_result", ToolUseID: "tool-1", CacheControl: cacheControl}}}}
		}},
		{name: "tool result text", configure: func(req *anthropicschema.MessagesRequest) {
			req.Messages[0].Content = anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{{ToolResult: &anthropicschema.ToolResultBlockParam{
				Type:      "tool_result",
				ToolUseID: "tool-1",
				Content: &anthropicschema.ToolResultContent{Array: []anthropicschema.ToolResultContentItem{{
					Text: &anthropicschema.TextBlockParam{Type: "text", Text: "result", CacheControl: cacheControl},
				}}},
			}}}}
		}},
		{name: "tool declaration", configure: func(req *anthropicschema.MessagesRequest) {
			req.Tools = []anthropicschema.ToolUnion{{Tool: &anthropicschema.Tool{Type: "custom", Name: "tool", InputSchema: json.RawMessage(`{"type":"object"}`), CacheControl: cacheControl}}}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &anthropicschema.MessagesRequest{
				Model:     "gemini-1.5-pro",
				MaxTokens: 100,
				Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
			}
			tt.configure(req)
			tr := NewAnthropicToGCPVertexAITranslator("")
			_, body, err := tr.RequestBody(nil, req, false)
			require.NoError(t, err)
			require.NotContains(t, string(body), "cache_control")
		})
	}
}

func TestAnthropicToGCPVertexAI_RequestBodyReplaysThinkingSignature(t *testing.T) {
	req := &anthropicschema.MessagesRequest{
		Model:     "gemini-2.5-pro",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{
					{Thinking: &anthropicschema.ThinkingBlockParam{Type: "thinking", Thinking: "I should check the weather.", Signature: "dGVzdC1zaWduYXR1cmU="}},
					{ToolUse: &anthropicschema.ToolUseBlockParam{Type: "tool_use", ID: "tool-1", Name: "get_weather", Input: map[string]any{"location": "Paris"}}},
				}},
			},
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Array: []anthropicschema.ContentBlockParam{
					{ToolResult: &anthropicschema.ToolResultBlockParam{Type: "tool_result", ToolUseID: "tool-1", Content: &anthropicschema.ToolResultContent{Text: "sunny"}}},
				}},
			},
		},
	}

	tr := NewAnthropicToGCPVertexAITranslator("")
	_, body, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"contents":[
			{"parts":[
				{"functionCall":{"args":{"location":"Paris"},"name":"get_weather"},"thoughtSignature":"dGVzdC1zaWduYXR1cmU="},
				{"text":"I should check the weather.","thought":true}
			],"role":"model"},
			{"parts":[{"functionResponse":{"name":"get_weather","response":{"output":"sunny"}}}],"role":"user"}
		],
		"tools":null,
		"generationConfig":{"maxOutputTokens":100}
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
		"usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":3,"input_tokens":7,"output_tokens":15}
	}`, string(body))
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(15), outputTokens)
	cachedTokens, ok := tokenUsage.CachedInputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(3), cachedTokens)
	cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
	require.True(t, ok)
	require.Zero(t, cacheCreationTokens)
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

func TestAnthropicToGCPVertexAI_ResponseBodyNonStreamingPromptBlocked(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	_, _, err := tr.RequestBody(nil, &anthropicschema.MessagesRequest{
		Model:     "ignored",
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
	}, false)
	require.NoError(t, err)

	_, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(`{
		"promptFeedback":{"blockReason":"SAFETY"},
		"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}
	}`), true, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"stop_reason":"refusal"`)
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
	require.Contains(t, out, `"usage":{"input_tokens":7,"cache_read_input_tokens":3,"output_tokens":5}`)
	require.Contains(t, out, "event: message_stop")
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), outputTokens)
	cachedTokens, ok := tokenUsage.CachedInputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(3), cachedTokens)
	cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
	require.True(t, ok)
	require.Zero(t, cacheCreationTokens)
}

func TestAnthropicToGCPVertexAI_ResponseBodyStreamingWithoutCandidates(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	_, _, err := tr.RequestBody(nil, &anthropicschema.MessagesRequest{
		Model:     "ignored",
		Stream:    true,
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
	}, false)
	require.NoError(t, err)

	streamBody := `data: {"responseId":"resp-blocked","promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}

`
	_, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(streamBody), true, nil)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(body)
	require.Len(t, events, 3)
	require.Equal(t, "message_start", events[0].eventType)
	require.Equal(t, "message_delta", events[1].eventType)
	require.Contains(t, events[1].data, `"stop_reason":"refusal"`)
	require.Equal(t, "message_stop", events[2].eventType)
}

func TestAnthropicToGCPVertexAI_ResponseBodyStreamingRecordsSpan(t *testing.T) {
	tr := NewAnthropicToGCPVertexAITranslator("gemini-1.5-pro")
	_, _, err := tr.RequestBody(nil, &anthropicschema.MessagesRequest{
		Model:     "ignored",
		Stream:    true,
		MaxTokens: 100,
		Messages:  []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}}},
	}, false)
	require.NoError(t, err)

	span := &recordingAnthropicMessageSpan{}
	_, _, _, _, err = tr.ResponseBody(nil, strings.NewReader(`data: {"responseId":"resp-1","candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP"}]}

`), true, span)
	require.NoError(t, err)
	require.NotEmpty(t, span.chunks)
	require.NotNil(t, span.chunks[0].MessageStart)
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

type recordingAnthropicMessageSpan struct {
	chunks []*anthropicschema.MessagesStreamChunk
}

func (s *recordingAnthropicMessageSpan) RecordResponseChunk(chunk *anthropicschema.MessagesStreamChunk) {
	s.chunks = append(s.chunks, chunk)
}

func (s *recordingAnthropicMessageSpan) RecordResponse(*anthropicschema.MessagesResponse) {}
func (s *recordingAnthropicMessageSpan) EndSpanOnError(int, []byte)                       {}
func (s *recordingAnthropicMessageSpan) EndSpan()                                         {}

// sseBlock is one Anthropic content block rebuilt from stream events.
type sseBlock struct {
	Type      string
	Thinking  string
	Signature string
	Text      string
	ToolName  string
	ToolInput string
}

// assembleSSEBlocks rebuilds content blocks from Anthropic SSE events and checks that the block
// indexes are consecutive, each block is opened before its deltas and closed before the next opens.
func assembleSSEBlocks(t *testing.T, sse []byte) []sseBlock {
	t.Helper()
	var blocks []sseBlock
	open := -1
	for line := range strings.SplitSeq(string(sse), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		require.NoError(t, json.Unmarshal([]byte(data), &ev))
		switch ev.Type {
		case "content_block_start":
			require.Equal(t, -1, open, "block %d started while block %d is open", ev.Index, open)
			require.Equal(t, len(blocks), ev.Index, "block indexes must be consecutive")
			blocks = append(blocks, sseBlock{Type: ev.ContentBlock.Type, ToolName: ev.ContentBlock.Name})
			open = ev.Index
		case "content_block_delta":
			require.Equal(t, open, ev.Index, "delta for a block that is not open")
			b := &blocks[open]
			switch ev.Delta.Type {
			case "thinking_delta":
				require.Equal(t, "thinking", b.Type)
				require.Empty(t, b.Signature, "thinking_delta after signature_delta")
				b.Thinking += ev.Delta.Thinking
			case "signature_delta":
				require.Equal(t, "thinking", b.Type)
				require.Empty(t, b.Signature, "a thinking block has one signature_delta")
				b.Signature = ev.Delta.Signature
			case "text_delta":
				b.Text += ev.Delta.Text
			case "input_json_delta":
				b.ToolInput += ev.Delta.PartialJSON
			}
		case "content_block_stop":
			require.Equal(t, open, ev.Index)
			open = -1
		}
	}
	require.Equal(t, -1, open, "a block is left open")
	return blocks
}

func TestAnthropicToGCPVertexAI_ThoughtSignatures(t *testing.T) {
	// "c2ln" and "c2lnLTI=" are base64 for "sig" and "sig-2".
	call := func(name, args, sig string) string {
		s := fmt.Sprintf(`{"functionCall":{"name":%q,"args":%s}`, name, args)
		if sig != "" {
			s += fmt.Sprintf(`,"thoughtSignature":%q`, sig)
		}
		return s + "}"
	}
	thought := func(text string) string { return fmt.Sprintf(`{"text":%q,"thought":true}`, text) }
	text := func(text, sig string) string {
		s := fmt.Sprintf(`{"text":%q`, text)
		if sig != "" {
			s += fmt.Sprintf(`,"thoughtSignature":%q`, sig)
		}
		return s + "}"
	}

	tests := []struct {
		name   string
		chunks [][]string // parts of each streamed chunk
		want   []sseBlock
		stop   string
		// multiSig marks responses with several signatures, which the non-streaming response
		// collapses to one.
		multiSig bool
	}{
		{
			name:   "signature on functionCall without thought text",
			chunks: [][]string{{call("get_weather", `{"city":"Paris"}`, "c2ln")}},
			want: []sseBlock{
				{Type: "thinking", Signature: "c2ln"},
				{Type: "tool_use", ToolName: "get_weather", ToolInput: `{"city":"Paris"}`},
			},
			stop: "tool_use",
		},
		{
			name: "thought summary, then signature on a later functionCall chunk",
			chunks: [][]string{
				{thought("Let me ")},
				{thought("check.")},
				{call("get_weather", `{"city":"Paris"}`, "c2ln")},
			},
			want: []sseBlock{
				{Type: "thinking", Thinking: "Let me check.", Signature: "c2ln"},
				{Type: "tool_use", ToolName: "get_weather", ToolInput: `{"city":"Paris"}`},
			},
			stop: "tool_use",
		},
		{
			name:   "thought summary and signed functionCall in one chunk do not duplicate the summary",
			chunks: [][]string{{thought("Let me check."), call("a", `{}`, "c2ln")}},
			want: []sseBlock{
				{Type: "thinking", Thinking: "Let me check.", Signature: "c2ln"},
				{Type: "tool_use", ToolName: "a", ToolInput: `{}`},
			},
			stop: "tool_use",
		},
		{
			name:   "parallel calls carry the signature on the first only",
			chunks: [][]string{{call("a", `{}`, "c2ln"), call("b", `{"x":1}`, "")}},
			want: []sseBlock{
				{Type: "thinking", Signature: "c2ln"},
				{Type: "tool_use", ToolName: "a", ToolInput: `{}`},
				{Type: "tool_use", ToolName: "b", ToolInput: `{"x":1}`},
			},
			stop: "tool_use",
		},
		{
			name:   "parallel calls in separate chunks",
			chunks: [][]string{{call("a", `{}`, "c2ln")}, {call("b", `{}`, "")}},
			want: []sseBlock{
				{Type: "thinking", Signature: "c2ln"},
				{Type: "tool_use", ToolName: "a", ToolInput: `{}`},
				{Type: "tool_use", ToolName: "b", ToolInput: `{}`},
			},
			stop: "tool_use",
		},
		{
			name:   "signature on the same chunk as text comes before the text",
			chunks: [][]string{{text("Hello", "c2ln")}},
			want:   []sseBlock{{Type: "thinking", Signature: "c2ln"}, {Type: "text", Text: "Hello"}},
			stop:   "end_turn",
		},
		{
			name:   "signature-only part with empty text after the text",
			chunks: [][]string{{text("Hello", "")}, {text("", "c2ln")}},
			want:   []sseBlock{{Type: "text", Text: "Hello"}, {Type: "thinking", Signature: "c2ln"}},
			stop:   "end_turn",
		},
		{
			name:   "summary and signature on the thought part",
			chunks: [][]string{{`{"text":"Reasoning","thought":true,"thoughtSignature":"c2ln"}`}, {text("Answer", "")}},
			want:   []sseBlock{{Type: "thinking", Thinking: "Reasoning", Signature: "c2ln"}, {Type: "text", Text: "Answer"}},
			stop:   "end_turn",
		},
		{
			name:   "a second signature opens a second thinking block",
			chunks: [][]string{{text("", "c2ln")}, {call("a", `{}`, "c2lnLTI=")}},
			want: []sseBlock{
				{Type: "thinking", Signature: "c2ln"},
				{Type: "thinking", Signature: "c2lnLTI="},
				{Type: "tool_use", ToolName: "a", ToolInput: `{}`},
			},
			stop:     "tool_use",
			multiSig: true,
		},
		{
			name:   "no signature and no summary means no thinking block",
			chunks: [][]string{{text("Hello", "")}, {text(" world", "")}},
			want:   []sseBlock{{Type: "text", Text: "Hello world"}},
			stop:   "end_turn",
		},
		{
			name:   "no signature on a functionCall means no thinking block",
			chunks: [][]string{{call("a", `{}`, "")}},
			want:   []sseBlock{{Type: "tool_use", ToolName: "a", ToolInput: `{}`}},
			stop:   "tool_use",
		},
		{
			name:   "summary without signature still streams as thinking",
			chunks: [][]string{{thought("Hmm")}, {text("Answer", "")}},
			want:   []sseBlock{{Type: "thinking", Thinking: "Hmm"}, {Type: "text", Text: "Answer"}},
			stop:   "end_turn",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &anthropicschema.MessagesRequest{
				Model: "gemini-4-neon", MaxTokens: 100, Stream: true,
				Messages: []anthropicschema.MessageParam{{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}}},
			}
			var sse strings.Builder
			for i, parts := range tc.chunks {
				finish := ""
				if i == len(tc.chunks)-1 {
					finish = `,"finishReason":"STOP"`
				}
				fmt.Fprintf(&sse, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[%s]}%s}]}\n\n", strings.Join(parts, ","), finish)
			}
			sse.WriteString(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3}}` + "\n\n")

			// Feed the stream one chunk at a time, like Envoy, and also check the streamed result
			// against the non-streaming translation of the same parts.
			tr := NewAnthropicToGCPVertexAITranslator("")
			_, _, err := tr.RequestBody(nil, req, false)
			require.NoError(t, err)
			var out []byte
			events := strings.SplitAfter(sse.String(), "\n\n")
			for i, ev := range events {
				if ev == "" {
					continue
				}
				_, b, _, _, rerr := tr.ResponseBody(nil, strings.NewReader(ev), i == len(events)-1, nil)
				require.NoError(t, rerr)
				out = append(out, b...)
			}
			got := assembleSSEBlocks(t, out)
			// Tool IDs are random, so only compare names and inputs.
			require.Equal(t, tc.want, got)
			require.Contains(t, string(out), `"stop_reason":"`+tc.stop+`"`)
			require.Equal(t, 1, strings.Count(string(out), "event: message_start"))
			require.Equal(t, 1, strings.Count(string(out), "event: message_stop"))

			// Non-streaming equivalence: one response made of all the chunks' parts.
			var all []string
			for _, parts := range tc.chunks {
				all = append(all, parts...)
			}
			nreq := *req
			nreq.Stream = false
			ntr := NewAnthropicToGCPVertexAITranslator("")
			_, _, err = ntr.RequestBody(nil, &nreq, false)
			require.NoError(t, err)
			resp := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[%s]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3}}`, strings.Join(all, ","))
			_, nb, _, _, err := ntr.ResponseBody(nil, strings.NewReader(resp), true, nil)
			require.NoError(t, err)
			var nr anthropicschema.MessagesResponse
			require.NoError(t, json.Unmarshal(nb, &nr))
			var sigs, nsigs []string
			for _, b := range got {
				if b.Type == "thinking" && b.Signature != "" {
					sigs = append(sigs, b.Signature)
				}
			}
			for _, b := range nr.Content {
				if b.Thinking != nil && b.Thinking.Signature != "" {
					nsigs = append(nsigs, b.Thinking.Signature)
				}
			}
			// The non-streaming response keeps a single signature; the stream must carry it first.
			if tc.multiSig {
				return
			}
			if len(nsigs) > 0 {
				require.Equal(t, nsigs[0], sigs[0])
			} else {
				require.Empty(t, sigs)
			}
		})
	}
}

func TestAnthropicToGCPVertexAI_RequestBodyThoughtSignatureHistory(t *testing.T) {
	thinking := func(text, sig string) anthropicschema.ContentBlockParam {
		return anthropicschema.ContentBlockParam{Thinking: &anthropicschema.ThinkingBlockParam{Type: "thinking", Thinking: text, Signature: sig}}
	}
	toolUse := func(id, name string) anthropicschema.ContentBlockParam {
		return anthropicschema.ContentBlockParam{ToolUse: &anthropicschema.ToolUseBlockParam{Type: "tool_use", ID: id, Name: name, Input: map[string]any{}}}
	}
	text := func(s string) anthropicschema.ContentBlockParam {
		return anthropicschema.ContentBlockParam{Text: &anthropicschema.TextBlockParam{Type: "text", Text: s}}
	}
	toolResult := func(id string) anthropicschema.ContentBlockParam {
		return anthropicschema.ContentBlockParam{ToolResult: &anthropicschema.ToolResultBlockParam{Type: "tool_result", ToolUseID: id, Content: &anthropicschema.ToolResultContent{Text: "ok"}}}
	}

	tests := []struct {
		name      string
		assistant []anthropicschema.ContentBlockParam
		followUp  *anthropicschema.ContentBlockParam // user turn after the assistant turn
		want      string                             // expected model-turn parts
		wantErr   string
	}{
		{
			name:      "signature-only thinking before a tool call lands on the functionCall",
			assistant: []anthropicschema.ContentBlockParam{thinking("", "c2ln"), toolUse("t1", "a")},
			want:      `[{"functionCall":{"name":"a"},"thoughtSignature":"c2ln"}]`,
		},
		{
			name:      "parallel calls: signature on the first functionCall only",
			assistant: []anthropicschema.ContentBlockParam{thinking("", "c2ln"), toolUse("t1", "a"), toolUse("t2", "b")},
			want:      `[{"functionCall":{"name":"a"},"thoughtSignature":"c2ln"},{"functionCall":{"name":"b"}}]`,
		},
		{
			name:      "tool call without any signature uses Google's documented dummy signature",
			assistant: []anthropicschema.ContentBlockParam{toolUse("t1", "a")},
			want:      `[{"functionCall":{"name":"a"},"thoughtSignature":"c2tpcF90aG91Z2h0X3NpZ25hdHVyZV92YWxpZGF0b3I="}]`,
		},
		{
			name:      "signature-only thinking before a text answer lands on the text part",
			assistant: []anthropicschema.ContentBlockParam{thinking("", "c2ln"), text("Hello")},
			want:      `[{"text":"Hello","thoughtSignature":"c2ln"}]`,
		},
		{
			name:      "signature goes on the last text part",
			assistant: []anthropicschema.ContentBlockParam{text("one"), thinking("", "c2ln"), text("two")},
			want:      `[{"text":"one"},{"text":"two","thoughtSignature":"c2ln"}]`,
		},
		{
			name:      "no thinking blocks leaves a text answer unchanged",
			assistant: []anthropicschema.ContentBlockParam{text("Hello")},
			want:      `[{"text":"Hello"}]`,
		},
		{
			name:      "redacted thinking from another provider is dropped",
			assistant: []anthropicschema.ContentBlockParam{{RedactedThinking: &anthropicschema.RedactedThinkingBlockParam{Type: "redacted_thinking", Data: "opaque"}}, text("Hello")},
			want:      `[{"text":"Hello"}]`,
		},
		{
			name:      "signature that is not base64 is a 400",
			assistant: []anthropicschema.ContentBlockParam{thinking("x", "not base64!"), text("Hello")},
			wantErr:   "thinking signature must be base64",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
				{Role: anthropicschema.MessageRoleAssistant, Content: anthropicschema.MessageContent{Array: tc.assistant}},
			}
			var results []anthropicschema.ContentBlockParam
			for _, b := range tc.assistant {
				if b.ToolUse != nil {
					results = append(results, toolResult(b.ToolUse.ID))
				}
			}
			if len(results) > 0 {
				msgs = append(msgs, anthropicschema.MessageParam{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Array: results}})
			} else {
				msgs = append(msgs, anthropicschema.MessageParam{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Next"}})
			}
			req := &anthropicschema.MessagesRequest{Model: "gemini-4-neon", MaxTokens: 100, Messages: msgs}
			_, body, err := NewAnthropicToGCPVertexAITranslator("").RequestBody(nil, req, false)
			if tc.wantErr != "" {
				require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			var got struct {
				Contents []struct {
					Role  string          `json:"role"`
					Parts json.RawMessage `json:"parts"`
				} `json:"contents"`
			}
			require.NoError(t, json.Unmarshal(body, &got))
			require.Equal(t, "model", got.Contents[1].Role)
			require.JSONEq(t, tc.want, string(got.Contents[1].Parts))
		})
	}
}
