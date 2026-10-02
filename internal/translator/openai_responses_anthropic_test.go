// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

var responsesAnthropicTestTime = time.Unix(1741476542, 0)

func newTestResponsesAnthropicTranslator(t *testing.T, override string) *openAIToAnthropicTranslatorV1Responses {
	t.Helper()
	tr := NewResponsesOpenAIToAnthropicTranslator("v1", override).(*openAIToAnthropicTranslatorV1Responses)
	tr.now = func() time.Time { return responsesAnthropicTestTime }
	return tr
}

// translateAnthropicResponsesRequest parses a raw Responses request and runs RequestBody.
func translateAnthropicResponsesRequest(t *testing.T, tr *openAIToAnthropicTranslatorV1Responses, raw string) ([]internalapi.Header, []byte, error) {
	t.Helper()
	var req openai.ResponseRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	return tr.RequestBody([]byte(raw), &req, false)
}

func headerValue(headers []internalapi.Header, key string) string {
	for _, h := range headers {
		if h.Key() == key {
			return h.Value()
		}
	}
	return ""
}

func TestResponsesAnthropic_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		override string
		request  string
		expBody  string
	}{
		{
			name:    "string input with instructions and sampling",
			request: `{"model":"claude-sonnet-4-5","instructions":"Be brief.","input":"Hi","max_output_tokens":256,"temperature":0.5,"top_p":0.9}`,
			expBody: `{"model":"claude-sonnet-4-5","max_tokens":256,
				"system":[{"type":"text","text":"Be brief."}],
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"temperature":0.5,"top_p":0.9}`,
		},
		{
			name:     "model override and unset max_output_tokens sends max_tokens 0",
			override: "claude-opus-4-7",
			request:  `{"model":"my-alias","input":"Hi"}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":0,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			name: "multi-turn conversation with tool calls",
			request: `{"model":"claude-opus-4-7","max_output_tokens":1000,"input":[
				{"role":"developer","content":"Use metric units."},
				{"role":"user","content":[
					{"type":"input_text","text":"Weather in Paris and Rome?"},
					{"type":"input_image","image_url":"https://example.com/map.png","detail":"low"},
					{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}
				]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking both."}]},
				{"type":"function_call","call_id":"toolu_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},
				{"type":"function_call","call_id":"toolu_2","name":"get_weather","arguments":"{\"city\":\"Rome\"}"},
				{"type":"function_call_output","call_id":"toolu_1","output":"18C"},
				{"type":"function_call_output","call_id":"toolu_2","output":[{"type":"input_text","text":"21C"}]},
				{"role":"user","content":"Which is warmer?"}
			]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":1000,
				"system":[{"type":"text","text":"Use metric units."}],
				"messages":[
					{"role":"user","content":[
						{"type":"text","text":"Weather in Paris and Rome?"},
						{"type":"image","source":{"type":"url","url":"https://example.com/map.png"}},
						{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}
					]},
					{"role":"assistant","content":[
						{"type":"text","text":"Checking both."},
						{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}},
						{"type":"tool_use","id":"toolu_2","name":"get_weather","input":{"city":"Rome"}}
					]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"18C"}]},
						{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"21C"}]},
						{"type":"text","text":"Which is warmer?"}
					]}
				]}`,
		},
		{
			name: "function call without arguments and empty output",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":[
				{"role":"user","content":"ping"},
				{"type":"function_call","call_id":"toolu_1","name":"ping","arguments":""},
				{"type":"function_call_output","call_id":"toolu_1","output":""}
			]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,"messages":[
				{"role":"user","content":[{"type":"text","text":"ping"}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"ping","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1"}]}
			]}`,
		},
		{
			name: "function call output with image and file and assistant refusal",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":[
				{"role":"assistant","content":[{"type":"refusal","refusal":"I cannot."}]},
				{"type":"function_call","call_id":"toolu_1","name":"screenshot","arguments":"{}"},
				{"type":"function_call_output","call_id":"toolu_1","output":[
					{"type":"input_text","text":"Captured."},
					{"type":"input_image","image_url":"https://example.com/shot.png"},
					{"type":"input_file","filename":"r.pdf","file_data":"JVBERi0="}
				]}
			]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,"messages":[
				{"role":"assistant","content":[
					{"type":"text","text":"I cannot."},
					{"type":"tool_use","id":"toolu_1","name":"screenshot","input":{}}
				]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"text","text":"Captured."},
					{"type":"image","source":{"type":"url","url":"https://example.com/shot.png"}},
					{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}
				]}]}
			]}`,
		},
		{
			name: "pdf input file",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":[{"role":"user","content":[
				{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0="},
				{"type":"input_file","file_url":"https://example.com/b.pdf"}
			]}]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,"messages":[{"role":"user","content":[
				{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}},
				{"type":"document","source":{"type":"url","url":"https://example.com/b.pdf"}}
			]}]}`,
		},
		{
			name: "function tools with tool_choice required and no parallel calls",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Weather?","parallel_tool_calls":false,"tool_choice":"required",
				"tools":[{"type":"function","name":"get_weather","description":"Get weather","strict":true,
					"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Weather?"}]}],
				"tools":[{"name":"get_weather","description":"Get weather","strict":true,
					"input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}],
				"tool_choice":{"type":"any","disable_parallel_tool_use":true}}`,
		},
		{
			name: "named function tool_choice",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Weather?","tool_choice":{"type":"function","name":"get_weather"},
				"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{}}}]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Weather?"}]}],
				"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{}}}],
				"tool_choice":{"type":"tool","name":"get_weather"}}`,
		},
		{
			name: "parallel_tool_calls false without tool_choice",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Weather?","parallel_tool_calls":false,
				"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{}}}]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Weather?"}]}],
				"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{}}}],
				"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`,
		},
		{
			name:    "tool_choice auto without tools is omitted",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Hi","tool_choice":"auto"}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			name: "json schema text format",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Hi",
				"text":{"format":{"type":"json_schema","name":"answer","strict":true,
					"schema":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}}}}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"output_config":{"format":{"type":"json_schema",
					"schema":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}}}}`,
		},
		{
			name:    "safety identifier and service tier",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Hi","safety_identifier":"user-hash","user":"ignored","service_tier":"default"}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"metadata":{"user_id":"user-hash"},"service_tier":"standard_only"}`,
		},
		{
			name: "default-valued OpenAI parameters are accepted",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Hi","store":false,"background":false,"truncation":"disabled",
				"include":["reasoning.encrypted_content"],"text":{"format":{"type":"text"},"verbosity":"medium"},"top_logprobs":0,
				"prompt_cache_key":"conv-1","stream_options":{"include_obfuscation":false}}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			name:    "adaptive thinking model with effort and summary",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Hi","reasoning":{"effort":"minimal","summary":"auto"}}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"low"}}`,
		},
		{
			name:    "adaptive thinking model with effort only",
			request: `{"model":"claude-sonnet-5","max_output_tokens":10,"input":"Hi","reasoning":{"effort":"xhigh"}}`,
			expBody: `{"model":"claude-sonnet-5","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`,
		},
		{
			name:    "effort none disables thinking on adaptive model",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":"Hi","reasoning":{"effort":"none"}}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"disabled"}}`,
		},
		{
			name:    "effort none on extended-thinking model sends no thinking",
			request: `{"model":"claude-sonnet-4-5-20250929","max_output_tokens":10,"input":"Hi","reasoning":{"effort":"none"}}`,
			expBody: `{"model":"claude-sonnet-4-5-20250929","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			name:    "extended-thinking model maps effort to budget",
			request: `{"model":"claude-sonnet-4-5-20250929","max_output_tokens":32000,"input":"Hi","reasoning":{"effort":"high"}}`,
			expBody: `{"model":"claude-sonnet-4-5-20250929","max_tokens":32000,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"enabled","budget_tokens":16384}}`,
		},
		{
			// An unset max_tokens is left for Anthropic to reject; the budget must not be clamped to -1.
			name:    "extended-thinking budget is not clamped when max_output_tokens is unset",
			request: `{"model":"claude-sonnet-4-5-20250929","input":"Hi","reasoning":{"effort":"high"}}`,
			expBody: `{"model":"claude-sonnet-4-5-20250929","max_tokens":0,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"enabled","budget_tokens":16384}}`,
		},
		{
			name:    "extended-thinking budget is not clamped when max_output_tokens is 0",
			request: `{"model":"claude-sonnet-4-5-20250929","max_output_tokens":0,"input":"Hi","reasoning":{"effort":"minimal"}}`,
			expBody: `{"model":"claude-sonnet-4-5-20250929","max_tokens":0,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"enabled","budget_tokens":1024}}`,
		},
		{
			name:    "extended-thinking budget is clamped below max_output_tokens",
			request: `{"model":"claude-haiku-4-5","max_output_tokens":2000,"input":"Hi","reasoning":{"effort":"high"}}`,
			expBody: `{"model":"claude-haiku-4-5","max_tokens":2000,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"enabled","budget_tokens":1999}}`,
		},
		{
			name:    "opus 4.5 gets budget and effort",
			request: `{"model":"claude-opus-4-5","max_output_tokens":20000,"input":"Hi","reasoning":{"effort":"medium"}}`,
			expBody: `{"model":"claude-opus-4-5","max_tokens":20000,
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],
				"thinking":{"type":"enabled","budget_tokens":8192},"output_config":{"effort":"medium"}}`,
		},
		{
			name: "reasoning items replay thinking blocks and drop foreign reasoning",
			request: `{"model":"claude-opus-4-7","max_output_tokens":10,"input":[
				{"role":"user","content":"Q1"},
				{"type":"reasoning","id":"rs_openai","summary":[],"encrypted_content":"gAAAAopenai-blob"},
				{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Let me think."}],"encrypted_content":"anthropic.thinking:sig-123"},
				{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"anthropic.redacted_thinking:opaque-data"},
				{"type":"function_call","call_id":"toolu_9","name":"lookup","arguments":"{}"},
				{"type":"function_call_output","call_id":"toolu_9","output":"ok"}
			]}`,
			expBody: `{"model":"claude-opus-4-7","max_tokens":10,"messages":[
				{"role":"user","content":[{"type":"text","text":"Q1"}]},
				{"role":"assistant","content":[
					{"type":"thinking","signature":"sig-123","thinking":"Let me think."},
					{"type":"redacted_thinking","data":"opaque-data"},
					{"type":"tool_use","id":"toolu_9","name":"lookup","input":{}}
				]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_9","content":[{"type":"text","text":"ok"}]}]}
			]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestResponsesAnthropicTranslator(t, tc.override)
			headers, body, err := translateAnthropicResponsesRequest(t, tr, tc.request)
			require.NoError(t, err)
			require.JSONEq(t, tc.expBody, string(body))
			require.Equal(t, "/v1/messages", headerValue(headers, pathHeaderName))
			require.Equal(t, "2023-06-01", headerValue(headers, "anthropic-version"))
			require.Equal(t, len(body), mustAtoi(t, headerValue(headers, contentLengthHeaderName)))
		})
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	require.NoError(t, json.Unmarshal([]byte(s), &n))
	return n
}

