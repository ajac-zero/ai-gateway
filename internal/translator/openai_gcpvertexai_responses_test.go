// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

// parseResponsesRequest parses a raw Responses request body the same way the endpoint spec does.
func parseResponsesRequest(t *testing.T, body string) *openai.ResponseRequest {
	t.Helper()
	var req openai.ResponseRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return &req
}

// translateResponsesRequest runs RequestBody and returns the path and body sent to Vertex AI.
func translateResponsesRequest(t *testing.T, modelOverride, body string) (string, string) {
	t.Helper()
	tr := NewResponsesOpenAIToGCPVertexAITranslator(modelOverride)
	headers, newBody, err := tr.RequestBody([]byte(body), parseResponsesRequest(t, body), false)
	require.NoError(t, err)
	require.Len(t, headers, 2)
	require.Equal(t, pathHeaderName, headers[0].Key())
	require.Equal(t, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))}, headers[1])
	return headers[0].Value(), string(newBody)
}

func TestResponsesOpenAIToGCPVertexAI_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name, model, body string
		expPath, expBody  string
	}{
		{
			name:    "string input with instructions",
			body:    `{"model":"gemini-2.5-flash","instructions":"Be brief.","input":"Hi"}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"tools":null,"generationConfig":{},
				"systemInstruction":{"parts":[{"text":"Be brief."}]}}`,
		},
		{
			name:    "streaming uses streamGenerateContent with SSE and the model override",
			model:   "gemini-2.5-pro",
			body:    `{"model":"alias","input":"Hi","stream":true}`,
			expPath: "publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"tools":null,"generationConfig":{}}`,
		},
		{
			name: "multi-turn conversation with system, developer, images and sampling",
			body: `{
				"model":"gemini-2.5-flash",
				"input":[
					{"role":"system","content":"System rule."},
					{"role":"user","content":[
						{"type":"input_text","text":"What is in this image?"},
						{"type":"input_image","image_url":"data:image/png;base64,iVBORw0K","detail":"auto"},
						{"type":"input_image","image_url":"https://example.com/cat.png"}
					]},
					{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A cat.","annotations":[]}]},
					{"type":"message","role":"developer","content":[{"type":"input_text","text":"Developer rule."}]},
					{"role":"user","content":"Is it cute?"}
				],
				"temperature":0.5,
				"top_p":0.25,
				"max_output_tokens":256,
				"presence_penalty":0.5,
				"frequency_penalty":0.25,
				"store":false,
				"user":"user-1"
			}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{
				"contents":[
					{"role":"user","parts":[
						{"text":"What is in this image?"},
						{"inlineData":{"mimeType":"image/png","data":"iVBORw0K"}},
						{"fileData":{"mimeType":"image/png","fileUri":"https://example.com/cat.png"}}
					]},
					{"role":"model","parts":[{"text":"A cat."}]},
					{"role":"user","parts":[{"text":"Is it cute?"}]}
				],
				"tools":null,
				"generationConfig":{"temperature":0.5,"topP":0.25,"maxOutputTokens":256,"presencePenalty":0.5,"frequencyPenalty":0.25},
				"systemInstruction":{"parts":[{"text":"System rule."},{"text":"Developer rule."}]}
			}`,
		},
		{
			name: "function tools, tool choice, and function call history with a thought signature",
			body: `{
				"model":"gemini-2.5-flash",
				"input":[
					{"role":"user","content":"Weather in Paris and Rome?"},
					{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need weather."}],"encrypted_content":"c2lnLTE="},
					{"type":"function_call","call_id":"call_a","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},
					{"type":"function_call","call_id":"call_b","name":"get_weather","arguments":"{\"city\":\"Rome\"}"},
					{"type":"function_call_output","call_id":"call_a","output":"18C"},
					{"type":"function_call_output","call_id":"call_b","output":[{"type":"input_text","text":"21"},{"type":"input_text","text":"C"}]},
					{"role":"user","content":"Thanks, and London?"}
				],
				"tools":[{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]},"strict":true}],
				"tool_choice":{"type":"function","name":"get_weather"}
			}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{
				"contents":[
					{"role":"user","parts":[{"text":"Weather in Paris and Rome?"}]},
					{"role":"model","parts":[
						{"functionCall":{"name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"c2lnLTE="},
						{"functionCall":{"name":"get_weather","args":{"city":"Rome"}}}
					]},
					{"role":"user","parts":[
						{"functionResponse":{"name":"get_weather","response":{"output":"18C"}}},
						{"functionResponse":{"name":"get_weather","response":{"output":"21C"}}}
					]},
					{"role":"user","parts":[{"text":"Thanks, and London?"}]}
				],
				"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get weather",
					"parametersJsonSchema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}],
				"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}},
				"generationConfig":{}
			}`,
		},
		{
			name: "function call without a reasoning item gets the documented dummy signature",
			body: `{
				"model":"gemini-3-flash-preview",
				"input":[
					{"role":"user","content":"Hi"},
					{"type":"function_call","call_id":"c1","name":"f","arguments":""},
					{"type":"function_call_output","call_id":"c1","output":"ok"}
				],
				"tools":[{"type":"function","name":"f"}],
				"tool_choice":"required"
			}`,
			expPath: "publishers/google/models/gemini-3-flash-preview:generateContent",
			expBody: `{
				"contents":[
					{"role":"user","parts":[{"text":"Hi"}]},
					{"role":"model","parts":[{"functionCall":{"name":"f"},"thoughtSignature":"c2tpcF90aG91Z2h0X3NpZ25hdHVyZV92YWxpZGF0b3I="}]},
					{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"ok"}}}]}
				],
				"tools":[{"functionDeclarations":[{"name":"f"}]}],
				"toolConfig":{"functionCallingConfig":{"mode":"ANY"}},
				"generationConfig":{}
			}`,
		},
		{
			name: "signature on a text-only assistant turn goes on its last part",
			body: `{
				"model":"gemini-2.5-flash",
				"input":[
					{"role":"user","content":"Hi"},
					{"role":"assistant","content":"Hello"},
					{"role":"assistant","content":" there"},
					{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"c2lnLTI="},
					{"role":"user","content":"Bye"}
				]
			}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{
				"contents":[
					{"role":"user","parts":[{"text":"Hi"}]},
					{"role":"model","parts":[{"text":"Hello"},{"text":" there","thoughtSignature":"c2lnLTI="}]},
					{"role":"user","parts":[{"text":"Bye"}]}
				],
				"tools":null,
				"generationConfig":{}
			}`,
		},
		{
			name: "allowed_tools declares only the allowed functions",
			body: `{
				"model":"gemini-2.5-flash","input":"Hi",
				"tools":[{"type":"function","name":"a"},{"type":"function","name":"b"}],
				"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"b"}]}
			}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],
				"tools":[{"functionDeclarations":[{"name":"b"}]}],
				"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
				"generationConfig":{}}`,
		},
		{
			name:    "json_schema text format uses responseJsonSchema on Gemini 2.5",
			body:    `{"model":"gemini-2.5-flash","input":"Hi","text":{"format":{"type":"json_schema","name":"person","schema":{"type":"object","properties":{"name":{"type":"string"}}}}}}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"tools":null,
				"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":{"type":"object","properties":{"name":{"type":"string"}}}}}`,
		},
		{
			name:    "json_object text format",
			body:    `{"model":"gemini-2.5-flash","input":"Hi","text":{"format":{"type":"json_object"}}}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"tools":null,"generationConfig":{"responseMimeType":"application/json"}}`,
		},
		{
			name:    "reasoning effort and summary on Gemini 2.5 use a thinking budget",
			body:    `{"model":"gemini-2.5-flash","input":"Hi","reasoning":{"effort":"medium","summary":"auto"}}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"tools":null,
				"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":8192}}}`,
		},
		{
			name:    "input_file with a data URL and a PDF URL",
			body:    `{"model":"gemini-2.5-flash","input":[{"role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,JVBERg==","filename":"a.pdf"},{"type":"input_file","file_url":"https://example.com/doc.pdf"}]}]}`,
			expPath: "publishers/google/models/gemini-2.5-flash:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"application/pdf","data":"JVBERg=="}},{"fileData":{"mimeType":"application/pdf","fileUri":"https://example.com/doc.pdf"}}]}],"tools":null,"generationConfig":{}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, body := translateResponsesRequest(t, tc.model, tc.body)
			require.Equal(t, tc.expPath, p)
			require.JSONEq(t, tc.expBody, body)
		})
	}
}

