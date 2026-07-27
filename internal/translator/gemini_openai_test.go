// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package translator

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
)

func TestGeminiToOpenAIRequest(t *testing.T) {
	raw := []byte(`{"systemInstruction":{"parts":[{"text":"Be concise."}]},"contents":[{"role":"user","parts":[{"text":"Hello "},{"text":"world"}]},{"role":"model","parts":[{"text":"Hi"}]}],"generationConfig":{"temperature":0.2,"topP":0.9,"maxOutputTokens":128}}`)
	tr := NewGeminiToOpenAITranslator("v1", "gpt-4o", false)
	headers, body, err := tr.RequestBody(raw, &gcp.NativeGenerateContentRequest{Raw: raw}, false)
	require.NoError(t, err)
	require.Equal(t, "/v1/chat/completions", headers[0].Value())
	require.JSONEq(t, `{"model":"gpt-4o","stream":false,"messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Hello world"},{"role":"assistant","content":"Hi"}],"temperature":0.2,"top_p":0.9,"max_tokens":128}`, string(body))
}

func TestGeminiToOpenAIResponse(t *testing.T) {
	tr := NewGeminiToOpenAITranslator("v1", "gpt-4o", false)
	raw := []byte(`{"model":"gpt-4o-2024-08-06","choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	_, body, usage, model, err := tr.ResponseBody(nil, bytes.NewReader(raw), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-2024-08-06", model)
	require.JSONEq(t, `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"modelVersion":"gpt-4o-2024-08-06","usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`, string(body))
	total, ok := usage.TotalTokens()
	require.True(t, ok)
	require.Equal(t, uint32(6), total)
}

func TestGeminiToOpenAIStreamingResponse(t *testing.T) {
	tr := NewGeminiToOpenAITranslator("v1", "gpt-4o", true)
	raw := []byte("data: {\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	_, body, _, _, err := tr.ResponseBody(nil, bytes.NewReader(raw), true, nil)
	require.NoError(t, err)
	payload := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("data: ")))
	require.JSONEq(t, `{"candidates":[{"index":0,"content":{"parts":[{"text":"Hi"}],"role":"model"}}],"modelVersion":"gpt-4o"}`, string(payload))
}
