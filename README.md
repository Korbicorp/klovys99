# klovys99

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

klovys99 is a local reverse proxy that anonymizes sensitive prompt data before
forwarding requests to Anthropic or OpenAI APIs.

It is designed to sit between coding clients such as Claude Code or Codex and
their upstream API, replacing detected personal or sensitive values with stable
pseudonym tokens before the request leaves the machine.

## Features

- Local reverse proxy for Anthropic and OpenAI-compatible JSON requests.
- `npm install` workflow that builds the Go binary locally and exposes a
  `klovys99` command.
- Client configuration helpers for Codex and Claude Code.
- Built-in deterministic detectors for common PII and sensitive identifiers.
- Dynamic detector loading from the official Gitleaks and Microsoft Presidio
  rule sources.
- Optional local LLM extraction through Ollama for contextual names, addresses,
  dates, and vehicle plates.
- Stable pseudonym tokens for the lifetime of the proxy process.
- Structured logs with anonymization counters instead of raw prompt values.
- Disk cache for downloaded external rules to avoid repeated network fetches on
  every startup.

## Requirements

- Node.js 18 or newer.
- Go 1.25 or newer.
- Network access on first startup to download the default Gitleaks and Presidio
  rule sources.
- An Anthropic API key, Claude subscription, or OpenAI API key depending on the
  client you route through Klovys99.
- Ollama, only when `KLOVIS_LLM_ENABLED=true`.

Check your local tooling:

```sh
node -v
npm -v
go version
```

Optional LLM mode requires a local Ollama model:

```sh
ollama --version
ollama pull mistral
```

## Installation

From the repository root:

```sh
npm install
```

`npm install` runs a `postinstall` step that builds the Go proxy into `dist/`
and exposes the CLI entrypoint `klovys99`.

If you want the install step to also update your client configuration
immediately:

```sh
KLOVIS_CLIENT=claude npm install
```

Supported values are `codex`, `claude`, and `both`.

## Quick Start

Configure one or both clients to point to Klovys99:

```sh
npx klovys99 configure codex
npx klovys99 configure claude
```

Or configure both at once:

```sh
npx klovys99 configure both
```

Then start the proxy:

```sh
npx klovys99 start
```

By default, Klovys99 listens on `http://127.0.0.1:8080` and exposes these local
routes:

- `http://127.0.0.1:8080/anthropic` for Claude Code and other Anthropic clients
- `http://127.0.0.1:8080/openai/v1` for Codex and other OpenAI-compatible
  clients

The historical unprefixed route still exists and forwards to
`KLOVIS_TARGET_URL`, which defaults to `https://api.anthropic.com`.

## Client Configuration

### Codex

```sh
npx klovys99 configure codex
```

This updates `~/.codex/config.toml` and sets:

```toml
openai_base_url = "http://127.0.0.1:8080/openai/v1"
```

### Claude Code

```sh
npx klovys99 configure claude
```

This updates `~/.claude/settings.json` and sets:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8080/anthropic"
  }
}
```

If you want another listen URL written into both clients, pass `--base-url`:

```sh
npx klovys99 configure both --base-url http://127.0.0.1:9090
```

## Quick API Checks

Anthropic-style request through Klovys99:

```sh
curl http://127.0.0.1:8080/anthropic/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{
    "model": "claude-sonnet-4-5",
    "max_tokens": 128,
    "messages": [
      {
        "role": "user",
        "content": "Email Alice at alice@example.com"
      }
    ]
  }'
```

OpenAI Responses-style request through Klovys99:

```sh
curl http://127.0.0.1:8080/openai/v1/responses \
  -H "authorization: Bearer $OPENAI_API_KEY" \
  -H "content-type: application/json" \
  -d '{
    "model": "gpt-5",
    "input": "Email Alice at alice@example.com"
  }'