func TestResponsesOpenAIToGCPVertexAI_RequestBody_PreGemini25Schemas(t *testing.T) {
	// Models before Gemini 2.5 only accept the OpenAPI subset, so JSON schemas are converted.
	_, body := translateResponsesRequest(t, "", `{
		"model":"gemini-2.0-flash","input":"Hi",
		"tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"x":{"type":"integer"}}}}],
		"text":{"format":{"type":"json_schema","name":"n","schema":{"type":"object","properties":{"y":{"type":"string"}}}}}
	}`)
	require.False(t, gjson.Get(body, "generationConfig.responseJsonSchema").Exists())
	require.Equal(t, "application/json", gjson.Get(body, "generationConfig.responseMimeType").String())
	require.True(t, gjson.Get(body, "generationConfig.responseSchema.properties.y").Exists(), body)
	require.False(t, gjson.Get(body, "tools.0.functionDeclarations.0.parametersJsonSchema").Exists())
	require.True(t, gjson.Get(body, "tools.0.functionDeclarations.0.parameters.properties.x").Exists(), body)
}

func TestResponsesOpenAIToGCPVertexAI_ReasoningMapping(t *testing.T) {
	// Expected values follow https://ai.google.dev/gemini-api/docs/openai#thinking.
	for _, tc := range []struct {
		model, effort string
		expThinking   string
		expErr        string
	}{
		{model: "gemini-2.5-flash", effort: "none", expThinking: `{"thinkingBudget":0}`},
		{model: "gemini-2.5-flash", effort: "minimal", expThinking: `{"thinkingBudget":1024}`},
		{model: "gemini-2.5-flash", effort: "low", expThinking: `{"thinkingBudget":1024}`},
		{model: "gemini-2.5-pro", effort: "high", expThinking: `{"thinkingBudget":24576}`},
		{model: "gemini-2.5-pro", effort: "xhigh", expErr: `reasoning.effort="xhigh" is not supported`},
		{model: "gemini-3-flash-preview", effort: "minimal", expThinking: `{"thinkingLevel":"MINIMAL"}`},
		{model: "gemini-3.1-pro-preview", effort: "minimal", expThinking: `{"thinkingLevel":"LOW"}`},
		{model: "gemini-3.1-pro-preview", effort: "medium", expThinking: `{"thinkingLevel":"MEDIUM"}`},
		{model: "gemini-3.1-pro-preview", effort: "high", expThinking: `{"thinkingLevel":"HIGH"}`},
		{model: "gemini-3-flash-preview", effort: "none", expErr: "thinking cannot be turned off"},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			body := `{"model":"` + tc.model + `","input":"Hi","reasoning":{"effort":"` + tc.effort + `"}}`
			tr := NewResponsesOpenAIToGCPVertexAITranslator("")
			_, newBody, err := tr.RequestBody([]byte(body), parseResponsesRequest(t, body), false)
			if tc.expErr != "" {
				require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expThinking, gjson.GetBytes(newBody, "generationConfig.thinkingConfig").Raw)
		})
	}
}

