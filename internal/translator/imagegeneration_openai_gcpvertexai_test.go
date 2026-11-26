// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestOpenAIToGCPVertexAIImageTranslator_RequestBody(t *testing.T) {
	const flash = "gemini-2.5-flash-image"
	for _, tc := range []struct {
		name     string
		override string
		req      openai.ImageGenerationRequest
		expPath  string
		expBody  string
	}{
		{
			name:    "no size leaves aspect ratio to the model",
			req:     openai.ImageGenerationRequest{Model: flash, Prompt: "a cat"},
			expPath: "publishers/google/models/gemini-2.5-flash-image:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"a cat"}]}],"tools":null,"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`,
		},
		{
			name: "explicit defaults are accepted and user is ignored",
			req: openai.ImageGenerationRequest{
				Model: flash, Prompt: "a cat", Size: "auto", N: 1, ResponseFormat: "b64_json",
				Quality: "auto", Background: "auto", Moderation: "auto", User: "user-123",
			},
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
			name:    "output format and compression",
			req:     openai.ImageGenerationRequest{Model: flash, Prompt: "a dog", Size: "1024x1792", OutputFormat: "jpeg", OutputCompression: ptr.To(0)},
			expPath: "publishers/google/models/gemini-2.5-flash-image:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"a dog"}]}],"tools":null,"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"9:16","imageOutputOptions":{"mimeType":"image/jpeg","compressionQuality":0}}}}`,
		},
		{
			name:    "output format alone",
			req:     openai.ImageGenerationRequest{Model: flash, Prompt: "a dog", OutputFormat: "png"},
			expPath: "publishers/google/models/gemini-2.5-flash-image:generateContent",
			expBody: `{"contents":[{"role":"user","parts":[{"text":"a dog"}]}],"tools":null,"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"imageOutputOptions":{"mimeType":"image/png"}}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewImageGenerationOpenAIToGCPVertexAITranslator(tc.override)
			headers, body, err := tr.RequestBody(nil, &tc.req, false)
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

func TestOpenAIToGCPVertexAIImageTranslator_RequestBody_Rejected(t *testing.T) {
	valid := openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "a cat"}
	for _, tc := range []struct {
		name          string
		mutate        func(*openai.ImageGenerationRequest)
		expErrContain string
	}{
		{"empty prompt", func(r *openai.ImageGenerationRequest) { r.Prompt = "" }, "prompt is required"},
		{"n greater than 1", func(r *openai.ImageGenerationRequest) { r.N = 2 }, "n=2 is not supported by GCP Vertex AI Gemini image models (only n=1)"},
		{"url response format", func(r *openai.ImageGenerationRequest) { r.ResponseFormat = "url" }, "response_format=url"},
		{"streaming", func(r *openai.ImageGenerationRequest) { r.Stream = true }, "stream=true"},
		{"partial images", func(r *openai.ImageGenerationRequest) { r.PartialImages = 2 }, "partial_images=2"},
		{"quality", func(r *openai.ImageGenerationRequest) { r.Quality = "hd" }, "quality=hd"},
		{"style", func(r *openai.ImageGenerationRequest) { r.Style = "vivid" }, "style=vivid"},
		{"transparent background", func(r *openai.ImageGenerationRequest) { r.Background = "transparent" }, "background=transparent"},
		{"low moderation", func(r *openai.ImageGenerationRequest) { r.Moderation = "low" }, "moderation=low"},
		{"unknown output format", func(r *openai.ImageGenerationRequest) { r.OutputFormat = "gif" }, "invalid output_format gif"},
		{"compression without format", func(r *openai.ImageGenerationRequest) { r.OutputCompression = ptr.To(50) }, "requires output_format jpeg or webp"},
		{"compression with png", func(r *openai.ImageGenerationRequest) {
			r.OutputFormat, r.OutputCompression = "png", ptr.To(50)
		}, "requires output_format jpeg or webp"},
		{"compression out of range", func(r *openai.ImageGenerationRequest) {
			r.OutputFormat, r.OutputCompression = "jpeg", ptr.To(101)
		}, "between 0 and 100"},
		{"unsupported aspect ratio", func(r *openai.ImageGenerationRequest) { r.Size = "4000x1000" }, "size 4000x1000 does not match"},
		{"malformed size", func(r *openai.ImageGenerationRequest) { r.Size = "large" }, "invalid size large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)
			_, _, err := NewImageGenerationOpenAIToGCPVertexAITranslator("").RequestBody(nil, &req, false)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
			require.ErrorContains(t, err, tc.expErrContain)
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
		// DALL-E 3 sizes are 7:4, 1.6% from 16:9.
		"1792x1024": "16:9",
		"1024x1792": "9:16",
		// The sizes Gemini 2.5 Flash Image itself produces, up to 2.8% from their nominal ratio.
		"1344x768":  "16:9",
		"1184x864":  "4:3",
		"864x1184":  "3:4",
		"1152x896":  "5:4",
		"896x1152":  "4:5",
		"1536x672":  "21:9",
		"1248x832":  "3:2",
		"1280x1000": "5:4", // 2.4% from 5:4.
	} {
		t.Run(size, func(t *testing.T) {
			got, err := openAIImageSizeToGeminiAspectRatio(size)
			require.NoError(t, err)
			require.Equal(t, exp, got)
		})
	}
	for _, size := range []string{
		"1290x1000", // 3.2% from 5:4 and 3.4% from 4:3: between two ratios.
		"1000x700",  // Between 4:3 and 3:2.
		"4000x1000", // Wider than any supported ratio.
	} {
		t.Run("unsupported "+size, func(t *testing.T) {
			_, err := openAIImageSizeToGeminiAspectRatio(size)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
			require.ErrorContains(t, err, "does not match an aspect ratio")
		})
	}
	for _, size := range []string{"large", "1024", "1024x", "x1024", "0x1024", "1024x-1", "1024*1024"} {
		t.Run("invalid "+size, func(t *testing.T) {
			_, err := openAIImageSizeToGeminiAspectRatio(size)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
			require.ErrorContains(t, err, "invalid size")
		})
	}
}

// testPNGBase64 returns a base64-encoded PNG of the given dimensions.
func testPNGBase64(t *testing.T, width, height int) string {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, &openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "two cats", Size: "1536x1024"}, false)
	require.NoError(t, err)

	// The first image is a 12x8 PNG, so its size is reported even though 1536x1024 was requested. The thought
	// image and the text part must be dropped.
	firstImage := testPNGBase64(t, 12, 8)
	gcpResp := `{
		"candidates": [{
			"content": {"role": "model", "parts": [
				{"text": "Here are your cats."},
				{"thought": true, "inlineData": {"mimeType": "image/png", "data": "dGhvdWdodA=="}},
				{"inlineData": {"mimeType": "image/png", "data": "` + firstImage + `"}},
				{"inlineData": {"mimeType": "image/png", "data": "aW1hZ2Uy"}}
			]},
			"finishReason": "STOP"
		}],
		"createTime": "2026-10-02T12:00:00Z",
		"modelVersion": "gemini-2.5-flash-image-001",
		"usageMetadata": {
			"promptTokenCount": 21, "candidatesTokenCount": 1290, "thoughtsTokenCount": 100, "totalTokenCount": 1411,
			"promptTokensDetails": [{"modality": "TEXT", "tokenCount": 21}],
			"candidatesTokensDetails": [{"modality": "IMAGE", "tokenCount": 1290}]
		}
	}`

	span := &mockImageGenerationSpan{}
	headers, body, usage, model, err := tr.ResponseBody(nil, strings.NewReader(gcpResp), true, span)
	require.NoError(t, err)

	created := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC).Unix()
	require.JSONEq(t, `{
		"created": `+strconv.FormatInt(created, 10)+`,
		"data": [{"b64_json": "`+firstImage+`"}, {"b64_json": "aW1hZ2Uy"}],
		"output_format": "png",
		"size": "12x8",
		"usage": {"input_tokens": 21, "output_tokens": 1390, "total_tokens": 1411, "input_tokens_details": {"text_tokens": 21}}
	}`, string(body))
	require.Equal(t, "gemini-2.5-flash-image-001", model)
	require.Equal(t, []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(body))}}, headers)

	in, _ := usage.InputTokens()
	out, _ := usage.OutputTokens()
	total, _ := usage.TotalTokens()
	require.Equal(t, [3]uint32{21, 1390, 1411}, [3]uint32{in, out, total})
	_, ok := usage.CachedInputTokens()
	require.False(t, ok)

	require.NotNil(t, span.recordedResponse)
	require.Len(t, span.recordedResponse.Data, 2)
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_ModelFallback(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("gemini-2.5-flash-image")
	_, _, err := tr.RequestBody(nil, &openai.ImageGenerationRequest{Model: "gpt-image-1", Prompt: "a cat"}, false)
	require.NoError(t, err)

	// The image data is not a decodable image, so size is omitted rather than failing the request.
	_, body, usage, model, err := tr.ResponseBody(nil, strings.NewReader(
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"aW1hZ2Ux"}}]}}]}`), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gemini-2.5-flash-image", model)

	var resp openai.ImageGenerationResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, "jpeg", resp.OutputFormat)
	require.Empty(t, resp.Size)
	require.Nil(t, resp.Usage)
	require.NotZero(t, resp.Created)
	_, ok := usage.InputTokens()
	require.False(t, ok)
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_ImageInputTokens(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
	_, body, usage, _, err := tr.ResponseBody(nil, strings.NewReader(`{
		"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}]}}],
		"usageMetadata":{"promptTokenCount":300,"cachedContentTokenCount":40,"candidatesTokenCount":1290,"totalTokenCount":1590,
			"promptTokensDetails":[{"modality":"TEXT","tokenCount":42},{"modality":"IMAGE","tokenCount":258}]}
	}`), true, nil)
	require.NoError(t, err)

	var resp openai.ImageGenerationResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, &openai.ImageGenerationUsage{
		InputTokens: 300, OutputTokens: 1290, TotalTokens: 1590,
		InputTokensDetails: &openai.ImageGenerationInputTokensDetails{TextTokens: 42, ImageTokens: 258},
	}, resp.Usage)
	cached, ok := usage.CachedInputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(40), cached)
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_OutputFormatMismatch(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
	_, _, err := tr.RequestBody(nil, &openai.ImageGenerationRequest{Model: "gemini-2.5-flash-image", Prompt: "a cat", OutputFormat: "jpeg"}, false)
	require.NoError(t, err)

	span := &mockImageGenerationSpan{}
	headers, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}]}}]}`), true, span)
	require.NoError(t, err)
	require.Equal(t, []internalapi.Header{
		{statusHeaderName, "502"},
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(body))},
	}, headers)
	require.JSONEq(t, `{"type":"error","error":{"type":"GCPVertexAIBackendError","code":"502",
		"message":"GCP Vertex AI returned image/png, but output_format jpeg was requested"}}`, string(body))
	require.Nil(t, span.recordedResponse)
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_NoImage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		expStatus  int
		expType    string
		expMessage string
	}{
		{
			name:       "blocked prompt",
			body:       `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"nope"}}`,
			expStatus:  http.StatusBadRequest,
			expType:    "content_policy_violation",
			expMessage: "GCP Vertex AI returned no image: prompt blocked: PROHIBITED_CONTENT nope",
		},
		{
			name:       "image safety finish reason with refusal text",
			body:       `{"candidates":[{"content":{"parts":[{"text":"I can't draw that."}]},"finishReason":"IMAGE_SAFETY"}]}`,
			expStatus:  http.StatusBadRequest,
			expType:    "content_policy_violation",
			expMessage: "GCP Vertex AI returned no image: finish reason IMAGE_SAFETY. Model text: I can't draw that.",
		},
		{
			name:       "no image finish reason",
			body:       `{"candidates":[{"content":{"parts":[]},"finishReason":"NO_IMAGE","finishMessage":"try again"}]}`,
			expStatus:  http.StatusBadGateway,
			expType:    "GCPVertexAIBackendError",
			expMessage: "GCP Vertex AI returned no image: finish reason NO_IMAGE try again",
		},
		{
			name:       "text-only answer",
			body:       `{"candidates":[{"content":{"parts":[{"text":"Which cat "},{"text":"do you mean?"}]},"finishReason":"STOP"}]}`,
			expStatus:  http.StatusBadGateway,
			expType:    "GCPVertexAIBackendError",
			expMessage: "GCP Vertex AI returned no image: finish reason STOP. Model text: Which cat do you mean?",
		},
		{
			name:       "only a thought image",
			body:       `{"candidates":[{"content":{"parts":[{"thought":true,"text":"thinking","inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}]}}]}`,
			expStatus:  http.StatusBadGateway,
			expType:    "GCPVertexAIBackendError",
			expMessage: "GCP Vertex AI returned no image: no inlineData parts in response candidates",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewImageGenerationOpenAIToGCPVertexAITranslator("")
			headers, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(tc.body), true, nil)
			require.NoError(t, err)
			require.Equal(t, statusHeaderName, headers[0].Key())
			require.Equal(t, strconv.Itoa(tc.expStatus), headers[0].Value())

			var resp openai.Error
			require.NoError(t, json.Unmarshal(body, &resp))
			require.Equal(t, "error", resp.Type)
			require.Equal(t, tc.expType, resp.Error.Type)
			require.Equal(t, strconv.Itoa(tc.expStatus), *resp.Error.Code)
			require.Equal(t, tc.expMessage, resp.Error.Message)
		})
	}
}

func TestOpenAIToGCPVertexAIImageTranslator_ResponseBody_NoImageStillReportsUsage(t *testing.T) {
	tr := NewImageGenerationOpenAIToGCPVertexAITranslator("gemini-2.5-flash-image")
	_, _, err := tr.RequestBody(nil, &openai.ImageGenerationRequest{Prompt: "a cat"}, false)
	require.NoError(t, err)
	_, _, usage, model, err := tr.ResponseBody(nil, strings.NewReader(
		`{"candidates":[{"finishReason":"IMAGE_SAFETY"}],"usageMetadata":{"promptTokenCount":9,"totalTokenCount":9}}`), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gemini-2.5-flash-image", model)
	in, ok := usage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(9), in)
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
