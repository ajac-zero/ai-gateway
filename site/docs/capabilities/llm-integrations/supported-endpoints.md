---
id: supported-endpoints
title: Supported API Endpoints
sidebar_position: 9
---

The Agent Router provides OpenAI-compatible API endpoints, the Anthropic-compatible API, and the native Gemini API for routing and managing LLM/AI traffic. This page documents which endpoints are currently supported and their capabilities.

## Overview

The Agent Router acts as a proxy that accepts OpenAI-compatible, Anthropic-compatible, and native Gemini API requests and routes them to various AI providers. While it maintains compatibility with these API specifications, it currently supports a subset of each full API.

## Supported Endpoints

### Chat Completions

**Endpoint:** `POST /v1/chat/completions`

**Status:** ✅ Fully Supported

**Description:** Create a chat completion response for the given conversation.

**Features:**

- ✅ Streaming and non-streaming responses
- ✅ Function calling
- ✅ Response format specification (including JSON schema)
- ✅ Temperature, top_p, and other sampling parameters
- ✅ System and user messages
- ✅ Audio and video inputs
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- OpenAI
- AWS Bedrock (with automatic translation)
- Azure OpenAI (with automatic translation)
- GCP VertexAI (with automatic translation)
- GCP Anthropic (with automatic translation)
- Anthropic (with automatic translation)
- Any OpenAI-compatible provider (Groq, Together AI, Mistral, Tetrate Agent Router Service, etc.)

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Hello, how are you?"
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

### Anthropic Messages

**Endpoint:** `POST /anthropic/v1/messages`

**Status:** ✅ Fully Supported

**Description:** Send a structured list of input messages with text and/or image content, and the model will generate the next message in the conversation.

**Features:**

- ✅ Streaming and non-streaming responses
- ✅ Function calling
- ✅ Extended thinking
- ✅ Response format specification (including JSON schema)
- ✅ Temperature, top_p, and other sampling parameters
- ✅ System and user messages
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- Anthropic
- GCP Anthropic
- AWS Anthropic
- AWS Bedrock

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4",
    "messages": [
      {
        "role": "user",
        "content": "Hello, how are you?"
      }
    ],
    "max_tokens": 100
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

### Anthropic Count Tokens

**Endpoint:** `POST /anthropic/v1/messages/count_tokens`

**Status:** ✅ Fully Supported

**Description:** Count the number of input tokens for a Messages API request without actually creating a message. Useful for estimating costs and validating request sizes before sending.

**Features:**

- ✅ Token counting for messages, system prompts, and tools
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Provider fallback and load balancing

**Supported Providers:**

- Anthropic
- GCP Anthropic
- AWS Anthropic

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-sonnet-4",
    "messages": [
      {
        "role": "user",
        "content": "Hello, how are you?"
      }
    ]
  }' \
  $GATEWAY_URL/anthropic/v1/messages/count_tokens
```

**Response:**

```json
{
  "input_tokens": 14
}
```

### Completions

**Endpoint:** `POST /v1/completions`

**Status:** ✅ Fully Supported

**Description:** Create a text completion for the given prompt (legacy endpoint).

**Features:**

- ✅ Non-streaming responses
- ✅ Streaming responses
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Temperature, top_p, and other sampling parameters
- ✅ Single and batch prompt processing
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing
- ✅ Full metrics support (token usage, request duration, time to first token, inter-token latency)

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports completions

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "babbage-002",
    "prompt": "def fib(n):\n    if n <= 1:\n        return n\n    else:\n        return fib(n-1) + fib(n-2)",
    "max_tokens": 25,
    "temperature": 0.4,
    "top_p": 0.9
  }' \
  $GATEWAY_URL/v1/completions
```

### Embeddings

**Endpoint:** `POST /v1/embeddings`

**Status:** ✅ Fully Supported

**Description:** Create embeddings for the given input text.

**Features:**