func TestResponsesOpenAIToGCPVertexAI_RequestBody_Rejected(t *testing.T) {
	for _, tc := range []struct {
		name, body, expErr string
	}{
		{"previous_response_id", `{"model":"m","input":"Hi","previous_response_id":"resp_1"}`, "previous_response_id"},
		{"conversation", `{"model":"m","input":"Hi","conversation":"conv_1"}`, "conversation"},
		{"store true", `{"model":"m","input":"Hi","store":true}`, "store=true"},
		{"background", `{"model":"m","input":"Hi","background":true}`, "background=true"},
		{"prompt template", `{"model":"m","prompt":{"id":"pmpt_1"}}`, "prompt templates"},
		{"context_management", `{"model":"m","input":"Hi","context_management":[{"type":"compaction"}]}`, "context_management"},
		{"truncation auto", `{"model":"m","input":"Hi","truncation":"auto"}`, `truncation="auto"`},
		{"flex service tier", `{"model":"m","input":"Hi","service_tier":"flex"}`, `service_tier="flex"`},
		{"top_logprobs", `{"model":"m","input":"Hi","top_logprobs":3}`, "top_logprobs"},
		{"verbosity", `{"model":"m","input":"Hi","text":{"verbosity":"low"}}`, `text.verbosity="low"`},
		{"include logprobs", `{"model":"m","input":"Hi","include":["message.output_text.logprobs"]}`, `include value "message.output_text.logprobs"`},
		{"parallel tool calls disabled", `{"model":"m","input":"Hi","parallel_tool_calls":false,"tools":[{"type":"function","name":"f"}]}`, "parallel_tool_calls=false"},
		{"web search tool", `{"model":"m","input":"Hi","tools":[{"type":"web_search"}]}`, `tool type "web_search"`},
		{"file search tool", `{"model":"m","input":"Hi","tools":[{"type":"file_search","vector_store_ids":["vs_1"]}]}`, `tool type "file_search"`},
		{"mcp tool", `{"model":"m","input":"Hi","tools":[{"type":"mcp","server_label":"x","server_url":"https://x"}]}`, `tool type "mcp"`},
		{"custom tool", `{"model":"m","input":"Hi","tools":[{"type":"custom","name":"x"}]}`, `tool type "custom"`},
		{"hosted tool choice", `{"model":"m","input":"Hi","tool_choice":{"type":"file_search"}}`, `tool_choice type "file_search"`},
		{"web search call item", `{"model":"m","input":[{"type":"web_search_call","id":"ws_1","status":"completed"}]}`, `input item type "web_search_call"`},
		{"item reference", `{"model":"m","input":[{"type":"item_reference","id":"msg_1"}]}`, `input item type "item_reference"`},
		{"unknown call id", `{"model":"m","input":[{"type":"function_call_output","call_id":"nope","output":"x"}]}`, `call_id "nope"`},
		{"image file id", `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`, "input_image file_id"},
		{"file id", `{"model":"m","input":[{"role":"user","content":[{"type":"input_file","file_id":"file_1"}]}]}`, "input_file file_id"},
		{"image in function output", `{"model":"m","input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","image_url":"https://x/y.png"}]}]}`, "non-text function_call_output"},
		{"invalid function arguments", `{"model":"m","input":[{"type":"function_call","call_id":"c","name":"f","arguments":"not json"}]}`, "arguments must be a JSON object"},
		{"bad encrypted content", `{"model":"m","input":[{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"%%%"}]}`, "encrypted_content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewResponsesOpenAIToGCPVertexAITranslator("")
			_, _, err := tr.RequestBody([]byte(tc.body), parseResponsesRequest(t, tc.body), false)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
			require.ErrorContains(t, err, tc.expErr)
		})
	}

	t.Run("accepted equivalents", func(t *testing.T) {
		for _, body := range []string{
			`{"model":"m","input":"Hi","store":false,"background":false,"truncation":"disabled","service_tier":"auto","text":{"verbosity":"medium"}}`,
			`{"model":"m","input":"Hi","include":["reasoning.encrypted_content"],"top_logprobs":0}`,
			`{"model":"m","input":"Hi","parallel_tool_calls":false}`,
		} {
			tr := NewResponsesOpenAIToGCPVertexAITranslator("")
			_, _, err := tr.RequestBody([]byte(body), parseResponsesRequest(t, body), false)
			require.NoError(t, err, body)
		}
	})
}

