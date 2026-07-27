// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package translator

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
)

func TestGeminiToGoogleAIStudio(t *testing.T) {
	raw := []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`)
	tr := NewGeminiToGoogleAIStudioTranslator("", "gemini-2.5-flash", true)
	headers, body, err := tr.RequestBody(raw, &gcp.NativeGenerateContentRequest{Raw: raw}, false)
	require.NoError(t, err)
	require.Nil(t, body)
	require.Equal(t, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", headers[0].Value())

	response := []byte("data: {\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2,\"totalTokenCount\":5}}\n\n")
	_, passed, usage, model, err := tr.ResponseBody(nil, bytes.NewReader(response), true, nil)
	require.NoError(t, err)
	require.Equal(t, response, passed)
	require.Equal(t, "gemini-2.5-flash", model)
	total, ok := usage.TotalTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), total)
}
