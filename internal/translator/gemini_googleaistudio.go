// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package translator

import (
	"cmp"
	"fmt"
	"io"
	"strconv"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	internaljson "github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewGeminiToGoogleAIStudioTranslator passes native Gemini requests to the Developer API.
func NewGeminiToGoogleAIStudioTranslator(version, model string, streaming bool) GeminiGenerateContentTranslator {
	return &geminiToGoogleAIStudioTranslator{
		version:   cmp.Or(version, "v1beta"),
		model:     model,
		streaming: streaming,
	}
}

type geminiToGoogleAIStudioTranslator struct {
	version   string
	model     string
	streaming bool
}

func (g *geminiToGoogleAIStudioTranslator) RequestBody(raw []byte, _ *gcp.NativeGenerateContentRequest, force bool) ([]internalapi.Header, []byte, error) {
	method := gcpMethodGenerateContent
	query := ""
	if g.streaming {
		method = gcpMethodStreamGenerateContent
		query = "?alt=sse"
	}
	path := fmt.Sprintf("/%s/models/%s:%s%s", g.version, g.model, method, query)
	if force {
		return []internalapi.Header{{pathHeaderName, path}}, append([]byte(nil), raw...), nil
	}
	return []internalapi.Header{{pathHeaderName, path}}, nil, nil
}

func (g *geminiToGoogleAIStudioTranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	if g.streaming {
		return []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}, nil
	}
	return nil, nil
}

func (g *geminiToGoogleAIStudioTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, _ tracingapi.Span[gcp.GenerateContentResponse, gcp.GenerateContentResponse]) ([]internalapi.Header, []byte, metrics.TokenUsage, string, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", err
	}
	usage := metrics.TokenUsage{}
	responseModel := g.model
	if g.streaming {
		usage = extractUsageFromSSE(raw)
	} else {
		var response gcp.GenerateContentResponse
		if internaljson.Unmarshal(raw, &response) == nil {
			if response.UsageMetadata != nil {
				applyUsageMetadata(response.UsageMetadata, &usage)
			}
			if response.ModelVersion != "" {
				responseModel = response.ModelVersion
			}
		}
	}
	return []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(raw))}}, raw, usage, responseModel, nil
}

func (*geminiToGoogleAIStudioTranslator) ResponseError(_ map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, err
	}
	return []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(raw))}}, raw, nil
}