// translateResponsesResponse runs a non-streaming Gemini response through the translator.
func translateResponsesResponse(t *testing.T, reqBody, geminiBody string) (string, metrics.TokenUsage, string) {
	t.Helper()
	tr := NewResponsesOpenAIToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody([]byte(reqBody), parseResponsesRequest(t, reqBody), false)
	require.NoError(t, err)
	headers, body, usage, model, err := tr.ResponseBody(nil, strings.NewReader(geminiBody), true, nil)
	require.NoError(t, err)
	require.Equal(t, []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(body))}}, headers)
	return string(body), usage, model
}

// normalizeResponsesIDs replaces generated item and call ID values, which are random, with their prefix.
func normalizeResponsesIDs(t *testing.T, body string) string {
	t.Helper()
	var out strings.Builder
	for i := 0; i < len(body); {
		matched := false
		for _, prefix := range []string{`:"msg_`, `:"fc_`, `:"rs_`, `:"call_`} {
			if strings.HasPrefix(body[i:], prefix) {
				end := strings.IndexByte(body[i+len(prefix):], '"')
				require.Positive(t, end)
				out.WriteString(prefix + `ID"`)
				i += len(prefix) + end + 1
				matched = true
				break
			}
		}
		if !matched {
			out.WriteByte(body[i])
			i++
		}
	}
	return out.String()
}