func TestResponsesAnthropic_RequestBody_Stream(t *testing.T) {
	tr := newTestResponsesAnthropicTranslator(t, "")
	_, body, err := translateAnthropicResponsesRequest(t, tr, `{"model":"claude-opus-4-7","input":"Hi","max_output_tokens":5,"stream":true}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"claude-opus-4-7","max_tokens":5,"stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`, string(body))
	headers, err := tr.ResponseHeaders(nil)
	require.NoError(t, err)
	require.Equal(t, "text/event-stream", headerValue(headers, contentTypeHeaderName))

	// A retry re-runs RequestBody and must reset the streaming state.
	_, _, err = translateAnthropicResponsesRequest(t, tr, `{"model":"claude-opus-4-7","input":"Hi","max_output_tokens":5}`)
	require.NoError(t, err)
	headers, err = tr.ResponseHeaders(nil)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestResponsesAnthropic_RequestBody_GCP(t *testing.T) {
	for _, tc := range []struct {
		name, request, expPath, expBody string
	}{
		{
			name:    "non-streaming",
			request: `{"model":"claude-opus-4-7","input":"Hi","max_output_tokens":5}`,
			expPath: "publishers/anthropic/models/claude-opus-4-7:rawPredict",
			expBody: `{"max_tokens":5,"anthropic_version":"vertex-2023-10-16",
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			name:    "streaming",
			request: `{"model":"claude-opus-4-7","input":"Hi","max_output_tokens":5,"stream":true}`,
			expPath: "publishers/anthropic/models/claude-opus-4-7:streamRawPredict",
			expBody: `{"max_tokens":5,"stream":true,"anthropic_version":"vertex-2023-10-16",
				"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewResponsesOpenAIToGCPAnthropicTranslator("", "").(*openAIToAnthropicTranslatorV1Responses)
			headers, body, err := translateAnthropicResponsesRequest(t, tr, tc.request)
			require.NoError(t, err)
			require.JSONEq(t, tc.expBody, string(body))
			require.Equal(t, tc.expPath, headerValue(headers, pathHeaderName))
			// Vertex takes the version from the body, not from the anthropic-version header.
			require.Empty(t, headerValue(headers, "anthropic-version"))
		})
	}
}

func TestResponsesAnthropic_RequestBody_Unsupported(t *testing.T) {
	for _, tc := range []struct {
		name, request, expErr string
	}{
		{"previous_response_id", `{"previous_response_id":"resp_1"}`, "previous_response_id"},
		{"conversation", `{"conversation":"conv_1"}`, "conversation"},
		{"store", `{"store":true}`, "store=true"},
		{"background", `{"background":true}`, "background=true"},
		{"prompt template", `{"prompt":{"id":"pmpt_1"}}`, "prompt"},
		{"context management", `{"context_management":[{"type":"compaction"}]}`, "context_management"},
		{"truncation auto", `{"truncation":"auto"}`, `truncation="auto"`},
		{"top_logprobs", `{"top_logprobs":3}`, "top_logprobs"},
		{"presence_penalty", `{"presence_penalty":0.5}`, "presence_penalty"},
		{"frequency_penalty", `{"frequency_penalty":0.5}`, "frequency_penalty"},
		{"priority service tier", `{"service_tier":"priority"}`, `service_tier="priority"`},
		{"24h cache retention", `{"prompt_cache_retention":"24h"}`, `prompt_cache_retention="24h"`},
		{"verbosity", `{"text":{"verbosity":"low"}}`, `text.verbosity="low"`},
		{"json_object format", `{"text":{"format":{"type":"json_object"}}}`, "json_object"},
		{"logprobs include", `{"include":["message.output_text.logprobs"]}`, "message.output_text.logprobs"},
		{"web search tool", `{"tools":[{"type":"web_search"}]}`, `tool type "web_search"`},
		{"file search tool", `{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}]}`, `tool type "file_search"`},
		{"mcp tool", `{"tools":[{"type":"mcp","server_label":"x","server_url":"https://x"}]}`, `tool type "mcp"`},
		{"code interpreter tool", `{"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`, `tool type "code_interpreter"`},
		{"custom tool", `{"tools":[{"type":"custom","name":"x"}]}`, `tool type "custom"`},
		{"allowed_tools choice", `{"tools":[{"type":"function","name":"f"}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[]}}`, "allowed_tools"},
		{"hosted tool choice", `{"tools":[{"type":"function","name":"f"}],"tool_choice":{"type":"file_search"}}`, "non-function tools"},
		{"required tool choice without tools", `{"tool_choice":"required"}`, "requires at least one tool"},
		{"item reference input", `{"input":[{"type":"item_reference","id":"msg_1"}]}`, `input item type "item_reference"`},
		{"web search call input", `{"input":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}]}`, `input item type "web_search_call"`},
		{"image file id", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`, "file_id"},
		{"non-pdf file", `{"input":[{"role":"user","content":[{"type":"input_file","file_data":"data:text/csv;base64,YQ==","filename":"a.csv"}]}]}`, `"text/csv"`},
		{"image in system message", `{"input":[{"role":"system","content":[{"type":"input_image","image_url":"https://x/y.png"}]}]}`, "system messages may contain only text"},
		{"invalid function call arguments", `{"input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{oops"}]}`, "not valid JSON"},
		{"temperature above 1", `{"temperature":1.5}`, "temperature 1.50 is not supported"},
		{"unknown effort", `{"reasoning":{"effort":"extreme"}}`, "unsupported reasoning effort"},
		{"reasoning budget exceeds max tokens", `{"model":"claude-sonnet-4-5","max_output_tokens":1000,"reasoning":{"effort":"low"}}`, "max_output_tokens must be greater than 1024"},
		{"reasoning budget at minimum max tokens", `{"model":"claude-sonnet-4-5","max_output_tokens":1024,"reasoning":{"effort":"high"}}`, "max_output_tokens must be greater than 1024"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Complete the case into a minimal valid request.
			request := tc.request
			if !strings.Contains(tc.request, `"model"`) {
				request = `{"model":"claude-opus-4-7",` + request[1:]
			}
			if !strings.Contains(tc.request, `"input"`) {
				request = request[:len(request)-1] + `,"input":"Hi"}`
			}
			tr := newTestResponsesAnthropicTranslator(t, "")
			_, _, err := translateAnthropicResponsesRequest(t, tr, request)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
			require.ErrorContains(t, err, tc.expErr)
		})
	}
}

func TestResponsesAnthropic_ResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name, request, anthropicResponse, expResponse string
		expUsage                                      metrics.TokenUsage
	}{
		{
			name:    "text, thinking and tool use with cache usage",
			request: `{"model":"claude-opus-4-7","input":"Weather?","instructions":"Be brief.","max_output_tokens":500,"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],"metadata":{"k":"v"}}`,
			anthropicResponse: `{"id":"msg_01ABC","type":"message","role":"assistant","model":"claude-opus-4-7-20260101",
				"content":[
					{"type":"thinking","thinking":"User wants weather.","signature":"sig=="},
					{"type":"redacted_thinking","data":"redacted=="},
					{"type":"text","text":"Let me check."},
					{"type":"tool_use","id":"toolu_01X","name":"get_weather","input":{"city":"Paris","units":"c"}}
				],
				"stop_reason":"tool_use","stop_sequence":null,
				"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":40,"output_tokens_details":{"thinking_tokens":15}}}`,
			expResponse: `{"id":"resp_01ABC","object":"response","created_at":1741476542,"completed_at":1741476542,"status":"completed",
				"model":"claude-opus-4-7-20260101","instructions":"Be brief.","max_output_tokens":500,"metadata":{"k":"v"},
				"output":[
					{"id":"rs_01ABC_0","type":"reasoning","summary":[{"type":"summary_text","text":"User wants weather."}],"encrypted_content":"anthropic.thinking:sig=="},
					{"id":"rs_01ABC_1","type":"reasoning","summary":[],"encrypted_content":"anthropic.redacted_thinking:redacted=="},
					{"id":"msg_01ABC_2","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Let me check.","annotations":[]}]},
					{"id":"fc_toolu_01X","type":"function_call","call_id":"toolu_01X","name":"get_weather","arguments":"{\"city\":\"Paris\",\"units\":\"c\"}","status":"completed"}
				],
				"parallel_tool_calls":true,"temperature":1,"top_p":1,"tool_choice":"auto","truncation":"disabled",
				"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],
				"text":{"format":{"type":"text"}},"store":false,"background":false,
				"usage":{"input_tokens":60,"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30,"cache_creation_input_tokens":30},
					"output_tokens":40,"output_tokens_details":{"reasoning_tokens":15},"total_tokens":100}}`,
			expUsage: func() metrics.TokenUsage {
				var u metrics.TokenUsage
				u.SetInputTokens(60)
				u.SetCachedInputTokens(20)
				u.SetCacheCreationInputTokens(30)
				u.SetOutputTokens(40)
				u.SetTotalTokens(100)
				u.SetReasoningTokens(15)
				return u
			}(),
		},
		{
			name:    "max_tokens stop is incomplete",
			request: `{"model":"claude-opus-4-7","input":"Hi","max_output_tokens":2,"temperature":0.2}`,
			anthropicResponse: `{"id":"msg_02","type":"message","role":"assistant","model":"claude-opus-4-7",
				"content":[{"type":"text","text":"Hel"}],"stop_reason":"max_tokens","usage":{"input_tokens":3,"output_tokens":2}}`,
			expResponse: `{"id":"resp_02","object":"response","created_at":1741476542,"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
				"model":"claude-opus-4-7","max_output_tokens":2,
				"output":[{"id":"msg_02_0","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hel","annotations":[]}]}],
				"parallel_tool_calls":true,"temperature":0.2,"top_p":1,"tool_choice":"auto","truncation":"disabled",
				"text":{"format":{"type":"text"}},"store":false,"background":false,
				"usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0,"cache_creation_input_tokens":0},
					"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":5}}`,
			expUsage: func() metrics.TokenUsage {
				var u metrics.TokenUsage
				u.SetInputTokens(3)
				u.SetCachedInputTokens(0)
				u.SetCacheCreationInputTokens(0)
				u.SetOutputTokens(2)
				u.SetTotalTokens(5)
				u.SetReasoningTokens(0)
				return u
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestResponsesAnthropicTranslator(t, "")
			_, _, err := translateAnthropicResponsesRequest(t, tr, tc.request)
			require.NoError(t, err)
			headers, body, usage, model, err := tr.ResponseBody(nil, strings.NewReader(tc.anthropicResponse), true, nil)
			require.NoError(t, err)
			require.JSONEq(t, tc.expResponse, string(body))
			require.Equal(t, tc.expUsage, usage)
			var parsed openai.Response
			require.NoError(t, json.Unmarshal(body, &parsed))
			require.Equal(t, parsed.Model, model)
			require.Equal(t, len(body), mustAtoi(t, headerValue(headers, contentLengthHeaderName)))
		})
	}
}

