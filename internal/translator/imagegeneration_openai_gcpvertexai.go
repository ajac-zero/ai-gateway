// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg" // Registers the JPEG decoder for image.DecodeConfig.
	_ "image/png"  // Registers the PNG decoder for image.DecodeConfig.
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/sjson"
	"google.golang.org/genai"
	"k8s.io/utils/ptr"

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
	// outputFormat is the OpenAI output_format the client asked for, or "" to accept the model's default.
	outputFormat string
}

// geminiImageAspectRatios are the aspect ratios Gemini image models accept in imageConfig.aspectRatio.
// https://cloud.google.com/vertex-ai/generative-ai/docs/reference/rest/v1beta1/GenerationConfig#imageconfig
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

// geminiAspectRatioTolerance is how far, as a ratio, a requested size may be from a Gemini aspect ratio and still
// select it. Gemini's own output sizes are up to about 2.8% off their nominal ratio (for example 1344x768 for
// 16:9 and 1184x864 for 4:3), and the closest pair of supported ratios (4:3 and 5:4) are 6.7% apart, so 3%
// accepts every size Gemini itself produces without ever matching two ratios.
var geminiAspectRatioTolerance = math.Log(1.03)

// openAIImageSizeToGeminiAspectRatio converts an OpenAI size such as "1536x1024" to the Gemini aspect ratio it
// represents. Gemini does not accept pixel dimensions: the model picks the resolution for the aspect ratio. A
// size whose ratio is not within [geminiAspectRatioTolerance] of a supported ratio is rejected rather than
// silently changing the image shape. An empty size or "auto" returns "", which leaves the choice to the model.
func openAIImageSizeToGeminiAspectRatio(size string) (string, error) {
	if size == "" || size == "auto" {
		return "", nil
	}
	w, h, ok := strings.Cut(size, "x")
	width, werr := strconv.Atoi(w)
	height, herr := strconv.Atoi(h)
	if !ok || werr != nil || herr != nil || width <= 0 || height <= 0 {
		return "", fmt.Errorf("%w: invalid size %s: must be auto or WIDTHxHEIGHT", internalapi.ErrInvalidRequestBody, size)
	}
	// Compare in log space so that, for example, 2:1 is as far from 1:1 as 1:2 is.
	target := math.Log(float64(width) / float64(height))
	for _, ar := range geminiImageAspectRatios {
		if math.Abs(math.Log(ar.ratio)-target) <= geminiAspectRatioTolerance {
			return ar.name, nil
		}
	}
	names := make([]string, len(geminiImageAspectRatios))
	for i, ar := range geminiImageAspectRatios {
		names[i] = ar.name
	}
	return "", fmt.Errorf("%w: size %s does not match an aspect ratio supported by GCP Vertex AI Gemini image models (%s)",
		internalapi.ErrInvalidRequestBody, size, strings.Join(names, ", "))
}

// validateGeminiImageGenerationRequest rejects OpenAI parameters that Gemini image models cannot honor, so that a
// request never silently produces something other than what the client asked for. The user parameter is the only
// one ignored: it identifies the end user for abuse monitoring and does not affect the generated image.
func validateGeminiImageGenerationRequest(req *openai.ImageGenerationRequest) error {
	unsupported := func(param, hint string) error {
		return fmt.Errorf("%w: %s is not supported by GCP Vertex AI Gemini image models%s",
			internalapi.ErrInvalidRequestBody, param, hint)
	}
	switch {
	case req.Prompt == "":
		return fmt.Errorf("%w: prompt is required", internalapi.ErrInvalidRequestBody)
	case req.N > 1:
		return unsupported(fmt.Sprintf("n=%d", req.N), " (only n=1)")
	case req.ResponseFormat != "" && req.ResponseFormat != "b64_json":
		return unsupported(fmt.Sprintf("response_format=%s", req.ResponseFormat), " (images are only returned as b64_json)")
	case req.Stream:
		return unsupported("stream=true", "")
	case req.PartialImages > 0:
		return unsupported(fmt.Sprintf("partial_images=%d", req.PartialImages), "")
	case req.Quality != "" && req.Quality != "auto":
		return unsupported(fmt.Sprintf("quality=%s", req.Quality), " (only auto)")
	case req.Style != "":
		return unsupported(fmt.Sprintf("style=%s", req.Style), "")
	case req.Background != "" && req.Background != "auto":
		return unsupported(fmt.Sprintf("background=%s", req.Background), " (only auto)")
	case req.Moderation != "" && req.Moderation != "auto":
		return unsupported(fmt.Sprintf("moderation=%s", req.Moderation), " (only auto)")
	}
	switch req.OutputFormat {
	case "", "png", "jpeg", "webp":
	default:
		return fmt.Errorf("%w: invalid output_format %s: must be png, jpeg, or webp", internalapi.ErrInvalidRequestBody, req.OutputFormat)
	}
	if c := req.OutputCompression; c != nil {
		if req.OutputFormat != "jpeg" && req.OutputFormat != "webp" {
			return fmt.Errorf("%w: output_compression requires output_format jpeg or webp", internalapi.ErrInvalidRequestBody)
		}
		if *c < 0 || *c > 100 {
			return fmt.Errorf("%w: output_compression must be between 0 and 100", internalapi.ErrInvalidRequestBody)
		}
	}
	return nil
}