func TestResponsesOpenAIToGCPVertexAI_ResponseBody(t *testing.T) {
	t.Run("text with thoughts, signature and usage", func(t *testing.T) {
		body, usage, model := translateResponsesResponse(t,
			`{"model":"gemini-2.5-flash","instructions":"Be brief.","input":"Hi","reasoning":{"effort":"low","summary":"auto"},"metadata":{"k":"v"}}`,
			`{"candidates":[{"content":{"role":"model","parts":[
				{"text":"Thinking about it.","thought":true},
				{"text":"Hello!","thoughtSignature":"c2lnLTE="}
			]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":3,"cachedContentTokenCount":4,"totalTokenCount":18},
			"modelVersion":"gemini-2.5-flash-001","createTime":"2025-07-11T22:15:44.956335Z","responseId":"abc"}`)
		require.Equal(t, "gemini-2.5-flash-001", model)
		require.JSONEq(t, `{
			"id":"resp_abc","object":"response","created_at":1752272144,"status":"completed","model":"gemini-2.5-flash-001",
			"instructions":"Be brief.","metadata":{"k":"v"},
			"output":[
				{"id":"rs_ID","type":"reasoning","summary":[{"type":"summary_text","text":"Thinking about it."}]},
				{"id":"msg_ID","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello!","annotations":[]}]},
				{"id":"rs_ID","type":"reasoning","summary":[],"encrypted_content":"c2lnLTE="}
			],
			"parallel_tool_calls":true,"temperature":1,"top_p":1,"tool_choice":"auto","truncation":"disabled","store":false,
			"reasoning":{"effort":"low","summary":"auto"},
			"text":{"format":{"type":"text"}},
			"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":0,"cache_creation_input_tokens":0},
				"output_tokens":8,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":18}
		}`, normalizeResponsesIDs(t, body))

		in, _ := usage.InputTokens()
		cached, _ := usage.CachedInputTokens()
		out, _ := usage.OutputTokens()
		reasoning, _ := usage.ReasoningTokens()
		total, _ := usage.TotalTokens()
		require.Equal(t, []uint32{10, 4, 8, 3, 18}, []uint32{in, cached, out, reasoning, total})
	})

	t.Run("function calls with signature and Gemini call id", func(t *testing.T) {
		body, _, model := translateResponsesResponse(t,
			`{"model":"gemini-3-flash-preview","input":"Weather?","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],"temperature":0.2}`,
			`{"candidates":[{"content":{"role":"model","parts":[
				{"functionCall":{"id":"gem-call-1","name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"c2lnLTI="},
				{"functionCall":{"name":"get_weather","args":{"city":"Rome"}}}
			]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"totalTokenCount":9}}`)
		require.Equal(t, "gemini-3-flash-preview", model)
		got := gjson.Parse(body)
		require.Equal(t, "completed", got.Get("status").String())
		require.InDelta(t, 0.2, got.Get("temperature").Float(), 1e-9)
		require.JSONEq(t, `[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]`, got.Get("tools").Raw)
		require.JSONEq(t, `[
			{"id":"rs_ID","type":"reasoning","summary":[],"encrypted_content":"c2lnLTI="},
			{"id":"fc_ID","type":"function_call","call_id":"gem-call-1","name":"get_weather","arguments":"{\"city\":\"Paris\"}","status":"completed"},
			{"id":"fc_ID","type":"function_call","call_id":"call_ID","name":"get_weather","arguments":"{\"city\":\"Rome\"}","status":"completed"}
		]`, normalizeResponsesIDs(t, got.Get("output").Raw))
	})

	for _, tc := range []struct {
		name, gemini, expStatus, expDetails, expError string
	}{
		{
			name:       "max tokens is incomplete",
			gemini:     `{"candidates":[{"content":{"role":"model","parts":[{"text":"Partial"}]},"finishReason":"MAX_TOKENS"}]}`,
			expStatus:  "incomplete",
			expDetails: `{"reason":"max_output_tokens"}`,
		},
		{
			name:       "safety is a content filter",
			gemini:     `{"candidates":[{"finishReason":"SAFETY"}]}`,
			expStatus:  "incomplete",
			expDetails: `{"reason":"content_filter"}`,
		},
		{
			name:       "blocked prompt is a content filter",
			gemini:     `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"usageMetadata":{"promptTokenCount":3,"totalTokenCount":3}}`,
			expStatus:  "incomplete",
			expDetails: `{"reason":"content_filter"}`,
		},
		{
			name:      "malformed function call fails",
			gemini:    `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
			expStatus: "failed",
			expError:  `{"code":"server_error","message":"Gemini finished with reason MALFORMED_FUNCTION_CALL"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _, _ := translateResponsesResponse(t, `{"model":"gemini-2.5-flash","input":"Hi"}`, tc.gemini)
			got := gjson.Parse(body)
			require.Equal(t, tc.expStatus, got.Get("status").String())
			if tc.expDetails != "" {
				require.JSONEq(t, tc.expDetails, got.Get("incomplete_details").Raw)
			} else {
				require.False(t, got.Get("incomplete_details").Exists())
			}
			if tc.expError != "" {
				require.JSONEq(t, tc.expError, got.Get("error").Raw)
			} else {
				require.False(t, got.Get("error").Exists())
			}
		})
	}
}