```

Upstream providers receive the same request shape, with sensitive values
replaced by pseudonym tokens such as `[EMAIL_1]`.

## How It Works

Klovys99 reads each incoming JSON request body, anonymizes supported prompt
content, then forwards the modified request to the configured upstream.

The proxy anonymizes:

- every `<session>...</session>` block found anywhere in a JSON request body;
- text content in prompts, system messages, `<system-reminder>` blocks, text
  file context, and tool results;
- text document sources where `source.type` is `text`.

Structural metadata such as model names, roles, content block types, tool IDs,
tool names, media types, cache-control values, and base64 document data is left
unchanged so the upstream request shape remains valid.

For a single proxy process, repeated values are mapped to stable tokens. For
example, the same email address is replaced by the same `[EMAIL_N]` token across
requests handled by that process.

When matches overlap, the detector with the highest priority wins. If priorities
are equal, the longest match wins.

## Configuration

Klovys99 runtime is configured with environment variables.

| Variable | Default | Description |
| --- | --- | --- |
| `KLOVIS_ADDR` | `127.0.0.1:8080` | Listen address for the local proxy. |
| `KLOVIS_TARGET_URL` | `https://api.anthropic.com` | Upstream used by legacy unprefixed routes such as `/v1/messages`. |
| `KLOVIS_ANTHROPIC_TARGET_URL` | `https://api.anthropic.com` | Upstream used by `/anthropic/...` routes. |
| `KLOVIS_OPENAI_TARGET_URL` | `https://api.openai.com` | Upstream used by `/openai/...` routes. |
| `KLOVIS_PROXY_DEBUG` | `false` | Enables debug traffic body logging when set to `true`. |
| `KLOVIS_LOG_TO_FILE` | `false` | Writes logs to `proxy.log` instead of stdout when set to `true`. |
| `KLOVIS_LLM_ENABLED` | `false` | Enables optional local LLM extraction through Ollama. |
| `KLOVIS_LLM_URL` | `http://localhost:11434` | Ollama base URL. |
| `KLOVIS_LLM_MODEL` | `mistral` | Ollama model used for entity extraction. |
| `KLOVIS_LLM_TIMEOUT` | `30s` | Startup and request timeout for LLM calls. |
| `KLOVIS_LLM_MAX_CHARS` | `1000` | Maximum input bytes sent to the LLM per chunk. |
| `KLOVIS_LLM_AUTOSTART` | `false` | Starts `ollama serve` automatically when the Ollama URL is local and not already reachable. |

The npm wrapper also honors:

| Variable | Description |
| --- | --- |
| `KLOVIS_CLIENT` | Client to configure during `npm install`: `codex`, `claude`, or `both`. |
| `KLOVIS_BASE_URL` | Base URL written by `klovys99 configure` or `npm install` auto-configuration. |
| `KLOVIS_SKIP_BUILD` | Skips the Go build during `postinstall` when set to `true`. |
| `KLOVIS_SKIP_CONFIGURE` | Skips client configuration during `postinstall` when set to `true`. |

Boolean variables accept only `true` or `false`.

## Logs

Klovys99 writes structured application logs to stdout by default. To write logs to
`proxy.log` instead, enable file logging:

```sh
KLOVIS_LOG_TO_FILE=true npx klovys99 start
```

To also inspect the final request body sent upstream after anonymization, enable
debug logging:

```sh
KLOVIS_LOG_TO_FILE=true KLOVIS_PROXY_DEBUG=true npx klovys99 start
```

Use debug mode carefully, because it records the anonymized upstream request body
in whichever log destination is configured.

## Optional LLM Extraction

LLM extraction is disabled by default. Enable it with:

```sh
KLOVIS_LLM_ENABLED=true npx klovys99 start
```

When enabled, Klovys99 checks the Ollama connection during startup and runs a small
extraction probe before accepting traffic. If startup verification fails, the
proxy exits.

By default, Klovys99 does not start Ollama for you. Start Ollama separately before
enabling LLM extraction, or opt in to local autostart:

```sh
KLOVIS_LLM_ENABLED=true KLOVIS_LLM_AUTOSTART=true npx klovys99 start
```

Autostart only applies to local Ollama URLs such as `http://localhost:11434` or
loopback IP addresses. Remote Ollama URLs are never started by Klovys99.

