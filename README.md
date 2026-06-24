# Klovis

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Klovis is a local Anthropic reverse proxy that anonymizes sensitive prompt data
before forwarding requests to the Anthropic API.

It is designed to sit between an Anthropic-compatible client and
`https://api.anthropic.com`, replacing detected personal or sensitive values with
stable pseudonym tokens before the request leaves the machine.

## Features

- Local reverse proxy for Anthropic API requests.
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

- Go 1.25 or newer.
- Network access on first startup to download the default Gitleaks and Presidio
  rule sources.
- An Anthropic API key for upstream requests.
- Ollama, only when `KLOVIS_LLM_ENABLED=true`.

Check your Go installation:

```sh
go version
```

Optional LLM mode requires a local Ollama model:

```sh
ollama --version
ollama pull mistral
```

## Installation

Install from source:

```sh
git clone https://github.com/Korbicorp/klovis.git
cd klovis
go build -o klovis ./cmd/klovis
```

Or run it directly without building a binary:

```sh
go run ./cmd/klovis
```

## Quick Start

Start the proxy:

```sh
go run ./cmd/klovis
```

By default, Klovis listens on `http://localhost:8080` and forwards requests to
`https://api.anthropic.com`.

Send Anthropic requests to the local proxy while keeping the usual Anthropic
headers:

```sh
curl http://localhost:8080/v1/messages \
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

The upstream Anthropic API receives the same request shape, with sensitive values
replaced by pseudonym tokens such as `[EMAIL_1]`.

## How It Works

Klovis reads each incoming request body, anonymizes supported JSON prompt
content, then forwards the modified request to Anthropic.

The proxy anonymizes:

- every `<session>...</session>` block found anywhere in a JSON request body;
- user message content outside `<system-reminder>...</system-reminder>` blocks.

For a single proxy process, repeated values are mapped to stable tokens. For
example, the same email address is replaced by the same `[EMAIL_N]` token across
requests handled by that process.

When matches overlap, the detector with the highest priority wins. If priorities
are equal, the longest match wins.

## Configuration

Klovis is configured with environment variables.

| Variable | Default | Description |
| --- | --- | --- |
| `KLOVIS_PROXY_DEBUG` | `false` | Enables debug traffic logging when set to `true`. |
| `KLOVIS_LLM_ENABLED` | `false` | Enables optional local LLM extraction through Ollama. |
| `KLOVIS_LLM_URL` | `http://localhost:11434` | Ollama base URL. |
| `KLOVIS_LLM_MODEL` | `mistral` | Ollama model used for entity extraction. |
| `KLOVIS_LLM_TIMEOUT` | `30s` | Startup and request timeout for LLM calls. |
| `KLOVIS_LLM_MAX_CHARS` | `1000` | Maximum input bytes sent to the LLM per chunk. |
| `KLOVIS_LLM_AUTOSTART` | `false` | Starts `ollama serve` automatically when the Ollama URL is local and not already reachable. |

Boolean variables accept only `true` or `false`.

### Debug Logs

By default, Klovis does not write traffic bodies to disk. To inspect the final
request body sent upstream after anonymization, enable debug logging:

```sh
KLOVIS_PROXY_DEBUG=true go run ./cmd/klovis
```

Debug mode writes logs to `proxy.log`. Use it only in local development, because
it records the anonymized upstream request body.

### Optional LLM Extraction

LLM extraction is disabled by default. Enable it with:

```sh
KLOVIS_LLM_ENABLED=true go run ./cmd/klovis
```

When enabled, Klovis checks the Ollama connection during startup and runs a small
extraction probe before accepting traffic. If startup verification fails, the
proxy exits.

By default, Klovis does not start Ollama for you. Start Ollama separately before
enabling LLM extraction, or opt in to local autostart:

```sh
KLOVIS_LLM_ENABLED=true KLOVIS_LLM_AUTOSTART=true go run ./cmd/klovis
```

Autostart only applies to local Ollama URLs such as `http://localhost:11434` or
loopback IP addresses. Remote Ollama URLs are never started by Klovis.

Deterministic detectors remain the baseline. LLM matches are added when
available and have lower priority than deterministic regex, Gitleaks, and
Presidio matches. If the LLM fails during a request, Klovis logs the technical
error and continues with deterministic anonymization.

## Detectors

Klovis combines built-in detectors with external rules loaded at startup.
External rule payloads are cached for 24 hours in the user cache directory under
`klovis/external-rules`.

| Category | Source | Priority | Description |
| --- | --- | ---: | --- |
| `EMAIL` | Built-in / Presidio | 1000 / 600 | Email addresses, normalized in lowercase for stable tokens. |
| `NIR` | Built-in | 1000 | French social security numbers, including spaced formats and Corsica departments `2A` and `2B`. |
| `IBAN` | Built-in / Presidio | 1000 / 600 | IBAN-like account identifiers, normalized by removing separators. |
| `IP` | Built-in / Presidio | 900 / 600 | IPv4 and IPv6 addresses. |
| `URL` | Built-in / Presidio | 900 / 600 | HTTP(S) and `www.` URLs. |
| `CREDIT_CARD` | Built-in / Presidio | 900 / 600 | Credit card-like digit sequences. |
| `MAC_ADDRESS` | Built-in / Presidio | 900 / 600 | MAC addresses with `:` or `-` separators. |
| `PHONE` | Built-in | 700 | French and common international phone numbers. |
| `BIRTH_DATE` | Built-in | 600 | Conservatively labelled birth dates. |
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

## Development

Clone the repository and install dependencies:

```sh
git clone https://github.com/Korbicorp/klovis.git
cd klovis
go mod download
```

Run the test suite:

```sh
go test ./...
```

Run the proxy locally:

```sh
go run ./cmd/klovis
```

Format Go code before submitting changes:

```sh
gofmt -w ./cmd ./internal
```

## Security Notes

Klovis reduces the amount of sensitive data sent upstream, but it is not a
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
gofmt -w ./cmd ./internal
```

## License

Klovis is released under the [MIT License](LICENSE).