// responsesSSEEvent is one parsed Responses streaming event.
type responsesSSEEvent struct {
	name string
	data gjson.Result
}

// parseResponsesSSE parses "event: X\ndata: {...}\n\n" blocks and checks the event name matches data.type.
func parseResponsesSSE(t *testing.T, body string) []responsesSSEEvent {
	t.Helper()
	var events []responsesSSEEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		lines := strings.Split(block, "\n")
		require.Len(t, lines, 2, block)
		name, ok := strings.CutPrefix(lines[0], "event: ")
		require.True(t, ok, block)
		data, ok := strings.CutPrefix(lines[1], "data: ")
		require.True(t, ok, block)
		require.True(t, gjson.Valid(data), data)
		parsed := gjson.Parse(data)
		require.Equal(t, name, parsed.Get("type").String())
		events = append(events, responsesSSEEvent{name: name, data: parsed})
	}
	return events
}

// streamResponses feeds the chunks to the translator, one ResponseBody call each, ending the stream on the last.
func streamResponses(t *testing.T, reqBody string, chunks ...string) (string, metrics.TokenUsage) {
	t.Helper()
	tr := NewResponsesOpenAIToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody([]byte(reqBody), parseResponsesRequest(t, reqBody), false)
	require.NoError(t, err)
	headers, err := tr.ResponseHeaders(nil)
	require.NoError(t, err)
	require.Equal(t, []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}, headers)

	var out bytes.Buffer
	var usage metrics.TokenUsage
	for i, chunk := range chunks {
		_, body, u, _, err := tr.ResponseBody(nil, strings.NewReader(chunk), i == len(chunks)-1, nil)
		require.NoError(t, err)
		require.NotNil(t, body)
		out.Write(body)
		usage.Override(u)
	}
	return out.String(), usage
}

func eventNames(events []responsesSSEEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.name
	}
	return names
}