- ✅ Single and batch text embedding
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- OpenAI
- AWS Bedrock (Titan models, with automatic translation)
- GCP VertexAI (with automatic translation)
- Any OpenAI-compatible provider that supports embeddings, including Azure OpenAI.

### Image Generation

**Endpoint:** `POST /v1/images/generations`

**Status:** ✅ Supported

**Description:** Generate one or more images from a text prompt using OpenAI-compatible models.

**Features:**

- **Non-streaming responses**: Returns JSON payload with image URLs or base64 content
- **Model selection**: Via request body `model` or `x-ai-eg-model` header
- **Parameters**: `prompt`, `size`, `n`, `quality`, `response_format`
- **Metrics**: Records image count, model, and size; token usage when provided
- **Provider fallback and load balancing**

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports image generations
- Google AI Studio (native Gemini `generateContent`), via the `GoogleAIStudio` backend schema. The Gemini image bytes returned as `inlineData` are base64-encoded into the OpenAI `b64_json` field.
- GCP Vertex AI Gemini image models, such as `gemini-2.5-flash-image` (with automatic translation; see below)

**GCP Vertex AI translation:**

Requests to a GCP Vertex AI backend are translated to the Gemini `generateContent` method with `responseModalities: ["TEXT", "IMAGE"]`. Each generated image is returned as `b64_json`.

- `size` selects a Gemini aspect ratio (`1:1`, `2:3`, `3:2`, `3:4`, `4:3`, `4:5`, `5:4`, `9:16`, `16:9`, or `21:9`) when it is within 3% of one. For example, `1536x1024` becomes `3:2` and `1792x1024` becomes `16:9`. The model chooses the pixel resolution, and the response `size` reports the dimensions it generated. `auto` or no size lets the model choose the aspect ratio. Other sizes, such as `4000x1000`, are rejected.
- `output_format` (`png`, `jpeg`, or `webp`) and `output_compression` are sent as `imageConfig.imageOutputOptions`. If Vertex AI returns a different format than requested, the gateway returns `502 Bad Gateway`.
- Parameters that Gemini cannot honor are rejected with `422 Unprocessable Entity` instead of being ignored: `n` greater than `1`, `response_format: url`, `stream`, `partial_images`, `style`, and any `quality`, `background`, or `moderation` other than `auto`. `user` is ignored, because it does not affect the generated image.
- If Vertex AI responds without an image, the gateway returns an OpenAI error instead of an empty `data` list: `400 Bad Request` with type `content_policy_violation` when a safety filter blocked the prompt or the image, and `502 Bad Gateway` otherwise, such as for a text-only answer. The error message includes the Gemini finish reason and any text the model returned.
- Token usage comes from the Gemini `usageMetadata`; output tokens include thinking tokens, and `input_tokens_details` splits prompt tokens into text and image tokens.

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-1",
    "prompt": "a serene mountain landscape at sunrise in watercolor",
    "size": "1024x1024",
    "n": 1
  }' \
  $GATEWAY_URL/v1/images/generations
```

### Audio Transcriptions

**Endpoint:** `POST /v1/audio/transcriptions`

**Status:** ✅ Supported

**Description:** Transcribe audio into text in the language of the audio.

**Features:**

- ✅ Multipart/form-data file upload (OpenAI-compatible)
- ✅ Model selection via form field `model` or `x-ai-eg-model` header
- ✅ Optional parameters: `language`, `prompt`, `response_format`, `temperature`, `timestamp_granularities[]`
- ✅ JSON and verbose JSON response formats
- ✅ Provider fallback and load balancing
- ✅ Model name virtualization (override model names for backends)

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports audio transcriptions

**Example:**

```bash
curl -F "model=whisper-1" \
  -F "file=@audio.mp3" \
  -F "language=en" \
  $GATEWAY_URL/v1/audio/transcriptions
