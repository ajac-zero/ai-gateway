// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package endpointspec

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestGenerateContentEndpointSpec(t *testing.T) {
	spec := GenerateContentEndpointSpec{ModelFromPath: "gemini-2.5-flash", Streaming: true}
	raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"secret"},{"thoughtSignature":"not-standard-base64"}]}],"cachedContent":"cachedContents/1"}`)
	model, req, stream, mutated, err := spec.ParseBody(raw, false)
	require.NoError(t, err)
	require.Equal(t, "gemini-2.5-flash", model)
	require.True(t, stream)
	require.NotNil(t, req)
	require.Nil(t, mutated)

	redacted, err := spec.RedactSensitiveInfoFromRequest(req)
	require.NoError(t, err)
	require.JSONEq(t, `{"contents":"[REDACTED]"}`, string(redacted.Raw))

	tr, err := spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaGCPVertexAI}, "")
	require.NoError(t, err)
	require.NotNil(t, tr)
	tr, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaGoogleAIStudio}, "")
	require.NoError(t, err)
	require.NotNil(t, tr)

	tr, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI, Prefix: "v1"}, "gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, tr)

	_, err = spec.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAnthropic}, "")
	require.ErrorContains(t, err, "unsupported backend")
}

func TestGenerateContentEndpointSpecMalformedBody(t *testing.T) {
	_, _, _, _, err := (GenerateContentEndpointSpec{}).ParseBody([]byte("{"), false)
	require.ErrorContains(t, err, "malformed request")
}