func TestResponsesAnthropic_ResponseBody_Refusal(t *testing.T) {
	tr := newTestResponsesAnthropicTranslator(t, "")
	_, _, err := translateAnthropicResponsesRequest(t, tr, `{"model":"claude-opus-4-7","input":"Hi"}`)
	require.NoError(t, err)
	_, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(
		`{"id":"msg_3","type":"message","role":"assistant","model":"claude-opus-4-7","content":[],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":0}}`), true, nil)
	require.NoError(t, err)
	var resp openai.Response
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, "incomplete", resp.Status)
	require.Equal(t, "content_filter", resp.IncompleteDetails.Reason)
	require.Nil(t, resp.CompletedAt)
}

// anthropicSSE renders Anthropic SSE events the way the Messages API sends them.
func anthropicSSE(events ...string) string {
	var b strings.Builder
	for _, data := range events {
		var typed struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &typed); err != nil {
			panic(err)
		}
		b.WriteString("event: " + typed.Type + "\ndata: " + data + "\n\n")
	}
	return b.String()
}

type anthropicResponsesSSEEvent struct {
	name string
	data map[string]any
}

// parseAnthropicResponsesSSE splits translated output into events and checks that the SSE event name
// matches the JSON type and that sequence numbers count up from zero.
func parseAnthropicResponsesSSE(t *testing.T, raw []byte) []anthropicResponsesSSEEvent {
	t.Helper()
	var events []anthropicResponsesSSEEvent
	for block := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n\n") {
		lines := strings.Split(block, "\n")
		require.Len(t, lines, 2, "event %q", block)
		name, ok := strings.CutPrefix(lines[0], "event: ")
		require.True(t, ok, lines[0])
		data, ok := strings.CutPrefix(lines[1], "data: ")
		require.True(t, ok, lines[1])
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &parsed))
		require.Equal(t, name, parsed["type"])
		require.EqualValues(t, len(events), parsed["sequence_number"])
		events = append(events, anthropicResponsesSSEEvent{name: name, data: parsed})
	}
	return events
}

