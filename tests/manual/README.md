# Manual provider probes

## Native Gemini

`native_gemini_features.py` is a PEP 723 script that uses the official
`google-genai` SDK to exercise Gemini's native `generateContent` and
`streamGenerateContent` API shapes.

First verify the service account and model directly against Vertex AI:

```shell
uv run tests/manual/native_gemini_features.py --mode direct
```

The defaults match the local development credential:

- Credentials: `~/.secrets/service_account.json`
- Project: read from the service account's `project_id`
- Location: `global`
- Model: `gemini-3.6-flash`

Run the gateway locally from source in one terminal. This launches the current
`aigw` and extproc implementation, Envoy Gateway, and Envoy without Kubernetes:

```shell
go run ./cmd/aigw run examples/aigw/gemini-vertex.yaml --debug
```

The standalone configuration reads `~/.secrets/service_account.json` through
`aigw run`'s file-substitution annotation and listens on `localhost:1975`.
Override its defaults with `GOOGLE_APPLICATION_CREDENTIALS`,
`GOOGLE_CLOUD_PROJECT`, or `GEMINI_MODEL`.

Then run the same feature matrix through it from another terminal:

```shell
uv run tests/manual/native_gemini_features.py \
  --mode gateway \
  --gateway-url http://localhost:1975
```

The gateway's `BackendSecurityPolicy` supplies the upstream GCP credential.
The script's placeholder API key only satisfies SDK client-side validation and
must not be forwarded upstream.

Use `--mode both` to compare direct Vertex AI and gateway behavior in one run.
Use `--repeat-failures` during development to continuously rerun only failing
features:

```shell
uv run tests/manual/native_gemini_features.py \
  --mode both \
  --gateway-url http://localhost:1975 \
  --repeat-failures \
  --interval 2
```

List or select probes with `--list` and repeatable `--probe NAME` arguments.
Pass gateway-specific routing or authentication headers with repeatable
`--header 'Name: value'` arguments.
