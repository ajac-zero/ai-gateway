// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

// TestRequestMetadata covers streaming, output type and reasoning metadata per
// endpoint. These are metadata, so they must not depend on content capture.
func TestRequestMetadata(t *testing.T) {
	sse := "sse"
	tests := []struct {
		name     string
		attrs    []attribute.KeyValue
		expected []attribute.KeyValue
	}{
		{
			name:     "chat non-stream",
			attrs:    chatRequestAttrs(&openai.ChatCompletionRequest{}),
			expected: nil,
		},
		{
			name: "chat stream json_schema reasoning",
			attrs: chatRequestAttrs(&openai.ChatCompletionRequest{
				Stream:          true,
				ReasoningEffort: openai.ReasoningEffortHigh,
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{},
				},
			}),
			expected: []attribute.KeyValue{
				attribute.Bool(RequestStream, true),
				attribute.String(RequestReasoningLevel, "high"),
				attribute.String(OutputType, "json"),
			},
		},
		{
			name: "chat text format",
			attrs: chatRequestAttrs(&openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfText: &openai.ChatCompletionResponseFormatTextParam{},
				},
			}),
			expected: []attribute.KeyValue{attribute.String(OutputType, "text")},
		},
		{
			name:     "completion stream",
			attrs:    completionRequestAttrs(&openai.CompletionRequest{Stream: true}),
			expected: []attribute.KeyValue{attribute.Bool(RequestStream, true)},
		},
		{
			name: "responses stream",
			attrs: responsesRequestAttrs(&openai.ResponseRequest{
				Stream:             true,
				PreviousResponseID: "resp_1",
				Reasoning:          openai.ReasoningParam{Effort: "low"},
			}),
			expected: []attribute.KeyValue{
				attribute.Bool(RequestStream, true),
				attribute.String(RequestReasoningLevel, "low"),
				attribute.String(RequestPreviousResponseID, "resp_1"),
			},
		},
		{
			name:     "messages stream",
			attrs:    anthropicRequestAttrs(&anthropicschema.MessagesRequest{Stream: true}),
			expected: []attribute.KeyValue{attribute.Bool(RequestStream, true)},
		},
		{
			name:  "image generation stream",
			attrs: imageGenerationRequestAttrs(&openai.ImageGenerationRequest{Stream: true}),
			expected: []attribute.KeyValue{
				attribute.String(OutputType, "image"),
				attribute.Bool(RequestStream, true),
			},
		},
		{
			name:     "speech non-stream",
			attrs:    speechRequestAttrs(&openai.SpeechRequest{}),
			expected: []attribute.KeyValue{attribute.String(OutputType, "speech")},
		},
		{
			name:  "speech sse",
			attrs: speechRequestAttrs(&openai.SpeechRequest{StreamFormat: &sse}),
			expected: []attribute.KeyValue{
				attribute.String(OutputType, "speech"),
				attribute.Bool(RequestStream, true),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.ElementsMatch(t, tc.expected, tc.attrs)
		})
	}
}

// TestResponsesStreamingMatchesUnary pins that a terminal stream event is
// recorded exactly as the unary response, with content disabled and enabled.
func TestResponsesStreamingMatchesUnary(t *testing.T) {
	resp := openai.Response{
		ID:    "resp_1",
		Model: "gpt-5",
		Usage: &openai.ResponseUsage{InputTokens: 10, OutputTokens: 4},
	}
	chunks := []*openai.ResponseStreamEventUnion{
		{OfResponseCreated: &openai.ResponseCreatedEvent{}},
		nil,
		{OfResponseCompleted: &openai.ResponseCompletedEvent{Response: resp}},
	}
	for _, capture := range []bool{false, true} {
		r := NewResponsesRecorder(&Config{CaptureMessageContent: capture})
		unary := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			r.RecordResponse(span, &resp)
			return false
		})
		stream := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			r.RecordResponseChunks(span, chunks)
			return false
		})
		require.NotEmpty(t, stream.Attributes)
		testotel.RequireAttributesEqual(t, unary.Attributes, stream.Attributes)
	}
}

func TestResponsesFailedResponseRecordsError(t *testing.T) {
	resp := openai.Response{
		ID:     "resp_failed",
		Status: "failed",
		Error: openai.ResponseError{
			Code:    "server_error",
			Message: "provider failure detail",
		},
	}
	chunks := []*openai.ResponseStreamEventUnion{
		{OfResponseFailed: &openai.ResponseFailedEvent{Response: resp}},
	}

	for _, capture := range []bool{false, true} {
		r := NewResponsesRecorder(&Config{CaptureMessageContent: capture})
		unary := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			r.RecordResponse(span, &resp)
			return false
		})
		stream := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			r.RecordResponseChunks(span, chunks)
			return false
		})

		require.Equal(t, otelcodes.Error, unary.Status.Code)
		require.Equal(t, capture, unary.Status.Description == "provider failure detail")
		require.Contains(t, unary.Attributes, attribute.String(ErrorType, "server_error"))
		testotel.RequireAttributesEqual(t, unary.Attributes, stream.Attributes)
		require.Equal(t, unary.Status, stream.Status)
	}
}

func TestResponsesIncompleteResponseDoesNotFailSpan(t *testing.T) {
	r := NewResponsesRecorder(NewConfig())
	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponse(span, &openai.Response{Status: "incomplete"})
		return false
	})
	require.Equal(t, otelcodes.Unset, span.Status.Code)
}

func TestAnthropicInputTokensIncludeCache(t *testing.T) {
	attrs := anthropicResponseAttrs(&anthropicschema.MessagesResponse{
		Usage: &anthropicschema.Usage{InputTokens: 5, CacheReadInputTokens: 10, CacheCreationInputTokens: 3, OutputTokens: 2},
	})
	require.Contains(t, attrs, attribute.Int(UsageInputTokens, 18))
	require.Contains(t, attrs, attribute.Int(UsageOutputTokens, 2))
}