func anthropicEventNames(events []anthropicResponsesSSEEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.name
	}
	return names
}

func TestResponsesAnthropic_Stream_TextAndToolUse(t *testing.T) {
	stream := anthropicSSE(
		`{"type":"message_start","message":{"id":"msg_01S","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"cache_read_input_tokens":100,"cache_creation_input_tokens":0,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Okay,"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" checking."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01T","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\": "}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":89}}`,
		`{"type":"message_stop"}`,
	)

	// Feed the stream in small chunks to exercise buffering across ResponseBody calls.
	tr := newTestResponsesAnthropicTranslator(t, "")
	_, _, err := translateAnthropicResponsesRequest(t, tr, `{"model":"claude-opus-4-7","input":"Weather?","stream":true,"tools":[{"type":"function","name":"get_weather"}]}`)
	require.NoError(t, err)
	var out []byte
	var usage metrics.TokenUsage
	for i := 0; i < len(stream); i += 7 {
		end := min(i+7, len(stream))
		_, chunk, u, model, bodyErr := tr.ResponseBody(nil, strings.NewReader(stream[i:end]), end == len(stream), nil)
		require.NoError(t, bodyErr)
		require.Equal(t, "claude-opus-4-7", model)
		out = append(out, chunk...)
		usage = u
	}

	events := parseAnthropicResponsesSSE(t, out)
	require.Equal(t, []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	}, anthropicEventNames(events))

	created := events[0].data["response"].(map[string]any)
	require.Equal(t, "resp_01S", created["id"])
	require.Equal(t, "in_progress", created["status"])
	require.Empty(t, created["output"])

	textAdded, _ := json.Marshal(events[2].data)
	require.JSONEq(t, `{"type":"response.output_item.added","sequence_number":2,"output_index":0,
		"item":{"id":"msg_01S_0","type":"message","status":"in_progress","role":"assistant","content":[]}}`, string(textAdded))
	partAdded, _ := json.Marshal(events[3].data)
	require.JSONEq(t, `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_01S_0","output_index":0,"content_index":0,
		"part":{"type":"output_text","text":"","annotations":[]}}`, string(partAdded))
	delta, _ := json.Marshal(events[4].data)
	require.JSONEq(t, `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_01S_0","output_index":0,"content_index":0,"delta":"Okay,","logprobs":[]}`, string(delta))
	textDone, _ := json.Marshal(events[6].data)
	require.JSONEq(t, `{"type":"response.output_text.done","sequence_number":6,"item_id":"msg_01S_0","output_index":0,"content_index":0,"text":"Okay, checking.","logprobs":[]}`, string(textDone))

	fcAdded, _ := json.Marshal(events[9].data)
	require.JSONEq(t, `{"type":"response.output_item.added","sequence_number":9,"output_index":1,
		"item":{"id":"fc_toolu_01T","type":"function_call","call_id":"toolu_01T","name":"get_weather","arguments":"","status":"in_progress"}}`, string(fcAdded))
	argsDone, _ := json.Marshal(events[12].data)
	require.JSONEq(t, `{"type":"response.function_call_arguments.done","sequence_number":12,"item_id":"fc_toolu_01T","output_index":1,
		"name":"get_weather","arguments":"{\"city\": \"Paris\"}"}`, string(argsDone))

	completed := events[14].data["response"].(map[string]any)
	require.Equal(t, "completed", completed["status"])
	completedOutput, _ := json.Marshal(completed["output"])
	require.JSONEq(t, `[
		{"id":"msg_01S_0","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Okay, checking.","annotations":[]}]},
		{"id":"fc_toolu_01T","type":"function_call","call_id":"toolu_01T","name":"get_weather","arguments":"{\"city\": \"Paris\"}","status":"completed"}
	]`, string(completedOutput))
	completedUsage, _ := json.Marshal(completed["usage"])
	require.JSONEq(t, `{"input_tokens":125,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":0,"cache_creation_input_tokens":0},
		"output_tokens":89,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":214}`, string(completedUsage))

	in, _ := usage.InputTokens()
	cached, _ := usage.CachedInputTokens()
	outTokens, _ := usage.OutputTokens()
	total, _ := usage.TotalTokens()
	require.Equal(t, []uint32{125, 100, 89, 214}, []uint32{in, cached, outTokens, total})

	// Feeding the whole stream at once must produce identical events.
	tr2 := newTestResponsesAnthropicTranslator(t, "")
	_, _, err = translateAnthropicResponsesRequest(t, tr2, `{"model":"claude-opus-4-7","input":"Weather?","stream":true,"tools":[{"type":"function","name":"get_weather"}]}`)
	require.NoError(t, err)
	_, whole, _, _, err := tr2.ResponseBody(nil, strings.NewReader(stream), true, nil)
	require.NoError(t, err)
	require.Equal(t, anthropicEventNames(events), anthropicEventNames(parseAnthropicResponsesSSE(t, whole)))
	require.Equal(t, normalizeCompletedAt(out), normalizeCompletedAt(whole))
}

