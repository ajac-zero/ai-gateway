// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// TestParseDataURI tests the parseDataURI function with various inputs.
func TestParseDataURI(t *testing.T) {
	tests := []struct {
		name          string
		uri           string
		wantType      string
		wantData      []byte
		expectErr     bool
		expectedError string
	}{
		{
			name:      "Valid JPEG Data URI",
			uri:       "data:image/jpeg;base64,dGVzdF9kYXRh", // "test_data" in base64.
			wantType:  "image/jpeg",
			wantData:  []byte("test_data"),
			expectErr: false,
		},
		{
			name:      "Valid PNG Data URI",
			uri:       "data:image/png;base64,dGVzdF9wbmc=", // "test_png" in base64.
			wantType:  "image/png",
			wantData:  []byte("test_png"),
			expectErr: false,
		},
		{
			name:          "Invalid URI Format",
			uri:           "not-a-data-uri",
			expectErr:     true,
			expectedError: "data uri does not have a valid format",
		},
		{
			name:          "Malformed Base64",
			uri:           "data:image/jpeg;base64,invalid-base64-string",
			expectErr:     true,
			expectedError: "illegal base64 data at input byte 7",
		},
		{
			name:      "Data URI without base64 encoding specified",
			uri:       "data:text/plain,SGVsbG8sIFdvcmxkIQ==",
			wantType:  "text/plain",
			wantData:  []byte("Hello, World!"),
			expectErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contentType, data, err := parseDataURI(tc.uri)

			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				require.Nil(t, data)
				require.Empty(t, contentType)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantType, contentType)
				require.Equal(t, tc.wantData, data)
			}
		})
	}
}

// TestSystemMsgToDeveloperMsg tests the systemMsgToDeveloperMsg function.
func TestSerializeOpenAIChatCompletionChunk_ReasoningContent(t *testing.T) {
	tests := []struct {
		name              string
		reasoningContent  *openai.StreamReasoningContent
		wantReasoningText string
		wantSignature     string
	}{
		{
			name:             "signature only",
			reasoningContent: &openai.StreamReasoningContent{Signature: "signature"},
			wantSignature:    "signature",
		},
		{
			name:              "text only",
			reasoningContent:  &openai.StreamReasoningContent{Text: "thinking"},
			wantReasoningText: "thinking",
		},
		{
			name:              "text and signature",
			reasoningContent:  &openai.StreamReasoningContent{Text: "thinking", Signature: "signature"},
			wantReasoningText: "thinking",
			wantSignature:     "signature",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunk := &openai.ChatCompletionResponseChunk{Choices: []openai.ChatCompletionResponseChunkChoice{{
				Delta: &openai.ChatCompletionResponseChunkChoiceDelta{ReasoningContent: tc.reasoningContent},
			}}}
			var serialized []byte
			require.NoError(t, serializeOpenAIChatCompletionChunk(chunk, &serialized))

			var data map[string]any
			require.NoError(t, json.Unmarshal(bytes.TrimSuffix(bytes.TrimPrefix(serialized, sseDataPrefix), []byte("\n\n")), &data))
			delta := data["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
			if tc.wantReasoningText == "" {
				_, ok := delta["reasoning_content"]
				require.False(t, ok)
			} else {
				require.Equal(t, tc.wantReasoningText, delta["reasoning_content"])
			}
			if tc.wantSignature == "" {
				_, ok := delta["thinking_blocks"]
				require.False(t, ok)
			} else {
				blocks := delta["thinking_blocks"].([]any)
				require.Equal(t, map[string]any{"type": "thinking", "signature": tc.wantSignature}, blocks[0])
			}
		})
	}
}

func TestSystemMsgToDeveloperMsg(t *testing.T) {
	systemMsg := openai.ChatCompletionSystemMessageParam{
		Name:    "test-system",
		Content: openai.ContentUnion{Value: "You are a helpful assistant."},
	}
	developerMsg := systemMsgToDeveloperMsg(systemMsg)
	require.Equal(t, "test-system", developerMsg.Name)
	require.Equal(t, openai.ChatMessageRoleDeveloper, developerMsg.Role)
	require.Equal(t, openai.ContentUnion{Value: "You are a helpful assistant."}, developerMsg.Content)
}