func TestResponsesOpenAIToGCPVertexAI_Streaming(t *testing.T) {
	t.Run("text deltas across split chunks", func(t *testing.T) {
		const chunk1 = `{"responseId":"r1","createTime":"2025-07-11T22:15:44Z","candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}],"modelVersion":"gemini-2.5-flash-001"}`
		const chunk2 = `{"responseId":"r1","candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]}}]}`
		const chunk3 = `{"responseId":"r1","candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"thoughtsTokenCount":2,"cachedContentTokenCount":1,"totalTokenCount":10}}`
		stream := "data: " + chunk1 + "\r\n\r\ndata: " + chunk2 + "\r\n\r\ndata: " + chunk3 + "\r\n\r\n"
		// Split in the middle of the second event to exercise buffering.
		split := strings.Index(stream, `"lo"`)
		body, usage := streamResponses(t, `{"model":"gemini-2.5-flash","input":"Hi","stream":true}`, stream[:split], stream[split:], "")

		events := parseResponsesSSE(t, body)
		require.Equal(t, []string{
			"response.created",
			"response.in_progress",
			"response.output_item.added",
			"response.content_part.added",
			"response.output_text.delta",
			"response.output_text.delta",
			"response.output_text.delta",
			"response.output_text.done",
			"response.content_part.done",
			"response.output_item.done",
			"response.completed",
		}, eventNames(events))
		for i, e := range events {
			require.Equal(t, int64(i), e.data.Get("sequence_number").Int())
		}

		created := events[0].data.Get("response")
		require.Equal(t, "resp_r1", created.Get("id").String())
		require.Equal(t, "in_progress", created.Get("status").String())
		require.Equal(t, int64(1752272144), created.Get("created_at").Int())
		require.JSONEq(t, `[]`, created.Get("output").Raw)
		require.False(t, created.Get("usage").Exists())

		itemID := events[2].data.Get("item.id").String()
		require.True(t, strings.HasPrefix(itemID, "msg_"))
		require.JSONEq(t, `{"type":"output_text","text":"","annotations":[]}`, events[3].data.Get("part").Raw)
		var deltas []string
		for _, e := range events[4:7] {
			require.Equal(t, itemID, e.data.Get("item_id").String())
			require.Equal(t, int64(0), e.data.Get("output_index").Int())
			require.Equal(t, int64(0), e.data.Get("content_index").Int())
			deltas = append(deltas, e.data.Get("delta").String())
		}
		require.Equal(t, []string{"Hel", "lo", "!"}, deltas)
		require.Equal(t, "Hello!", events[7].data.Get("text").String())

		completed := events[10].data.Get("response")
		require.Equal(t, "completed", completed.Get("status").String())
		require.Equal(t, "gemini-2.5-flash-001", completed.Get("model").String())
		require.JSONEq(t, `[{"id":"`+itemID+`","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello!","annotations":[]}]}]`, completed.Get("output").Raw)
		require.JSONEq(t, `{"input_tokens":5,"input_tokens_details":{"cached_tokens":1,"cache_write_tokens":0,"cache_creation_input_tokens":0},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":10}`, completed.Get("usage").Raw)

		in, _ := usage.InputTokens()
		out, _ := usage.OutputTokens()
		reasoning, _ := usage.ReasoningTokens()
		cached, _ := usage.CachedInputTokens()
		require.Equal(t, []uint32{5, 5, 2, 1}, []uint32{in, out, reasoning, cached})
	})

	t.Run("reasoning summary then function call", func(t *testing.T) {
		body, _ := streamResponses(t,
			`{"model":"gemini-3-flash-preview","input":"Weather?","stream":true,"reasoning":{"summary":"auto"},"tools":[{"type":"function","name":"get_weather"}]}`,
			"data: "+`{"responseId":"r2","candidates":[{"content":{"role":"model","parts":[{"text":"Let me ","thought":true}]}}]}`+"\n\n",
			"data: "+`{"responseId":"r2","candidates":[{"content":{"role":"model","parts":[{"text":"check.","thought":true}]}}]}`+"\n\n"+
				"data: "+`{"responseId":"r2","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"c2lnLTM="}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":6,"thoughtsTokenCount":9,"totalTokenCount":19}}`+"\n\n",
			"",
		)
		events := parseResponsesSSE(t, body)
		require.Equal(t, []string{
			"response.created",
			"response.in_progress",
			"response.output_item.added",
			"response.reasoning_summary_part.added",
			"response.reasoning_summary_text.delta",
			"response.reasoning_summary_text.delta",
			"response.reasoning_summary_text.done",
			"response.reasoning_summary_part.done",
			"response.output_item.done",
			"response.output_item.added",
			"response.function_call_arguments.delta",
			"response.function_call_arguments.done",
			"response.output_item.done",
			"response.completed",
		}, eventNames(events))

		require.Equal(t, "reasoning", events[2].data.Get("item.type").String())
		require.Equal(t, "Let me check.", events[6].data.Get("text").String())
		require.JSONEq(t, `{"type":"summary_text","text":"Let me check."}`, events[7].data.Get("part").Raw)
		reasoningDone := events[8].data
		require.Equal(t, int64(0), reasoningDone.Get("output_index").Int())
		require.Equal(t, "c2lnLTM=", reasoningDone.Get("item.encrypted_content").String())

		fcAdded := events[9].data
		require.Equal(t, int64(1), fcAdded.Get("output_index").Int())
		require.Equal(t, "in_progress", fcAdded.Get("item.status").String())
		require.Empty(t, fcAdded.Get("item.arguments").String())
		require.True(t, fcAdded.Get("item.arguments").Exists())
		fcID := fcAdded.Get("item.id").String()
		require.Equal(t, fcID, events[10].data.Get("item_id").String())
		require.JSONEq(t, `{"city":"Paris"}`, events[10].data.Get("delta").String())
		require.Equal(t, "get_weather", events[11].data.Get("name").String())
		require.Equal(t, "completed", events[12].data.Get("item.status").String())

		completed := events[13].data.Get("response")
		require.Len(t, completed.Get("output").Array(), 2)
		require.Equal(t, int64(15), completed.Get("usage.output_tokens").Int())
	})

	t.Run("max tokens ends with response.incomplete", func(t *testing.T) {
		body, _ := streamResponses(t, `{"model":"gemini-2.5-flash","input":"Hi","stream":true,"max_output_tokens":2}`,
			"data: "+`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"MAX_TOKENS"}]}`+"\n\n", "")
		events := parseResponsesSSE(t, body)
		last := events[len(events)-1]
		require.Equal(t, "response.incomplete", last.name)
		require.Equal(t, "max_output_tokens", last.data.Get("response.incomplete_details.reason").String())
		require.Equal(t, int64(2), last.data.Get("response.max_output_tokens").Int())
	})

	t.Run("buffered partial chunk returns an empty body", func(t *testing.T) {
		tr := NewResponsesOpenAIToGCPVertexAITranslator("")
		req := `{"model":"gemini-2.5-flash","input":"Hi","stream":true}`
		_, _, err := tr.RequestBody([]byte(req), parseResponsesRequest(t, req), false)
		require.NoError(t, err)
		_, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(`data: {"candidates":[`), false, nil)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Empty(t, body)
	})

	t.Run("invalid chunk is an error", func(t *testing.T) {
		tr := NewResponsesOpenAIToGCPVertexAITranslator("")
		req := `{"model":"gemini-2.5-flash","input":"Hi","stream":true}`
		_, _, err := tr.RequestBody([]byte(req), parseResponsesRequest(t, req), false)
		require.NoError(t, err)
		_, _, _, _, err = tr.ResponseBody(nil, strings.NewReader("data: {not json\n\n"), false, nil)
		require.ErrorContains(t, err, "error decoding GCP streaming chunk")
	})
}

