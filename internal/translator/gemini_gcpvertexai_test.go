// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package translator

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
)

func TestGeminiToGCPVertexAIRequest(t *testing.T) {
	tr := NewGeminiToGCPVertexAITranslator("gemini-2.5-flash", false)
	raw := []byte(`{"contents":[{"parts":[{"functionResponse":{"id":"call-1","name":"weather","response":{"ok":true}}},{"thoughtSignature":"opaque-value"}]}],"cachedContent":"cachedContents/1","labels":{"team":"gateway"}}`)
	headers, body, err := tr.RequestBody(raw, &gcp.NativeGenerateContentRequest{Raw: raw}, false)
	require.NoError(t, err)
	require.Equal(t, "publishers/google/models/gemini-2.5-flash:generateContent", headers[0].Value())
	require.NotContains(t, string(body), `"id":"call-1"`)
	require.Contains(t, string(body), `"thoughtSignature":"opaque-value"`)
	require.Contains(t, string(body), `"cachedContent":"cachedContents/1"`)
	require.Contains(t, string(body), `"labels":{"team":"gateway"}`)

	_, retryBody, err := tr.RequestBody(raw, &gcp.NativeGenerateContentRequest{Raw: raw}, true)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(retryBody))
}

func TestGeminiToGCPVertexAIResponse(t *testing.T) {
	tr := NewGeminiToGCPVertexAITranslator("gemini-2.5-flash", false)
	raw := []byte(`{"modelVersion":"gemini-2.5-flash-001","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":2,"cachedContentTokenCount":3,"totalTokenCount":16}}`)
	_, body, usage, model, err := tr.ResponseBody(nil, bytes.NewReader(raw), true, nil)
	require.NoError(t, err)
	require.Equal(t, raw, body)
	require.Equal(t, "gemini-2.5-flash-001", model)
	in, ok := usage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), in)
	reasoning, ok := usage.ReasoningTokens()
	require.True(t, ok)
	require.Equal(t, uint32(2), reasoning)
}

func TestGeminiToGCPVertexAIStreamingUsage(t *testing.T) {
	tr := NewGeminiToGCPVertexAITranslator("gemini-2.5-flash", true)
	raw := []byte("data: {\"candidates\":[]}\n\ndata: {\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":2,\"totalTokenCount\":9}}\n\n")
	_, body, usage, _, err := tr.ResponseBody(nil, bytes.NewReader(raw), true, nil)
	require.NoError(t, err)
	require.Equal(t, raw, body)
	total, ok := usage.TotalTokens()
	require.True(t, ok)
	require.Equal(t, uint32(9), total)
}
