// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/sjson"
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewImageGenerationOpenAIToGCPVertexAITranslator implements [Factory] for OpenAI /v1/images/generations
// to GCP Vertex AI Gemini image generation translation.
//
// Gemini image models (for example gemini-2.5-flash-image) generate images through the generateContent
// method with responseModalities ["TEXT", "IMAGE"], and return each image as inlineData in the candidate
// parts. https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/image-generation
func NewImageGenerationOpenAIToGCPVertexAITranslator(modelNameOverride internalapi.ModelNameOverride) OpenAIImageGenerationTranslator {
	return &openAIToGCPVertexAIImageGenerationTranslator{modelNameOverride: modelNameOverride}
}

// openAIToGCPVertexAIImageGenerationTranslator implements [OpenAIImageGenerationTranslator] for /v1/images/generations.
type openAIToGCPVertexAIImageGenerationTranslator struct {
	modelNameOverride internalapi.ModelNameOverride
	// requestModel stores the effective model for this request (override or provided)
	// so we can attribute metrics later.
	requestModel internalapi.RequestModel
}

// geminiImageAspectRatios are the aspect ratios Gemini image models accept in imageConfig.aspectRatio.
// https://cloud.google.com/vertex-ai/generative-ai/docs/models/gemini/2-5-flash-image
var geminiImageAspectRatios = []struct {
	name  string
	ratio float64
}{
	{"1:1", 1},
	{"2:3", 2.0 / 3},
	{"3:2", 3.0 / 2},
	{"3:4", 3.0 / 4},
	{"4:3", 4.0 / 3},
	{"4:5", 4.0 / 5},
	{"5:4", 5.0 / 4},
	{"9:16", 9.0 / 16},
	{"16:9", 16.0 / 9},
	{"21:9", 21.0 / 9},
}

// openAIImageSizeToGeminiAspectRatio converts an OpenAI size such as "1536x1024" to the closest aspect ratio
// Gemini supports. Gemini does not accept pixel dimensions, so sizes like DALL-E 3's 1792x1024 map to the
// nearest ratio (16:9). An empty size or "auto" returns "", which leaves the choice to the model.
func openAIImageSizeToGeminiAspectRatio(size string) (string, error) {
	if size == "" || size == "auto" {
		return "", nil
	}
	w, h, ok := strings.Cut(size, "x")
	width, werr := strconv.Atoi(w)
	height, herr := strconv.Atoi(h)
	if !ok || werr != nil || herr != nil || width <= 0 || height <= 0 {
		return "", fmt.Errorf("%w: invalid size %q: must be auto or WIDTHxHEIGHT", internalapi.ErrInvalidRequestBody, size)
	}
	// Compare in log space so that, for example, 2:1 is as far from 1:1 as 1:2 is.
	target := math.Log(float64(width) / float64(height))
	best, bestDistance := "", math.Inf(1)
	for _, ar := range geminiImageAspectRatios {
		if d := math.Abs(math.Log(ar.ratio) - target); d < bestDistance {
			best, bestDistance = ar.name, d
		}
	}
	return best, nil
}