```

### Audio Translations

**Endpoint:** `POST /v1/audio/translations`

**Status:** ✅ Supported

**Description:** Translate audio into English text.

**Features:**

- ✅ Multipart/form-data file upload (OpenAI-compatible)
- ✅ Model selection via form field `model` or `x-ai-eg-model` header
- ✅ Optional parameters: `prompt`, `response_format`, `temperature`
- ✅ Provider fallback and load balancing
- ✅ Model name virtualization (override model names for backends)

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports audio translations

**Example:**

```bash
curl -F "model=whisper-1" \
  -F "file=@audio.mp3" \
  $GATEWAY_URL/v1/audio/translations
```

### Responses

**Endpoint:** `POST /v1/responses`

**Status:** ✅ Fully Supported

**Description:** Creates a model response. Provide text or image inputs to generate text or JSON outputs. Have the model call your own custom code or use built-in tools.

**Features:**

- ✅ Streaming and non-streaming responses
- ✅ Function calling
- ✅ MCP Tools support
- ✅ Reasoning
- ✅ Multi-turn conversations
- ✅ Native multimodal support for text and images
- ✅ Response format specification (including JSON schema)
- ✅ Temperature, top_p, and other sampling parameters
- ✅ System and user messages
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- OpenAI
- Azure OpenAI with an API version that supports Responses, such as `2025-04-01-preview`
- GCP Vertex AI (Gemini models, with automatic translation; see the limitations below)
- Anthropic (with automatic translation to the Messages API)
- GCP Anthropic (with automatic translation to the Messages API)
- Any OpenAI-compatible provider (Groq, Together AI, Mistral, Tetrate Agent Router Service, etc.)

**GCP Vertex AI translation:**

Requests to a GCP Vertex AI backend are translated to the Gemini `generateContent` and `streamGenerateContent` methods. Streaming responses use the standard Responses event sequence (`response.created`, `response.output_item.added`, `response.output_text.delta`, `response.function_call_arguments.delta`, `response.completed`, and so on).

- ✅ String and message-list input, including multi-turn conversations, `instructions`, and `system` or `developer` messages
- ✅ Image inputs (`input_image` URLs and data URLs) and file inputs (`input_file` URLs and inline data)
- ✅ Function tools, `tool_choice` (`auto`, `none`, `required`, a named function, or `allowed_tools`), and `function_call` / `function_call_output` items
- ✅ `reasoning.effort` mapped to a Gemini thinking level (Gemini 3) or thinking budget (earlier models) following [Google's OpenAI compatibility table](https://ai.google.dev/gemini-api/docs/openai#thinking); `reasoning.summary` returns Gemini thoughts as reasoning summaries
- ✅ Gemini thought signatures are returned as `encrypted_content` on reasoning items. Send reasoning items back in `input` to keep multi-turn function calling working on Gemini 3 models.
- ✅ `temperature`, `top_p`, `max_output_tokens`, `presence_penalty`, `frequency_penalty`, and `text.format` (`json_object` and `json_schema`)
- ✅ Token usage, including cached and reasoning tokens

Vertex AI is stateless and does not run OpenAI built-in tools, so the gateway returns `422 Unprocessable Entity` for features it cannot honor: `previous_response_id`, `conversation`, `store: true`, `background: true`, prompt templates, `context_management`, `truncation: auto`, `service_tier` values other than `auto` or `default`, `top_logprobs`, `text.verbosity` other than `medium`, `include` values other than `reasoning.encrypted_content`, `parallel_tool_calls: false` with tools, OpenAI built-in, MCP, and custom tools, OpenAI file IDs, and input items produced by built-in tools.

**Translation to Anthropic:**

For backends with the `Anthropic` or `GCPAnthropic` schema, the gateway translates each Responses request to an Anthropic Messages request and translates the result, including the streaming event sequence, back to the Responses format. The following are translated:

- `instructions`, string `input`, and input items: user, assistant, system, and developer messages, `function_call`, `function_call_output`, and `reasoning` items. System and developer messages become the Anthropic system prompt.
- Text, image (URL or base64), and PDF (`input_file`) content.
- `function` tools, `tool_choice` (`auto`, `required`, `none`, or a named function), and `parallel_tool_calls`.
- `max_output_tokens` (required: Anthropic requires `max_tokens`, so as with Chat Completions, a request without it is sent with `max_tokens: 0` and Anthropic rejects it), `temperature` (0 to 1), `top_p`, `safety_identifier` or `user`, and `service_tier` `auto` or `default`.
- `text.format` of type `json_schema`, sent as Anthropic structured outputs.
- `reasoning.effort` and `reasoning.summary`. Models with adaptive thinking receive `thinking.type: adaptive` and `output_config.effort`. Claude 4.5 and earlier models receive extended thinking with a `budget_tokens` value derived from the effort. Thinking is returned as `reasoning` items whose `encrypted_content` lets clients send the thinking back on the next turn. Reasoning items produced by other providers are skipped, because Claude cannot use them.
- Usage, including cached and cache-write input tokens and reasoning tokens.

Requests that use a feature Anthropic cannot honor fail with HTTP 422 instead of silently dropping the feature: `previous_response_id`, `conversation`, `store: true`, `background: true`, `prompt` templates, `context_management`, `truncation: auto`, `top_logprobs`, presence and frequency penalties, `service_tier` `flex`, `scale`, or `priority`, `prompt_cache_retention: 24h`, `text.verbosity` other than `medium`, `text.format` of type `json_object`, `include` values other than `reasoning.encrypted_content`, OpenAI built-in tools (web search, file search, MCP, code interpreter, computer use, image generation, shell, custom, and others), and OpenAI file IDs. `prompt_cache_key` and `max_tool_calls` are accepted but have no effect, because they only tune OpenAI prompt caching and built-in tools.

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "what is in this image?"},
          {
            "type": "input_image",
            "image_url": "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg"
          }
        ]
      }
    ]
  }' \
  $GATEWAY_URL/v1/responses
```