Deterministic detectors remain the baseline. LLM matches are added when
available and have lower priority than deterministic regex, Gitleaks, and
Presidio matches. If the LLM fails during a request, Klovys99 logs the technical
error and continues with deterministic anonymization.

## Detectors

Klovys99 combines built-in detectors with external rules loaded at startup.
External rule payloads are cached for 24 hours in the user cache directory under
`klovys99/external-rules`.

| Category | Source | Priority | Description |
| --- | --- | ---: | --- |
| `EMAIL` | Built-in / Presidio | 1000 / 600 | Email addresses, normalized in lowercase for stable tokens. |
| `NIR` | Built-in | 1000 | French social security numbers, including spaced formats and Corsica departments `2A` and `2B`. |
| `IBAN` | Built-in / Presidio | 1000 / 600 | IBAN-like account identifiers, normalized by removing separators. |
| `IP` | Built-in / Presidio | 900 / 600 | IPv4 and IPv6 addresses. |
| `CREDIT_CARD` | Built-in / Presidio | 900 / 600 | Credit card-like digit sequences. |
| `MAC_ADDRESS` | Built-in / Presidio | 900 / 600 | MAC addresses with `:` or `-` separators. |
| `PHONE` | Built-in | 700 | French and common international phone numbers. |
| `DATE` | Built-in / Presidio / LLM | 600 / external / 50 | Conservatively labelled birth dates and supported contextual dates. |
| `BLOOD_TYPE` | Built-in | 600 | Contextual blood groups such as `Groupe sanguin O+`. |
| `SECRET` | Gitleaks | 600 | Secrets loaded dynamically from the official Gitleaks config. |
| `CRYPTO` | Presidio | 600 | Cryptocurrency wallet identifiers loaded from supported Presidio recognizers. |
| `ADDRESS` | Built-in / LLM | 900 / 700 / 50 | French postal addresses, labelled addresses, and optional contextual LLM matches. |
| `NAME` | Built-in | 900 | Contextual names following strong French or English cues and form labels. |
| `FIRST_NAME` | Built-in | 500 | Conservatively labelled first names. |
| `LAST_NAME` | Built-in | 500 | Conservatively labelled last names. |
| `NUMERIC_ID` | Built-in | 100 | Generic long numeric IDs. |
| `REFERENCE_ID` | Built-in | 100 | Labelled alphanumeric references requiring letters and digits. |
| `PERSON_NAME` | LLM | 50 | Contextual full names found by the local model. |
| `DATE` | LLM / Presidio | 50 / 600 | Dates tied to identity, family, documents, health, work, or events. |
| `VEHICLE_PLATE` | LLM | 50 | Vehicle registration plates found by the local model. |

## Claude Code Notes

When Claude Code uses a non-first-party `ANTHROPIC_BASE_URL`, some Claude
features behave differently upstream. In practice:

- Remote Control is disabled by Claude Code when the base URL does not point to
  `api.anthropic.com`.
- Tool search behavior changes when routing through a proxy. If you need
  deferred tool references, set `ENABLE_TOOL_SEARCH=true` in your Claude
  environment because Klovys99 forwards `tool_reference` blocks unchanged.

## Development

Clone the repository and install both Node and Go dependencies:

```sh
git clone https://github.com/Korbicorp/klovys99.git
cd klovys99
npm install
go mod download
```

Run the test suites:

```sh
go test ./...
node --test npm/test/*.test.js
```

Run the proxy locally without npm:

```sh
go run ./cmd/klovys99
```

Format Go code before submitting changes:

```sh
gofmt -w ./cmd ./internal
```

## Security Notes

Klovys99 reduces the amount of sensitive data sent upstream, but it is not a
formal data-loss-prevention guarantee. Review detector coverage for your own
threat model before using it with production data.

External Gitleaks and Presidio rules are loaded from their upstream repositories
by default. Cached copies are reused for 24 hours and stale cache entries may be
used as a fallback if a refresh fails.

## Contributing

Issues and pull requests are welcome. For code changes, please include focused
tests that cover the behavior being changed.

Useful checks before opening a pull request:

```sh
go test ./...
node --test npm/test/*.test.js
gofmt -w ./cmd ./internal
```