// normalizeCompletedAt removes the only field that depends on when the stream finished.
func normalizeCompletedAt(b []byte) string {
	s := string(b)
	if i := strings.Index(s, `"completed_at":`); i >= 0 {
		j := i + len(`"completed_at":`)
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		s = s[:i] + s[j:]
	}
	return s
}

func TestResponsesAnthropic_Stream_ThinkingAndCumulativeUsage(t *testing.T) {
	stream := anthropicSSE(
		`{"type":"message_start","message":{"id":"msg_T","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Think "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hard."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQB"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"REDACTED"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"42"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":7,"output_tokens":50,"output_tokens_details":{"thinking_tokens":30}}}`,
		`{"type":"message_stop"}`,
	)
	tr := newTestResponsesAnthropicTranslator(t, "")
	_, _, err := translateAnthropicResponsesRequest(t, tr, `{"model":"claude-opus-4-7","input":"Q","stream":true,"reasoning":{"effort":"high","summary":"auto"}}`)
	require.NoError(t, err)
	_, out, usage, _, err := tr.ResponseBody(nil, strings.NewReader(stream), true, nil)
	require.NoError(t, err)

	events := parseAnthropicResponsesSSE(t, out)
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
		"response.output_item.done",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.incomplete",
	}, anthropicEventNames(events))

	summaryDone, _ := json.Marshal(events[7].data)
	require.JSONEq(t, `{"type":"response.reasoning_summary_part.done","sequence_number":7,"item_id":"rs_T_0","output_index":0,"summary_index":0,
		"part":{"type":"summary_text","text":"Think hard."}}`, string(summaryDone))
	reasoningDone, _ := json.Marshal(events[8].data["item"])
	require.JSONEq(t, `{"id":"rs_T_0","type":"reasoning","summary":[{"type":"summary_text","text":"Think hard."}],"encrypted_content":"anthropic.thinking:EqQB"}`, string(reasoningDone))
	redactedDone, _ := json.Marshal(events[10].data["item"])
	require.JSONEq(t, `{"id":"rs_T_1","type":"reasoning","summary":[],"encrypted_content":"anthropic.redacted_thinking:REDACTED"}`, string(redactedDone))

	final := events[17].data["response"].(map[string]any)
	require.Equal(t, "incomplete", final["status"])
	require.Equal(t, map[string]any{"reason": "max_output_tokens"}, final["incomplete_details"])
	finalUsage, _ := json.Marshal(final["usage"])
	require.JSONEq(t, `{"input_tokens":22,"input_tokens_details":{"cached_tokens":5,"cache_write_tokens":7,"cache_creation_input_tokens":7},
		"output_tokens":50,"output_tokens_details":{"reasoning_tokens":30},"total_tokens":72}`, string(finalUsage))

	in, _ := usage.InputTokens()
	cacheWrite, _ := usage.CacheCreationInputTokens()
	reasoning, _ := usage.ReasoningTokens()
	require.Equal(t, []uint32{22, 7, 30}, []uint32{in, cacheWrite, reasoning})

	// The reasoning items round-trip into the next request as the original thinking blocks.
	var finalResp openai.Response
	raw, _ := json.Marshal(final)
	require.NoError(t, json.Unmarshal(raw, &finalResp))
	next := openai.ResponseRequest{Model: "claude-opus-4-7", Input: openai.ResponseNewParamsInputUnion{OfInputItemList: []openai.ResponseInputItemUnionParam{
		{OfReasoning: finalResp.Output[0].OfReasoning},
		{OfReasoning: finalResp.Output[1].OfReasoning},
		{OfOutputMessage: finalResp.Output[2].OfOutputMessage},
	}}}
	_, body, err := tr.RequestBody(nil, &next, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"claude-opus-4-7","max_tokens":0,"messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":"Think hard.","signature":"EqQB"},
		{"type":"redacted_thinking","data":"REDACTED"},
		{"type":"text","text":"42"}
	]}]}`, string(body))
}

func TestResponsesAnthropic_Stream_ToolInputInBlockStart(t *testing.T) {
	stream := anthropicSSE(
		`{"type":"message_start","message":{"id":"msg_I","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":4,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_I","name":"lookup","input":{"q":"x"}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
	tr := newTestResponsesAnthropicTranslator(t, "")
	_, _, err := translateAnthropicResponsesRequest(t, tr, `{"model":"claude-opus-4-7","input":"Q","stream":true}`)
	require.NoError(t, err)
	_, out, _, _, err := tr.ResponseBody(nil, strings.NewReader(stream), true, nil)
	require.NoError(t, err)
	events := parseAnthropicResponsesSSE(t, out)
	require.Equal(t, []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.completed",
	}, anthropicEventNames(events))
	require.JSONEq(t, `{"q":"x"}`, events[4].data["arguments"].(string))
}