### Decisions

**Endpoint:** `POST /v1/decisions`

**Status:** ✅ Supported for OpenAI backends (beta)

**Description:** Evaluate shared text or image evidence against typed questions and return probabilities, fixed choices, or rubric scores. The request and response use the native OpenAI Decisions API format. OpenAI offers this API as a beta, so its request and response formats may change.

**Features:**

- ✅ `predicate`, `choice`, and `score` questions, including multiple independent questions in one request
- ✅ `refusal` answers when the model declines an individual question
- ✅ Text input and user messages containing text and inline base64 images
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Model name virtualization, provider fallback, and load balancing
- ✅ Token usage and model metadata from the upstream response
- ❌ Streaming (not documented by the OpenAI Decisions API)
- ❌ Hosted image URLs and file IDs (not supported by the OpenAI Decisions API)

**Supported Providers:**

- OpenAI

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-6-luna",
    "input": "I was charged twice for my order.",
    "questions": [{
      "type": "choice",
      "name": "department",
      "instructions": "Which department should handle this complaint?",
      "choices": [
        {"value": "billing", "description": "Payments, invoices, and refunds."},
        {"value": "technical", "description": "Problems using the product."},
        {"value": "other", "description": "Requests outside these categories."}
      ]
    }]
  }' \
  $GATEWAY_URL/v1/decisions
```

### Responses Input Tokens

**Endpoint:** `POST /v1/responses/input_tokens`

**Status:** ✅ Supported

**Description:** Count the number of input tokens for a Responses API request without generating a response. Accepts the same request body as `/v1/responses` and returns the input token count. This is useful for validating context window fit and estimating cost before making an inference call.

**Features:**

- ✅ Same request body format as `/v1/responses`
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking
- ✅ Provider fallback and load balancing

**Supported Providers:**

- OpenAI
- Azure OpenAI (with automatic `api-version` injection)

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "input": "Hello, how are you?",
    "instructions": "You are a helpful assistant."
  }' \
  $GATEWAY_URL/v1/responses/input_tokens
```