// TestResponsesOpenAIToGCPVertexAI_SignatureRoundTrip sends the output of one turn back as input and
// checks that Gemini receives the thought signature on the function call part it came from.
func TestResponsesOpenAIToGCPVertexAI_SignatureRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			const gemini = `{"candidates":[{"content":{"role":"model","parts":[
				{"text":"Thinking.","thought":true},
				{"functionCall":{"name":"f","args":{"a":1}},"thoughtSignature":"c2lnLTQ="}
			]},"finishReason":"STOP"}]}`
			var output string
			if stream {
				body, _ := streamResponses(t, `{"model":"gemini-3-flash-preview","input":"Go","stream":true,"tools":[{"type":"function","name":"f"}]}`,
					"data: "+strings.Join(strings.Fields(gemini), "")+"\n\n", "")
				events := parseResponsesSSE(t, body)
				output = events[len(events)-1].data.Get("response.output").Raw
			} else {
				body, _, _ := translateResponsesResponse(t, `{"model":"gemini-3-flash-preview","input":"Go","tools":[{"type":"function","name":"f"}]}`, gemini)
				output = gjson.Get(body, "output").Raw
			}
			callID := gjson.Get(output, `#(type=="function_call").call_id`).String()
			require.NotEmpty(t, callID)

			items := gjson.Parse(output).Array()
			next := `{"model":"gemini-3-flash-preview","tools":[{"type":"function","name":"f"}],"input":[{"role":"user","content":"Go"}`
			for _, item := range items {
				next += "," + item.Raw
			}
			next += `,{"type":"function_call_output","call_id":"` + callID + `","output":"done"}]}`

			_, body := translateResponsesRequest(t, "", next)
			require.JSONEq(t, `[
				{"role":"user","parts":[{"text":"Go"}]},
				{"role":"model","parts":[{"functionCall":{"name":"f","args":{"a":1}},"thoughtSignature":"c2lnLTQ="}]},
				{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"done"}}}]}
			]`, gjson.Get(body, "contents").Raw)
		})
	}
}

func TestResponsesOpenAIToGCPVertexAI_ResponseError(t *testing.T) {
	tr := NewResponsesOpenAIToGCPVertexAITranslator("")
	headers, body, err := tr.ResponseError(map[string]string{statusHeaderName: "400"},
		strings.NewReader(`{"error":{"code":400,"message":"Invalid argument","status":"INVALID_ARGUMENT"}}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"error","error":{"type":"INVALID_ARGUMENT","code":"400","message":"Invalid argument"}}`, string(body))
	require.Equal(t, internalapi.Header{contentTypeHeaderName, jsonContentType}, headers[0])
}
