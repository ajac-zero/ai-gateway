// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package extproc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractGeminiModelFromPath(t *testing.T) {
	tests := []struct {
		path      string
		model     string
		streaming bool
		wantErr   bool
	}{
		{"/v1beta/models/gemini-2.5-flash:generateContent", "gemini-2.5-flash", false, false},
		{"/prefix/v1beta/models/gemini%2Dpro:streamGenerateContent", "gemini-pro", true, false},
		{"/v1beta/models/gemini:countTokens", "", false, true},
		{"/v1beta/models/:generateContent", "", false, true},
		{"/v1beta/models/a/b:generateContent", "", false, true},
		{"/v1beta/models/gemini:generateContent/", "", false, true},
	}
	for _, test := range tests {
		model, streaming, err := extractGeminiModelFromPath(test.path)
		if test.wantErr {
			require.Error(t, err, test.path)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, test.model, model)
		require.Equal(t, test.streaming, streaming)
	}
}