**Response Format:**

```json
{
  "input_tokens": 15
}
```

### Rerank

**Endpoint:** `POST /cohere/v2/rerank`

**Status:** ✅ Fully Supported

**Description:** Rerank a list of documents for a given query to return relevance scores and an ordered list. Cohere-compatible API.

**Features:**

- ✅ Single-query document reranking
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- Cohere
- Any Cohere-compatible provider that supports rerank, including vLLM.

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "rerank-english-v3.0",
    "query": "What is the capital of France?",
    "documents": [
      "Paris is the capital of France.",
      "Berlin is the capital of Germany."
    ]
  }' \
  $GATEWAY_URL/cohere/v2/rerank
```

### System One (TypeSafe Jev)

**Endpoint:** `POST /typesafe/v1/systemone`

**Status:** ✅ Fully Supported

**Description:** Evaluate application state against typed questions and return structured decisions with probabilities and confidence. Native [TypeSafe AI](https://docs.typesafe.ai/api.md) API, passed through unchanged, so the official TypeSafe SDKs work by changing only the base URL.

**Features:**

- ✅ `noul` (yes/no probability), `choice` (single selection) and `score` (rubric) questions, batched in one call
- ✅ Model selection via request body or `x-ai-eg-model` header, including the `jev-latest` and `jev-preview` aliases
- ✅ Token usage tracking and cost calculation (TypeSafe bills input tokens only)
- ✅ Provider fallback and load balancing
- ❌ Streaming (not offered by the TypeSafe API)

**Supported Providers:**

- TypeSafe AI

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "jev-latest",
    "state": {"ticket": "I was charged twice for order #4471."},
    "questions": {
      "is_billing": {"type": "noul", "instructions": "Is this ticket about billing?"},
      "team": {
        "type": "choice",
        "instructions": "Which team should handle this?",
        "criteria": {"billing": null, "shipping": null, "other": null}
      }
    }
  }' \
  $GATEWAY_URL/typesafe/v1/systemone
```

### Tokenize

**Endpoint:** `POST /tokenize`

**Status:** ✅ Supported

**Description:** Count tokens for text input without generating a response. Useful for cost estimation, prompt optimization, and understanding model input limits. The request format is compatible with the [vLLM tokenize API](https://docs.vllm.ai/en/latest/api/tokenization.html).

**Features:**

- ✅ Chat message tokenization (OpenAI messages format)
- ✅ Completion prompt tokenization (single string prompt)
- ✅ Model selection via `model` field in request body
- ✅ Tool/function call tokenization support
- ✅ Provider fallback and load balancing
- ✅ Metrics support (request duration)

**Supported Providers:**

| Provider                            | API Schema     | Translation Target                                                                                                           | Notes                                                                                 |
| ----------------------------------- | -------------- | ---------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| OpenAI-compatible (e.g., vLLM)      | `OpenAI`       | Passthrough                                                                                                                  | vLLM natively supports `/tokenize`. OpenAI itself does not offer a tokenize REST API. |
| GCP Vertex AI (Gemini)              | `GCPVertexAI`  | [Gemini CountTokens API](https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/count-tokens)                 | Supports `media_resolution` parameter.                                                |
| GCP Anthropic (Claude on Vertex AI) | `GCPAnthropic` | [Anthropic MessageCountTokens API](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/count-tokens) | Uses `rawPredict` method with `count-tokens` virtual model.                           |
| AWS Bedrock                         | `AWSBedrock`   | [AWS Bedrock CountTokens API](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html)          | Supports models that implement the Converse API.                                      |
| AWS Bedrock (Anthropic)             | `AWSAnthropic` | [AWS Bedrock CountTokens API](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html)          | Uses the InvokeModel-style CountTokens API with the Anthropic Messages body.          |

**Chat Message Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.1-8B-Instruct",
    "messages": [
      {
        "role": "system",
        "content": "You are a helpful assistant."
      },
      {
        "role": "user",
        "content": "How many tokens is this message?"
      }
    ]
  }' \
  $GATEWAY_URL/tokenize