// RequestBody implements [OpenAIImageGenerationTranslator.RequestBody]. It translates an OpenAI
// ImageGenerationRequest to a Gemini generateContent request.
//
// prompt, model, size (as an aspect ratio), output_format, and output_compression are translated. Parameters
// Gemini cannot honor are rejected; see [validateGeminiImageGenerationRequest].
func (o *openAIToGCPVertexAIImageGenerationTranslator) RequestBody(_ []byte, req *openai.ImageGenerationRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if err = validateGeminiImageGenerationRequest(req); err != nil {
		return nil, nil, err
	}
	aspectRatio, err := openAIImageSizeToGeminiAspectRatio(req.Size)
	if err != nil {
		return nil, nil, err
	}
	o.requestModel = cmp.Or(o.modelNameOverride, req.Model)
	o.outputFormat = req.OutputFormat

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

	imageConfig := genai.ImageConfig{AspectRatio: aspectRatio}
	if req.OutputFormat != "" {
		imageConfig.ImageOutputOptions = &genai.ImageConfigImageOutputOptions{MIMEType: "image/" + req.OutputFormat}
		if c := req.OutputCompression; c != nil {
			imageConfig.ImageOutputOptions.CompressionQuality = ptr.To(int32(*c)) //nolint:gosec // validated to be 0-100.
		}
	}
	if imageConfig != (genai.ImageConfig{}) {
		// genai.GenerationConfig has no imageConfig field (only genai.GenerateContentConfig does), but the
		// REST API accepts generationConfig.imageConfig.
		newBody, err = sjson.SetBytesOptions(newBody, "generationConfig.imageConfig", imageConfig, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set image config: %w", err)
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
//
// A successful Gemini response can still carry no image, for example when a safety filter blocks the prompt or
// the output, or the model answers only with text. That is rewritten to an OpenAI error response with an error
// status (see [geminiNoImageError]) instead of an empty data list. Token usage is reported either way, because
// Vertex AI bills the request.
func (o *openAIToGCPVertexAIImageGenerationTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.ImageGenerationSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	gcpResp := &genai.GenerateContentResponse{}
	if err = json.NewDecoder(body).Decode(gcpResp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	responseModel = cmp.Or(gcpResp.ModelVersion, o.requestModel)

	var usage *openai.ImageGenerationUsage
	if u := gcpResp.UsageMetadata; u != nil {
		chatUsage := geminiUsageToOpenAIUsage(u)
		tokenUsage.SetInputTokens(uint32(chatUsage.PromptTokens))      //nolint:gosec
		tokenUsage.SetOutputTokens(uint32(chatUsage.CompletionTokens)) //nolint:gosec
		tokenUsage.SetTotalTokens(uint32(chatUsage.TotalTokens))       //nolint:gosec
		if u.CachedContentTokenCount > 0 {
			tokenUsage.SetCachedInputTokens(uint32(u.CachedContentTokenCount)) //nolint:gosec
		}
		usage = &openai.ImageGenerationUsage{
			InputTokens:  chatUsage.PromptTokens,
			OutputTokens: chatUsage.CompletionTokens,
			TotalTokens:  chatUsage.TotalTokens,
		}
		for _, d := range u.PromptTokensDetails {
			if d == nil {
				continue
			}
			if usage.InputTokensDetails == nil {
				usage.InputTokensDetails = &openai.ImageGenerationInputTokensDetails{}
			}
			switch d.Modality {
			case genai.MediaModalityText:
				usage.InputTokensDetails.TextTokens += int(d.TokenCount)
			case genai.MediaModalityImage:
				usage.InputTokensDetails.ImageTokens += int(d.TokenCount)
			}
		}
	}

	openAIResp := &openai.ImageGenerationResponse{Created: time.Now().Unix(), Usage: usage}
	if !gcpResp.CreateTime.IsZero() {
		openAIResp.Created = gcpResp.CreateTime.Unix()
	}
	var firstImage *genai.Blob
	for _, candidate := range gcpResp.Candidates {
		if candidate == nil || candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part == nil || part.Thought || part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}
			if firstImage == nil {
				firstImage = part.InlineData
			}
			// genai decodes inlineData.data from base64 into raw bytes, so encode it back for b64_json.
			openAIResp.Data = append(openAIResp.Data, openai.ImageGenerationResponseData{
				B64JSON: base64.StdEncoding.EncodeToString(part.InlineData.Data),
			})
		}
	}

	if firstImage == nil {
		status, errType, message := geminiNoImageError(gcpResp)
		newHeaders, newBody, err = openAIImageGenerationErrorResponse(status, errType, message)
		return newHeaders, newBody, tokenUsage, responseModel, err
	}
	openAIResp.OutputFormat = strings.TrimPrefix(firstImage.MIMEType, "image/")
	if o.outputFormat != "" && openAIResp.OutputFormat != o.outputFormat {
		newHeaders, newBody, err = openAIImageGenerationErrorResponse(http.StatusBadGateway, gcpVertexAIBackendError,
			fmt.Sprintf("GCP Vertex AI returned %s, but output_format %s was requested", firstImage.MIMEType, o.outputFormat))
		return newHeaders, newBody, tokenUsage, responseModel, err
	}
	// Gemini picks the resolution from the aspect ratio, so report the size it actually generated.
	if cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(firstImage.Data)); decodeErr == nil {
		openAIResp.Size = fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
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

// geminiImageBlockedFinishReasons are the finish reasons with which Gemini refuses to return an image for policy
// reasons. Retrying the same prompt will not help, so they map to a client error.
var geminiImageBlockedFinishReasons = map[genai.FinishReason]bool{
	genai.FinishReasonSafety:                 true,
	genai.FinishReasonBlocklist:              true,
	genai.FinishReasonProhibitedContent:      true,
	genai.FinishReasonSPII:                   true,
	genai.FinishReasonRecitation:             true,
	genai.FinishReasonImageSafety:            true,
	genai.FinishReasonImageProhibitedContent: true,
	genai.FinishReasonImageRecitation:        true,
}

// geminiNoImageError describes why a successful Gemini response carries no image. A blocked prompt or a policy
// finish reason such as IMAGE_SAFETY is a 400 content_policy_violation, like OpenAI's moderation errors. Anything
// else, such as NO_IMAGE or a text-only answer, is a 502, because the provider did not return what it was asked
// for. The message includes the model's text, which often explains a refusal.
func geminiNoImageError(resp *genai.GenerateContentResponse) (status int, errType, message string) {
	status, errType = http.StatusBadGateway, gcpVertexAIBackendError
	var reason string
	if pf := resp.PromptFeedback; pf != nil && pf.BlockReason != "" {
		status, errType = http.StatusBadRequest, "content_policy_violation"
		reason = strings.TrimSpace(fmt.Sprintf("prompt blocked: %s %s", pf.BlockReason, pf.BlockReasonMessage))
	}
	var text strings.Builder
	for _, candidate := range resp.Candidates {
		if candidate == nil {
			continue
		}
		if reason == "" && candidate.FinishReason != "" {
			reason = strings.TrimSpace(fmt.Sprintf("finish reason %s %s", candidate.FinishReason, candidate.FinishMessage))
			if geminiImageBlockedFinishReasons[candidate.FinishReason] {
				status, errType = http.StatusBadRequest, "content_policy_violation"
			}
		}
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part != nil && !part.Thought && part.Text != "" {
				text.WriteString(part.Text)
			}
		}
	}
	message = "GCP Vertex AI returned no image: " + cmp.Or(reason, "no inlineData parts in response candidates")
	if t := strings.TrimSpace(text.String()); t != "" {
		message += ". Model text: " + t
	}
	return
}

// openAIImageGenerationErrorResponse builds an OpenAI error body, in the same shape as
// [convertGCPVertexAIErrorToOpenAI], and rewrites the response status to the given one.
func openAIImageGenerationErrorResponse(status int, errType, message string) (newHeaders []internalapi.Header, newBody []byte, err error) {
	code := strconv.Itoa(status)
	newBody, err = json.Marshal(openai.Error{
		Type:  "error",
		Error: openai.ErrorType{Type: errType, Code: &code, Message: message},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{statusHeaderName, code},
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}