func TestResponsesAnthropic_Stream_Failures(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"msg_E","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":4,"output_tokens":1}}}`
	for _, tc := range []struct {
		name, stream, expCode, expMessage string
	}{
		{
			name:       "error event",
			stream:     anthropicSSE(start, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
			expCode:    "server_error",
			expMessage: "Overloaded",
		},
		{
			name:       "rate limit error event",
			stream:     anthropicSSE(start, `{"type":"error","error":{"type":"rate_limit_error","message":"Slow down"}}`),
			expCode:    "rate_limit_exceeded",
			expMessage: "Slow down",
		},
		{
			name:       "stream ends before message_stop",
			stream:     anthropicSSE(start, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			expCode:    "server_error",
			expMessage: "the Anthropic stream ended before the message was complete",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestResponsesAnthropicTranslator(t, "")
			_, _, err := translateAnthropicResponsesRequest(t, tr, `{"model":"claude-opus-4-7","input":"Q","stream":true}`)
			require.NoError(t, err)
			_, out, usage, _, err := tr.ResponseBody(nil, strings.NewReader(tc.stream), true, nil)
			require.NoError(t, err)
			events := parseAnthropicResponsesSSE(t, out)
			last := events[len(events)-1]
			require.Equal(t, "response.failed", last.name)
			resp := last.data["response"].(map[string]any)
			require.Equal(t, "failed", resp["status"])
			require.Equal(t, map[string]any{"code": tc.expCode, "message": tc.expMessage}, resp["error"])
			in, _ := usage.InputTokens()
			require.Equal(t, uint32(4), in)
			require.Equal(t, 1, bytes.Count(out, []byte("response.failed\n")), "exactly one terminal event")
		})
	}
}

func TestResponsesAnthropic_ResponseError(t *testing.T) {
	tr := newTestResponsesAnthropicTranslator(t, "")
	headers, body, err := tr.ResponseError(
		map[string]string{statusHeaderName: "429", contentTypeHeaderName: "application/json"},
		strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`),
	)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"error","error":{"type":"rate_limit_error","code":"429",
		"message":"Number of request tokens has exceeded your per-minute rate limit"}}`, string(body))
	require.Equal(t, "application/json", headerValue(headers, contentTypeHeaderName))

	_, body, err = tr.ResponseError(map[string]string{statusHeaderName: "503"}, strings.NewReader("upstream unavailable"))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"error","error":{"type":"AnthropicBackendError","code":"503","message":"upstream unavailable"}}`, string(body))
}