```

**Completion Prompt Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.1-8B-Instruct",
    "prompt": "Once upon a time, in a land far away"
  }' \
  $GATEWAY_URL/tokenize
```

**Response Format:**

For translated backends (GCP Vertex AI, GCP Anthropic, AWS Bedrock, AWS Bedrock Anthropic), the response contains only the token count:

```json
{
  "count": 15
}
```

For OpenAI-compatible backends that natively support tokenization (e.g., vLLM), the response contains the token count and additional fields may be present depending on request parameters:

```json
{
  "count": 15,
  "max_model_len": 131072,
  "tokens": [1234, 5678, 9012],
  "token_strs": ["Hello", " world", "!"]
}
```

**Configuration Notes:**

- For **vLLM backends**: Configure with `OpenAI` schema. vLLM natively provides `/tokenize` and the gateway passes the request through.
- For **GCP Vertex AI**: Configure with `GCPVertexAI` schema. Requests are automatically translated to the Gemini CountTokens API. Completion prompts are automatically converted to chat messages.
- For **GCP Anthropic**: Configure with `GCPAnthropic` schema. Requests are translated to the Anthropic MessageCountTokens API via `rawPredict`. Completion prompts are automatically converted to chat messages. Model version suffixes (`@default`, `@latest`) are automatically stripped.
- For **AWS Bedrock**: Configure with `AWSBedrock` schema. Requests are translated to the AWS Bedrock CountTokens API using the Converse-style input. Completion prompts are automatically converted to chat messages. Cross-region inference (CRIS) model ID prefixes are automatically stripped.
- For **AWS Bedrock (Anthropic)**: Configure with `AWSAnthropic` schema. Requests are translated to the AWS Bedrock CountTokens API using the InvokeModel-style Anthropic Messages body. Completion prompts are automatically converted to chat messages. Cross-region inference (CRIS) model ID prefixes are automatically stripped.

### Gemini Generate Content

**Endpoint:** `POST /v1beta/models/{model}:generateContent`

**Status:** ✅ Fully Supported

**Description:** Generate content using the native Gemini API format. Clients such as Gemini CLI or the Google Generative AI SDK that target the Gemini API directly (not the OpenAI-compatible shim) can be pointed at the gateway without modification.

**Features:**

- ✅ Non-streaming responses
- ✅ Function calling
- ✅ Token usage tracking and cost calculation
- ✅ System instructions
- ✅ Safety settings and provider-specific fields
- ✅ Model selection from URL path

**Supported Providers:**

- Google Vertex AI (with automatic path rewriting)

**Example:**

```bash
curl -s -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {
        "role": "user",
        "parts": [{"text": "Hello, how are you?"}]
      }
    ]
  }' \
  $GATEWAY_URL/v1beta/models/gemini-3-flash-preview:generateContent
```

### Gemini Stream Generate Content

**Endpoint:** `POST /v1beta/models/{model}:streamGenerateContent`

**Status:** Supported for native passthrough to Google Vertex AI

**Description:** Stream generated content using the native Gemini API format. Returns Server-Sent Events (SSE).

**Features:**

- ✅ Streaming responses (SSE)
- ✅ Native request and response preservation, including function calling
- ✅ Token usage tracking and cost calculation
- ✅ System instructions
- ✅ Safety settings and provider-specific fields
- ✅ Model selection from URL path

**Supported Providers:**

- Google Vertex AI (with automatic path rewriting)

**Example:**

