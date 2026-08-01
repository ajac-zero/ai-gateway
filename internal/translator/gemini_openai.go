// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package translator

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strconv"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewGeminiToOpenAITranslator translates the common text subset of generateContent.
func NewGeminiToOpenAITranslator(prefix, model string, streaming bool) GeminiGenerateContentTranslator {
	return &geminiToOpenAITranslator{path: path.Join("/", prefix, "chat/completions"), model: model, streaming: streaming}
}

type geminiToOpenAITranslator struct {
	path      string
	model     string
	streaming bool
	buffered  []byte
}

type geminiTextPart struct {
	Text string `json:"text"`
}

func (g *geminiToOpenAITranslator) RequestBody(raw []byte, _ *gcp.NativeGenerateContentRequest, _ bool) ([]internalapi.Header, []byte, error) {
	var request struct {
		Contents []struct {
			Role  string           `json:"role"`
			Parts []geminiTextPart `json:"parts"`
		} `json:"contents"`
		SystemInstruction *struct {
			Parts []geminiTextPart `json:"parts"`
		} `json:"systemInstruction"`
		GenerationConfig *struct {
			Temperature     *float64 `json:"temperature"`
			TopP            *float64 `json:"topP"`
			MaxOutputTokens *int64   `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, nil, fmt.Errorf("invalid Gemini request: %w", err)
	}
	messages := make([]map[string]any, 0, len(request.Contents)+1)
	if request.SystemInstruction != nil {
		messages = append(messages, map[string]any{"role": "system", "content": joinGeminiText(request.SystemInstruction.Parts)})
	}
	for _, content := range request.Contents {
		role := content.Role
		if role == "model" {
			role = "assistant"
		}
		if role != "user" && role != "assistant" {
			return nil, nil, fmt.Errorf("%w: unsupported Gemini role %q", internalapi.ErrInvalidRequestBody, content.Role)
		}
		messages = append(messages, map[string]any{"role": role, "content": joinGeminiText(content.Parts)})
	}
	translated := map[string]any{"model": g.model, "messages": messages, "stream": g.streaming}
	if config := request.GenerationConfig; config != nil {
		translated["temperature"] = config.Temperature
		translated["top_p"] = config.TopP
		translated["max_tokens"] = config.MaxOutputTokens
	}
	body, err := json.Marshal(translated)
	if err != nil {
		return nil, nil, err
	}
	return []internalapi.Header{{pathHeaderName, g.path}, {contentLengthHeaderName, strconv.Itoa(len(body))}}, body, nil
}

func joinGeminiText(parts []geminiTextPart) string {
	var out bytes.Buffer
	for _, part := range parts {
		out.WriteString(part.Text)
	}
	return out.String()
}

func (g *geminiToOpenAITranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	if g.streaming {
		return []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}, nil
	}
	return nil, nil
}

func (g *geminiToOpenAITranslator) ResponseBody(_ map[string]string, reader io.Reader, end bool, _ tracingapi.Span[gcp.GenerateContentResponse, gcp.GenerateContentResponse]) ([]internalapi.Header, []byte, metrics.TokenUsage, string, error) {
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", err
	}
	if g.streaming {
		return g.streamingResponse(raw, end)
	}
	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     uint32 `json:"prompt_tokens"`
			CompletionTokens uint32 `json:"completion_tokens"`
			TotalTokens      uint32 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("invalid OpenAI response: %w", err)
	}
	candidates := make([]map[string]any, 0, len(response.Choices))
	for index, choice := range response.Choices {
		candidates = append(candidates, map[string]any{"index": index, "content": map[string]any{"role": "model", "parts": []map[string]any{{"text": choice.Message.Content}}}, "finishReason": openAIFinishReasonToGemini(choice.FinishReason)})
	}
	result := map[string]any{"candidates": candidates, "modelVersion": response.Model, "usageMetadata": map[string]any{"promptTokenCount": response.Usage.PromptTokens, "candidatesTokenCount": response.Usage.CompletionTokens, "totalTokenCount": response.Usage.TotalTokens}}
	body, err := json.Marshal(result)
	usage := metrics.TokenUsage{}
	usage.SetInputTokens(response.Usage.PromptTokens)
	usage.SetOutputTokens(response.Usage.CompletionTokens)
	usage.SetTotalTokens(response.Usage.TotalTokens)
	return []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(body))}}, body, usage, response.Model, err
}

func (g *geminiToOpenAITranslator) streamingResponse(raw []byte, end bool) ([]internalapi.Header, []byte, metrics.TokenUsage, string, error) {
	g.buffered = append(g.buffered, raw...)
	out := make([]byte, 0)
	for {
		lineEnd := bytes.IndexByte(g.buffered, '\n')
		if lineEnd < 0 {
			break
		}
		line := bytes.TrimSpace(g.buffered[:lineEnd])
		g.buffered = g.buffered[lineEnd+1:]
		if !bytes.HasPrefix(line, sseDataPrefix) || bytes.Equal(bytes.TrimPrefix(line, sseDataPrefix), sseDoneMessage) {
			continue
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content *string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bytes.TrimPrefix(line, sseDataPrefix), &chunk); err != nil {
			return nil, nil, metrics.TokenUsage{}, "", err
		}
		for index, choice := range chunk.Choices {
			candidate := map[string]any{"index": index, "content": map[string]any{"role": "model", "parts": []map[string]any{{"text": choice.Delta.Content}}}}
			if choice.FinishReason != "" {
				candidate["finishReason"] = openAIFinishReasonToGemini(choice.FinishReason)
			}
			event, _ := json.Marshal(map[string]any{"candidates": []any{candidate}, "modelVersion": chunk.Model})
			out = append(out, sseDataPrefix...)
			out = append(out, event...)
			out = append(out, '\n', '\n')
		}
	}
	if end {
		g.buffered = nil
	}
	return nil, out, metrics.TokenUsage{}, g.model, nil
}

func (*geminiToOpenAITranslator) ResponseError(_ map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	raw, err := io.ReadAll(body)
	return []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(raw))}}, raw, err
}

func openAIFinishReasonToGemini(reason string) string {
	switch reason {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}
