#!/usr/bin/env -S uv run --script
# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "google-auth>=2.40.0",
#   "google-genai>=1.62.0",
# ]
# ///

"""Probe native Gemini generateContent support through Envoy AI Gateway.

Examples:
  # Establish that the model and service account work directly on Vertex AI.
  uv run tests/manual/native_gemini_features.py --mode direct

  # Exercise the same feature matrix through a locally exposed gateway.
  uv run tests/manual/native_gemini_features.py \
    --mode gateway --gateway-url http://localhost:8080

  # Continuously retry only unsupported features while developing the gateway.
  uv run tests/manual/native_gemini_features.py \
    --mode gateway --gateway-url http://localhost:8080 \
    --repeat-failures --interval 2
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import sys
import time
import traceback
from collections.abc import Callable, Iterable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from google import genai
from google.genai import types
from google.oauth2 import service_account

DEFAULT_CREDENTIALS = Path.home() / ".secrets" / "service_account.json"
DEFAULT_MODEL = "gemini-3.6-flash"

# A valid 1x1 transparent PNG. Keeping it inline makes the probe deterministic.
TINY_PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUB"
    "AScY42YAAAAASUVORK5CYII="
)


@dataclass
class Probe:
    name: str
    description: str
    run: Callable[[genai.Client, str], str]


@dataclass
class Result:
    target: str
    probe: Probe
    passed: bool
    detail: str
    elapsed: float


def require_text(response: Any) -> str:
    text = response.text
    if not text or not text.strip():
        raise AssertionError("response did not contain text")
    return text.strip()


def require_usage(response: Any) -> None:
    usage = response.usage_metadata
    if usage is None or not usage.total_token_count:
        raise AssertionError("response did not contain usageMetadata.totalTokenCount")


def probe_text(client: genai.Client, model: str) -> str:
    response = client.models.generate_content(
        model=model,
        contents="Reply with exactly: native-gemini-ok",
    )
    require_usage(response)
    return require_text(response)


def probe_config(client: genai.Client, model: str) -> str:
    response = client.models.generate_content(
        model=model,
        contents="Reply with the single word low.",
        config=types.GenerateContentConfig(
            system_instruction="Follow the requested output format exactly.",
            temperature=0,
            top_p=0.8,
            max_output_tokens=32,
            stop_sequences=["STOP-NOW"],
        ),
    )
    require_usage(response)
    return require_text(response)


def probe_structured_json(client: genai.Client, model: str) -> str:
    schema = {
        "type": "object",
        "properties": {
            "status": {"type": "string", "enum": ["ok"]},
            "count": {"type": "integer"},
        },
        "required": ["status", "count"],
        "additionalProperties": False,
    }
    response = client.models.generate_content(
        model=model,
        contents="Return status ok and count 3.",
        config=types.GenerateContentConfig(
            response_mime_type="application/json",
            response_json_schema=schema,
            temperature=0,
        ),
    )
    parsed = json.loads(require_text(response))
    if parsed.get("status") != "ok" or parsed.get("count") != 3:
        raise AssertionError(f"unexpected structured response: {parsed!r}")
    return json.dumps(parsed, sort_keys=True)


def probe_safety(client: genai.Client, model: str) -> str:
    response = client.models.generate_content(
        model=model,
        contents="Say hello in a friendly way.",
        config=types.GenerateContentConfig(
            safety_settings=[
                types.SafetySetting(
                    category="HARM_CATEGORY_HATE_SPEECH",
                    threshold="BLOCK_ONLY_HIGH",
                )
            ]
        ),
    )
    return require_text(response)


def probe_inline_image(client: genai.Client, model: str) -> str:
    response = client.models.generate_content(
        model=model,
        contents=[
            "The attached image is one pixel. Reply with the word pixel.",
            types.Part.from_bytes(data=TINY_PNG, mime_type="image/png"),
        ],
    )
    return require_text(response)


def weather_tool() -> types.Tool:
    return types.Tool(
        function_declarations=[
            types.FunctionDeclaration(
                name="get_weather",
                description="Get deterministic weather for a city.",
                parameters_json_schema={
                    "type": "object",
                    "properties": {"city": {"type": "string"}},
                    "required": ["city"],
                },
            )
        ]
    )


def probe_function_call(client: genai.Client, model: str) -> str:
    response = client.models.generate_content(
        model=model,
        contents="Call get_weather for Madrid.",
        config=types.GenerateContentConfig(
            tools=[weather_tool()],
            automatic_function_calling=types.AutomaticFunctionCallingConfig(
                disable=True
            ),
            tool_config=types.ToolConfig(
                function_calling_config=types.FunctionCallingConfig(mode="ANY")
            ),
        ),
    )
    calls = response.function_calls or []
    if not calls or calls[0].name != "get_weather":
        raise AssertionError(f"expected get_weather function call, got {calls!r}")
    return f"{calls[0].name}({calls[0].args})"


def probe_function_response(client: genai.Client, model: str) -> str:
    config = types.GenerateContentConfig(
        tools=[weather_tool()],
        automatic_function_calling=types.AutomaticFunctionCallingConfig(disable=True),
        tool_config=types.ToolConfig(
            function_calling_config=types.FunctionCallingConfig(mode="ANY")
        ),
    )
    user = types.Content(
        role="user",
        parts=[types.Part.from_text(text="Call get_weather for Madrid.")],
    )
    first = client.models.generate_content(model=model, contents=[user], config=config)
    calls = first.function_calls or []
    if not calls:
        raise AssertionError("model did not return a function call")
    function_response = types.Content(
        role="tool",
        parts=[
            types.Part.from_function_response(
                name=calls[0].name,
                response={"temperature_c": 21, "condition": "sunny"},
            )
        ],
    )
    second = client.models.generate_content(
        model=model,
        contents=[user, first.candidates[0].content, function_response],
        config=types.GenerateContentConfig(tools=[weather_tool()]),
    )
    return require_text(second)


def probe_streaming(client: genai.Client, model: str) -> str:
    chunks = client.models.generate_content_stream(
        model=model,
        contents="Count from one to three, using words only.",
    )
    texts: list[str] = []
    saw_usage = False
    for chunk in chunks:
        if chunk.text:
            texts.append(chunk.text)
        if chunk.usage_metadata and chunk.usage_metadata.total_token_count:
            saw_usage = True
    text = "".join(texts).strip()
    if not text:
        raise AssertionError("stream did not contain text")
    if not saw_usage:
        raise AssertionError("stream did not contain usage metadata")
    return text


def probe_multi_turn(client: genai.Client, model: str) -> str:
    chat = client.chats.create(
        model=model,
        config=types.GenerateContentConfig(
            thinking_config=types.ThinkingConfig(include_thoughts=True)
        ),
    )
    first = chat.send_message("Remember the code word cobalt. Reply briefly.")
    require_text(first)
    second = chat.send_message("What code word did I ask you to remember?")
    text = require_text(second)
    if "cobalt" not in text.lower():
        raise AssertionError(f"multi-turn context was not retained: {text!r}")
    return text


PROBES = [
    Probe("text", "basic text and usage metadata", probe_text),
    Probe("config", "system instruction and generation controls", probe_config),
    Probe("structured-json", "JSON response schema", probe_structured_json),
    Probe("safety", "safety settings", probe_safety),
    Probe("inline-image", "inline multimodal bytes", probe_inline_image),
    Probe("function-call", "function declaration and call", probe_function_call),
    Probe(
        "function-response",
        "function response and signature replay",
        probe_function_response,
    ),
    Probe("streaming", "SSE text and usage metadata", probe_streaming),
    Probe("multi-turn", "chat history and thought-signature replay", probe_multi_turn),
]


def project_from_credentials(path: Path) -> str:
    with path.open(encoding="utf-8") as file:
        project = json.load(file).get("project_id")
    if not project:
        raise ValueError(f"project_id is missing from {path}")
    return project


def direct_client(args: argparse.Namespace) -> genai.Client:
    credentials = service_account.Credentials.from_service_account_file(
        args.credentials,
        scopes=["https://www.googleapis.com/auth/cloud-platform"],
    )
    project = args.project or project_from_credentials(Path(args.credentials))
    return genai.Client(
        vertexai=True,
        credentials=credentials,
        project=project,
        location=args.location,
        http_options=types.HttpOptions(api_version="v1"),
    )


def parse_headers(values: Iterable[str]) -> dict[str, str]:
    headers: dict[str, str] = {}
    for value in values:
        name, separator, header_value = value.partition(":")
        if not separator or not name.strip() or not header_value.strip():
            raise ValueError(f"invalid header {value!r}; expected 'Name: value'")
        headers[name.strip()] = header_value.strip()
    return headers


def gateway_client(args: argparse.Namespace) -> genai.Client:
    if not args.gateway_url:
        raise ValueError(
            "--gateway-url or GEMINI_GATEWAY_URL is required in gateway mode"
        )
    headers = parse_headers(args.header)
    # Developer API mode emits /v1beta/models/{model}:generateContent. The
    # placeholder key only satisfies SDK validation; backend auth is gateway-managed.
    return genai.Client(
        api_key="gateway-managed",
        http_options=types.HttpOptions(
            base_url=args.gateway_url.rstrip("/"),
            api_version="v1beta",
            headers=headers,
            timeout=args.timeout * 1000,
        ),
    )


def selected_probes(names: list[str]) -> list[Probe]:
    if not names:
        return PROBES
    known = {probe.name: probe for probe in PROBES}
    unknown = sorted(set(names) - known.keys())
    if unknown:
        raise ValueError(f"unknown probes: {', '.join(unknown)}")
    return [known[name] for name in names]


def run_round(
    clients: dict[str, genai.Client], probes: list[Probe], model: str, verbose: bool
) -> list[Result]:
    results: list[Result] = []
    for target, client in clients.items():
        for probe in probes:
            started = time.monotonic()
            try:
                detail = probe.run(client, model).replace("\n", " ")[:160]
                result = Result(target, probe, True, detail, time.monotonic() - started)
            except Exception as error:  # noqa: BLE001 - this is a diagnostic runner.
                if verbose:
                    traceback.print_exc()
                detail = f"{type(error).__name__}: {error}".replace("\n", " ")[:300]
                result = Result(
                    target, probe, False, detail, time.monotonic() - started
                )
            results.append(result)
            status = "PASS" if result.passed else "FAIL"
            print(
                f"[{status}] {target:7} {probe.name:18} {result.elapsed:6.2f}s  {result.detail}",
                flush=True,
            )
    return results


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument(
        "--mode", choices=["direct", "gateway", "both"], default="gateway"
    )
    result.add_argument("--gateway-url", default=os.getenv("GEMINI_GATEWAY_URL"))
    result.add_argument("--credentials", default=str(DEFAULT_CREDENTIALS))
    result.add_argument("--project", default=os.getenv("GOOGLE_CLOUD_PROJECT"))
    result.add_argument("--location", default="global")
    result.add_argument("--model", default=os.getenv("GEMINI_MODEL", DEFAULT_MODEL))
    result.add_argument(
        "--header", action="append", default=[], help="Gateway header, repeatable"
    )
    result.add_argument(
        "--probe", action="append", default=[], help="Run only this named probe"
    )
    result.add_argument("--list", action="store_true", help="List probes and exit")
    result.add_argument("--repeat-failures", action="store_true")
    result.add_argument("--interval", type=float, default=2.0)
    result.add_argument("--max-rounds", type=int, default=0, help="0 means unlimited")
    result.add_argument(
        "--timeout", type=int, default=120, help="Per-request timeout in seconds"
    )
    result.add_argument("--verbose", action="store_true")
    return result


def main() -> int:
    args = parser().parse_args()
    if args.list:
        for probe in PROBES:
            print(f"{probe.name:18} {probe.description}")
        return 0

    probes = selected_probes(args.probe)
    clients: dict[str, genai.Client] = {}
    if args.mode in {"direct", "both"}:
        clients["direct"] = direct_client(args)
    if args.mode in {"gateway", "both"}:
        clients["gateway"] = gateway_client(args)

    round_number = 0
    try:
        while True:
            round_number += 1
            print(f"\nRound {round_number}: model={args.model} probes={len(probes)}")
            results = run_round(clients, probes, args.model, args.verbose)
            failures = [result for result in results if not result.passed]
            passed = len(results) - len(failures)
            print(f"Summary: {passed}/{len(results)} passed")
            if not failures:
                return 0
            if not args.repeat_failures:
                return 1
            if args.max_rounds and round_number >= args.max_rounds:
                return 1
            failed_names = {result.probe.name for result in failures}
            probes = [probe for probe in probes if probe.name in failed_names]
            print(f"Retrying {len(probes)} failing probes in {args.interval:g}s...")
            time.sleep(args.interval)
    except KeyboardInterrupt:
        print("\nInterrupted", file=sys.stderr)
        return 130
    finally:
        for client in clients.values():
            client.close()


if __name__ == "__main__":
    raise SystemExit(main())
