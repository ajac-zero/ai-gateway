// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestOpenAIToAnthropicTranslator_RequestBody(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model:     "claude-requested",
		MaxTokens: ptr.To(int64(100)),
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
			},
		}},
	}

	tr := NewChatCompletionOpenAIToAnthropicTranslator("gateway/v1", "claude-override")
	headers, body, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.Contains(t, headers, internalapi.Header{pathHeaderName, "/gateway/v1/messages"})
	require.Contains(t, headers, internalapi.Header{anthropicVersionHeaderName, anthropicDefaultVersion})
	require.Equal(t, "claude-override", gjson.GetBytes(body, "model").String())
	require.False(t, gjson.GetBytes(body, anthropicVersionKey).Exists())

	req.Stream = true
	_, body, err = tr.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(body, "stream").Bool())
}

func TestOpenAIToAnthropicTranslator_UsesResponseModel(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model:     "claude-requested",
		MaxTokens: ptr.To(int64(100)),
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
			},
		}},
	}

	tr := NewChatCompletionOpenAIToAnthropicTranslator("v1", "")
	_, _, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)

	response := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-executed","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	_, body, _, model, err := tr.ResponseBody(nil, bytes.NewBufferString(response), true, nil)
	require.NoError(t, err)
	require.Equal(t, "claude-executed", model)
	require.Equal(t, "claude-executed", gjson.GetBytes(body, "model").String())
}

func TestOpenAIToAnthropicTranslator_StreamUsesResponseModel(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model:     "claude-requested",
		Stream:    true,
		MaxTokens: ptr.To(int64(100)),
	}
	tr := NewChatCompletionOpenAIToAnthropicTranslator("v1", "")
	_, _, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)

	event := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-executed\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
	_, _, _, model, err := tr.ResponseBody(nil, bytes.NewBufferString(event), false, nil)
	require.NoError(t, err)
	require.Equal(t, "claude-executed", model)
}

func TestOpenAIToAnthropicTranslator_StreamParserReset(t *testing.T) {
	testStreamParserReset(t, NewChatCompletionOpenAIToAnthropicTranslator("v1", ""))
}

func TestOpenAIToGCPAnthropicTranslator_StreamParserReset(t *testing.T) {
	testStreamParserReset(t, NewChatCompletionOpenAIToGCPAnthropicTranslator("", ""))
}

func testStreamParserReset(t *testing.T, tr OpenAIChatCompletionTranslator) {
	t.Helper()

	streamResponse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-response\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"streamed\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	nonStreamResponse := `{"id":"msg_non_stream","type":"message","role":"assistant","model":"claude-response","content":[{"type":"text","text":"non-streamed"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`

	request := func(stream bool) *openai.ChatCompletionRequest {
		return &openai.ChatCompletionRequest{
			Model:     "claude-requested",
			Stream:    stream,
			MaxTokens: ptr.To(int64(100)),
			Messages: []openai.ChatCompletionMessageParamUnion{{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
				},
			}},
		}
	}

	for _, order := range [][]bool{{true, false}, {false, true}} {
		for _, stream := range order {
			_, _, err := tr.RequestBody(nil, request(stream), false)
			require.NoError(t, err)

			if stream {
				_, body, _, _, err := tr.ResponseBody(nil, bytes.NewBufferString(streamResponse), true, nil)
				require.NoError(t, err)
				require.NotEmpty(t, body)
				require.Contains(t, string(body), `"content":"streamed"`)
				continue
			}

			_, body, usage, _, err := tr.ResponseBody(nil, bytes.NewBufferString(nonStreamResponse), true, nil)
			require.NoError(t, err)
			require.NotEmpty(t, body)
			require.Equal(t, "non-streamed", gjson.GetBytes(body, "choices.0.message.content").String())
			require.Equal(t, int64(7), gjson.GetBytes(body, "usage.prompt_tokens").Int())
			require.Equal(t, int64(3), gjson.GetBytes(body, "usage.completion_tokens").Int())
			inputTokens, ok := usage.InputTokens()
			require.True(t, ok)
			require.Equal(t, uint32(7), inputTokens)
			outputTokens, ok := usage.OutputTokens()
			require.True(t, ok)
			require.Equal(t, uint32(3), outputTokens)
		}
	}
}

func TestOpenAIToAnthropicTranslator_RawError(t *testing.T) {
	tr := NewChatCompletionOpenAIToAnthropicTranslator("v1", "")
	_, body, err := tr.ResponseError(map[string]string{statusHeaderName: "503"}, bytes.NewBufferString("unavailable"))
	require.NoError(t, err)
	require.Equal(t, anthropicBackendError, gjson.GetBytes(body, "error.type").String())
}