// RequestBody implements [OpenAIImageGenerationTranslator.RequestBody]. It translates an OpenAI
// ImageGenerationRequest to a Gemini generateContent request.
//
// Only prompt, model, n, and size are translated. Gemini image models return a single candidate, so n
// greater than 1 is rejected. Images are always returned as b64_json, because Vertex AI does not host
// generated images at a URL.
func (o *openAIToGCPVertexAIImageGenerationTranslator) RequestBody(_ []byte, req *openai.ImageGenerationRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if req.N > 1 {
		return nil, nil, fmt.Errorf("%w: n=%d is not supported by GCP Vertex AI Gemini image models; only n=1 is supported",
			internalapi.ErrInvalidRequestBody, req.N)
	}
	aspectRatio, err := openAIImageSizeToGeminiAspectRatio(req.Size)
	if err != nil {
		return nil, nil, err
	}
	o.requestModel = cmp.Or(o.modelNameOverride, req.Model)

	gcpReq := gcp.GenerateContentRequest{
		Contents: []genai.Content{{
			Role:  genai.RoleUser,
			Parts: []*genai.Part{genai.NewPartFromText(req.Prompt)},
		}},
		GenerationConfig: &genai.GenerationConfig{
			ResponseModalities: []genai.Modality{genai.ModalityText, genai.ModalityImage},
		},
	}
	newBody, err = json.Marshal(gcpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling Gemini image generation request: %w", err)
	}
	if aspectRatio != "" {
		// genai.GenerationConfig has no imageConfig field, but the REST API accepts generationConfig.imageConfig.
		newBody, err = sjson.SetBytesOptions(newBody, "generationConfig.imageConfig.aspectRatio", aspectRatio, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set aspect ratio: %w", err)
		}
	}
	newHeaders = []internalapi.Header{
		{pathHeaderName, buildGCPModelPathSuffix(gcpModelPublisherGoogle, o.requestModel, gcpMethodGenerateContent)},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseError implements [OpenAIImageGenerationTranslator.ResponseError].
// Translate GCP Vertex AI exceptions to OpenAI error type.
func (o *openAIToGCPVertexAIImageGenerationTranslator) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	return convertGCPVertexAIErrorToOpenAI(respHeaders, body)
}

// ResponseHeaders implements [OpenAIImageGenerationTranslator.ResponseHeaders].
func (o *openAIToGCPVertexAIImageGenerationTranslator) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [OpenAIImageGenerationTranslator.ResponseBody]. It translates a Gemini generateContent
// response to an OpenAI ImageGenerationResponse. Each inlineData image part becomes one b64_json entry; text parts
// and thought parts are dropped.
func (o *openAIToGCPVertexAIImageGenerationTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.ImageGenerationSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	gcpResp := &genai.GenerateContentResponse{}
	if err = json.NewDecoder(body).Decode(gcpResp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	responseModel = cmp.Or(gcpResp.ModelVersion, o.requestModel)

	openAIResp := &openai.ImageGenerationResponse{Created: time.Now().Unix()}
	if !gcpResp.CreateTime.IsZero() {
		openAIResp.Created = gcpResp.CreateTime.Unix()
	}
	for _, candidate := range gcpResp.Candidates {
		if candidate == nil || candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part == nil || part.Thought || part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}
			// genai decodes inlineData.data from base64 into raw bytes, so encode it back for b64_json.
			openAIResp.Data = append(openAIResp.Data, openai.ImageGenerationResponseData{
				B64JSON: base64.StdEncoding.EncodeToString(part.InlineData.Data),
			})
			if openAIResp.OutputFormat == "" {
				openAIResp.OutputFormat = strings.TrimPrefix(part.InlineData.MIMEType, "image/")
			}
		}
	}
	if len(openAIResp.Data) == 0 {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("GCP Vertex AI returned no image: %s", geminiNoImageReason(gcpResp))
	}

	if u := gcpResp.UsageMetadata; u != nil {
		input, output, total := int(u.PromptTokenCount), int(u.CandidatesTokenCount)+int(u.ThoughtsTokenCount), int(u.TotalTokenCount)
		tokenUsage.SetInputTokens(uint32(input))   //nolint:gosec
		tokenUsage.SetOutputTokens(uint32(output)) //nolint:gosec
		tokenUsage.SetTotalTokens(uint32(total))   //nolint:gosec
		openAIResp.Usage = &openai.ImageGenerationUsage{InputTokens: input, OutputTokens: output, TotalTokens: total}
	}

	if span != nil {
		span.RecordResponse(openAIResp)
	}

	newBody, err = json.Marshal(openAIResp)
	if err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("error marshaling OpenAI image generation response: %w", err)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

// geminiNoImageReason describes why a successful Gemini response carries no image, such as a blocked prompt
// or a finish reason like IMAGE_SAFETY, so the failure can be diagnosed from the gateway logs.
func geminiNoImageReason(resp *genai.GenerateContentResponse) string {
	if pf := resp.PromptFeedback; pf != nil && pf.BlockReason != "" {
		return fmt.Sprintf("prompt blocked: %s %s", pf.BlockReason, pf.BlockReasonMessage)
	}
	for _, candidate := range resp.Candidates {
		if candidate != nil && candidate.FinishReason != "" {
			return fmt.Sprintf("finish reason %s %s", candidate.FinishReason, candidate.FinishMessage)
		}
	}
	return "no inlineData parts in response candidates"
}