```bash
curl -s -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {
        "role": "user",
        "parts": [{"text": "Tell me a story."}]
      }
    ]
  }' \
  $GATEWAY_URL/v1beta/models/gemini-3-flash-preview:streamGenerateContent
```

### Models

**Endpoint:** `GET /v1/models`

**Description:** List available models configured in the AI Gateway.

**Features:**

- ✅ Returns models declared in AIGatewayRoute configurations
- ✅ OpenAI-compatible response format
- ✅ Model metadata (ID, owned_by, created timestamp)

**Example:**

```bash
curl $GATEWAY_URL/v1/models
```

**Response Format:**

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-4o-mini",
      "object": "model",
      "created": 1677610602,
      "owned_by": "openai"
    }
  ]
}
```

## Provider-Endpoint Compatibility Table

The following table summarizes which providers support which endpoints:

| Provider                                                                                              | Chat Completions | Completions | Embeddings | Image Generation | Anthropic Messages | Count Tokens | Rerank | System One | Tokenize | Notes                                                                                                                |
| ----------------------------------------------------------------------------------------------------- | :--------------: | :---------: | :--------: | :--------------: | :----------------: | :----------: | :----: | :--------: | :------: | -------------------------------------------------------------------------------------------------------------------- |
| [OpenAI](https://platform.openai.com/docs/api-reference)                                              |        ✅        |     ✅      |     ✅     |        ❌        |         ✅         |      ❌      |   ❌   |     ❌     |    ❌    | OpenAI does not offer a tokenize REST API                                                                            |
| [AWS Bedrock](https://docs.aws.amazon.com/bedrock/latest/APIReference/)                               |        ✅        |     🚧      |     ✅     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via API translation (embeddings: Titan models only)                                                                  |
| [Azure OpenAI](https://learn.microsoft.com/en-us/azure/ai-services/openai/reference)                  |        ✅        |     🚧      |     ✅     |        ❌        |         ⚠️         |      ❌      |   ❌   |     ❌     |    ❌    | Via API translation or via [OpenAI-compatible API](https://learn.microsoft.com/en-us/azure/ai-foundry/openai/latest) |
| [Google Gemini](https://ai.google.dev/gemini-api/docs/openai)                                         |        ✅        |     ⚠️      |     ✅     |        ⚠️        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Google AI Studio (native)](https://ai.google.dev/api/rest)                                           |        ❌        |     ❌      |     ❌     |        ⚠️        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Native `generateContent` via `GoogleAIStudio` schema; image generation only                                          |
| [Groq](https://console.groq.com/docs/openai)                                                          |        ✅        |     ❌      |     ❌     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Grok](https://docs.x.ai/docs/api-reference)                                                          |        ✅        |     ⚠️      |     ❌     |        ⚠️        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Together AI](https://docs.together.ai/docs/openai-api-compatibility)                                 |        ⚠️        |     ⚠️      |     ⚠️     |        ⚠️        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Cohere](https://docs.cohere.com/v2/docs/compatibility-api)                                           |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |      ❌      |   ✅   |     ❌     |    ❌    | Via OpenAI-compatible API and Cohere V2 API for rerank                                                               |
| [Mistral](https://docs.mistral.ai/api/)                                                               |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [DeepInfra](https://deepinfra.com/docs/inference)                                                     |        ✅        |     ⚠️      |     ✅     |        ⚠️        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [DeepSeek](https://api-docs.deepseek.com/)                                                            |        ⚠️        |     ⚠️      |     ❌     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Heabsy](https://api.heabsy.com/docs)                                                                 |        ⚠️        |     ❌      |     ❌     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Hunyuan](https://cloud.tencent.com/document/product/1729/111007)                                     |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Tencent LLM Knowledge Engine](https://www.tencentcloud.com/document/product/1255/70381)              |        ⚠️        |     ❌      |     ❌     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Tetrate Agent Router Service (TARS)](https://router.tetrate.ai/)                                     |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Google Vertex AI](https://cloud.google.com/vertex-ai/docs/reference/rest)                            |        ✅        |     🚧      |     ✅     |        ✅        |         ❌         |      ❌      |   ❌   |     ❌     |    ✅    | Via API translation                                                                                                  |
| [Anthropic on Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude) |        ✅        |     ❌      |     🚧     |        ❌        |         ✅         |      ✅      |   ❌   |     ❌     |    ✅    | Via API translation                                                                                                  |
| [Anthropic on AWS Bedrock](https://aws.amazon.com/bedrock/anthropic/)                                 |        🚧        |     ❌      |     ❌     |        ❌        |         ✅         |      ✅      |   ❌   |     ❌     |    ✅    | Native Anthropic API                                                                                                 |
| [SambaNova](https://docs.sambanova.ai/sambastudio/latest/open-ai-api.html)                            |        ✅        |     ⚠️      |     ✅     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Anthropic](https://docs.claude.com/en/home)                                                          |        ✅        |     ❌      |     ❌     |        ❌        |         ✅         |      ✅      |   ❌   |     ❌     |    ❌    | Via OpenAI-compatible API and Native Anthropic API                                                                   |
| [vLLM](https://docs.vllm.ai/en/latest/)                                                               |        ✅        |     ✅      |     ✅     |        ❌        |         ❌         |      ❌      |   ❌   |     ❌     |    ✅    | Via OpenAI-compatible API; native `/tokenize` support                                                                |
| [TypeSafe AI](https://docs.typesafe.ai/api.md)                                                        |        ❌        |     ❌      |     ❌     |        ❌        |         ❌         |      ❌      |   ❌   |     ✅     |    ❌    | Native System One API for the Jev decision model                                                                     |

- ✅ - Supported and Tested on Agent Router CI
- ⚠️️ - Expected to work based on provider documentation, but not tested on the CI.
- ❌ - Not supported according to provider documentation.
- 🚧 - Unimplemented, or under active development but planned for future releases

## Custom endpoint prefixes

By default, the gateway registers provider endpoints under these prefixes:

- OpenAI: `/`
- Cohere: `/cohere`
- Anthropic: `/anthropic`
- TypeSafe: `/typesafe`

You can override them via Helm using values under `endpointConfig`:

```yaml
# values.yaml
endpointConfig:
  # Explicit provider roots
  openai: ""
  cohere: "/cohere"
  anthropic: "/anthropic"
  typesafe: "/typesafe"
  # rootPrefix applies to all routes; final paths are <rootPrefix><providerPrefix>/...
  # endpointConfig:
  #   rootPrefix: "/"
```

Or with helm CLI:

```bash
helm upgrade --install ai-gateway envoyproxy/ai-gateway-helm \
  -n envoy-ai-gateway-system --create-namespace \
  --set 'endpointConfig.openai=/' \
  --set 'endpointConfig.cohere=/cohere' \
  --set 'endpointConfig.anthropic=/anthropic' \
  --set 'endpointConfig.typesafe=/typesafe'
```

Notes:

- `endpointConfig.rootPrefix` (default `/`) is prepended to all provider prefixes.
- Only these keys are accepted: `openai`, `cohere`, `anthropic`, `typesafe`.
- If any key is omitted or empty, defaults are applied as listed above.

## What's Next

To learn more about configuring and using the Agent Router with these endpoints:

- **[Supported Providers](./supported-providers.md)** - Complete list of supported AI providers and their configurations
- **[Usage-Based Rate Limiting](../traffic/usage-based-ratelimiting.md)** - Configure token-based rate limiting and cost controls
- **[Provider Fallback](../traffic/provider-fallback.md)** - Set up automatic failover between providers for high availability
- **[Metrics and Monitoring](../observability/metrics.md)** - Monitor usage, costs, and performance metrics

[issue#609]: https://github.com/theagentrouter/agent-router/issues/609
