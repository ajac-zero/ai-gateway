// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestOpenAIToGCPVertexAIImageTranslator_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name          string
		override      string
		req           openai.ImageGenerationRequest
		expPath       string
		expBody       string
		expErrContain string
	}{
		{
			name:    "no size leaves aspect ratio to the model",
			req:     openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "a cat"},
			expPath: "publishers/google/models/gemini-2.5-flash-image:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"a cat"}]}],"tools":null,"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`,
		},
		{
			name:    "auto size and n=1",
			req:     openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "a cat", Size: "auto", N: 1},
			expPath: "publishers/google/models/gemini-2.5-flash-image:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"a cat"}]}],"tools":null,"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`,
		},
		{
			name:     "model override and landscape size",
			override: "gemini-3-pro-image-preview",
			req:      openai.ImageGenerationRequest{Model: "gpt-image-1", Prompt: "a dog", Size: "1536x1024"},
			expPath:  "publishers/google/models/gemini-3-pro-image-preview:generateContent",
			expBody:  `{"contents":[{"role":"user","parts":[{"text":"a dog"}]}],"tools":null,"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"3:2"}}}`,
		},
		{
			name:          "n greater than 1 is rejected",
			req:           openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "a cat", N: 2},
			expErrContain: "n=2 is not supported",
		},
		{
			name:          "malformed size is rejected",
			req:           openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "a cat", Size: "large"},
			expErrContain: `invalid size "large"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewImageGenerationOpenAIToGCPVertexAITranslator(tc.override)
			headers, body, err := tr.RequestBody(nil, &tc.req, false)
			if tc.expErrContain != "" {
				require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
				require.ErrorContains(t, err, tc.expErrContain)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.expBody, string(body))
			require.Len(t, headers, 2)
			require.Equal(t, pathHeaderName, headers[0].Key())
			require.Equal(t, tc.expPath, headers[0].Value())
			require.Equal(t, contentLengthHeaderName, headers[1].Key())
			require.Equal(t, strconv.Itoa(len(body)), headers[1].Value())
		})
	}
}

func TestOpenAIImageSizeToGeminiAspectRatio(t *testing.T) {
	for size, exp := range map[string]string{
		"":          "",
		"auto":      "",
		"1024x1024": "1:1",
		"256x256":   "1:1",
		"1536x1024": "3:2",
		"1024x1536": "2:3",
		"1024x768":  "4:3",
		"768x1024":  "3:4",
		"1280x1024": "5:4",
		"1024x1280": "4:5",
		"1920x1080": "16:9",
		"1080x1920": "9:16",
		"2520x1080": "21:9",
		// Not a supported ratio: 1.75 is closest to 16:9 (1.78), not 3:2 (1.5) or 21:9 (2.33).
		"1792x1024": "16:9",
		"1024x1792": "9:16",
		// Wider than any supported ratio.
		"4000x1000": "21:9",
	} {
		t.Run(size, func(t *testing.T) {
			got, err := openAIImageSizeToGeminiAspectRatio(size)
			require.NoError(t, err)
			require.Equal(t, exp, got)
		})
	}
	for _, size := range []string{"large", "1024", "1024x", "x1024", "0x1024", "1024x-1", "1024*1024"} {
		t.Run("invalid "+size, func(t *testing.T) {
			_, err := openAIImageSizeToGeminiAspectRatio(size)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		})
	}
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, &openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "two cats"}, false)
	require.NoError(t, err)

	// "aW1hZ2Ux" and "aW1hZ2Uy" are base64 for "image1" and "image2". The thought image ("thought") and the
	// text part must be dropped.
	gcpResp := `{
		"candidates": [{
			"content": {"role": "model", "parts": [
				{"text": "Here are your cats."},
				{"thought": true, "inlineData": {"mimeType": "image/png", "data": "dGhvdWdodA=="}},
				{"inlineData": {"mimeType": "image/png", "data": "aW1hZ2Ux"}},
				{"inlineData": {"mimeType": "image/png", "data": "aW1hZ2Uy"}}
			]},
			"finishReason": "STOP"
		}],
		"createTime": "2026-10-02T12:00:00Z",
		"modelVersion": "gemini-2.5-flash-image-001",
		"usageMetadata": {"promptTokenCount": 7, "candidatesTokenCount": 1290, "thoughtsTokenCount": 100, "totalTokenCount": 1397}
	}`

	span := &mockImageGenerationSpan{}
	headers, body, usage, model, err := tr.ResponseBody(nil, strings.NewReader(gcpResp), true, span)
	require.NoError(t, err)

	created := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC).Unix()
	require.JSONEq(t, `{
		"created": `+strconv.FormatInt(created, 10)+`,
		"data": [{"b64_json": "aW1hZ2Ux"}, {"b64_json": "aW1hZ2Uy"}],
		"output_format": "png",
		"usage": {"input_tokens": 7, "output_tokens": 1390, "total_tokens": 1397}
	}`, string(body))
	require.Equal(t, "gemini-2.5-flash-image-001", model)
	require.Equal(t, []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(body))}}, headers)

	in, _ := usage.InputTokens()
	out, _ := usage.OutputTokens()
	total, _ := usage.TotalTokens()
	require.Equal(t, [3]uint32{7, 1390, 1397}, [3]uint32{in, out, total})

	require.NotNil(t, span.recordedResponse)
	require.Len(t, span.recordedResponse.Data, 2)
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_ModelFallback(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("gemini-2.5-flash-image")
	_, _, err := tr.RequestBody(nil, &openai.ImageGenerationRequest{Model: "gpt-image-1", Prompt: "a cat"}, false)
	require.NoError(t, err)

	_, body, usage, model, err := tr.ResponseBody(nil, strings.NewReader(
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"aW1hZ2Ux"}}]}}]}`), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gemini-2.5-flash-image", model)

	var resp openai.ImageGenerationResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, "jpeg", resp.OutputFormat)
	require.Nil(t, resp.Usage)
	require.NotZero(t, resp.Created)
	_, ok := usage.InputTokens()
	require.False(t, ok)
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_NoImage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		expReason string
	}{
		{
			name:      "blocked prompt",
			body:      `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"nope"}}`,
			expReason: "prompt blocked: PROHIBITED_CONTENT nope",
		},
		{
			name:      "image safety finish reason",
			body:      `{"candidates":[{"content":{"parts":[{"text":"I can't draw that."}]},"finishReason":"IMAGE_SAFETY"}]}`,
			expReason: "finish reason IMAGE_SAFETY",
		},
		{
			name:      "only a thought image",
			body:      `{"candidates":[{"content":{"parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}]}}]}`,
			expReason: "no inlineData parts",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
			_, _, _, _, err := tr.ResponseBody(nil, strings.NewReader(tc.body), true, nil)
			require.ErrorContains(t, err, "GCP Vertex AI returned no image")
			require.ErrorContains(t, err, tc.expReason)
		})
	}
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_InvalidJSON(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
	_, _, _, _, err := tr.ResponseBody(nil, strings.NewReader("not json"), true, nil)
	require.ErrorContains(t, err, "failed to unmarshal body")
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseError(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
	headers, body, err := tr.ResponseError(map[string]string{statusHeaderName: "400"}, strings.NewReader(
		`{"error":{"code":400,"message":"Multiple candidates is not enabled","status":"INVALID_ARGUMENT"}}`))
	require.NoError(t, err)
	require.Len(t, headers, 2)
	require.JSONEq(t, `{"type":"error","error":{"type":"INVALID_ARGUMENT","message":"Multiple candidates is not enabled","code":"400"}}`, string(body))

	h, err := tr.ResponseHeaders(nil)
	require.NoError(t, err)
	require.Nil(t, h)
}
