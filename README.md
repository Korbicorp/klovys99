# klovys99

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

klovys99 is a local reverse proxy that anonymizes sensitive prompt data before
forwarding requests to Anthropic or OpenAI APIs.

It is designed to sit between coding clients such as Claude Code or Codex and
their upstream API, replacing detected personal or sensitive values with stable
pseudonym tokens before the request leaves the machine.

## Architecture At A Glance

<table>
  <tr>
    <td align="center" width="22%">
      <img src="docs/readme-assets/logos/vscode.svg" alt="VS Code" width="58" /><br />
      <strong>VS Code clients</strong><br />
      Claude extension<br />
      Codex extension
    </td>
    <td align="center" width="10%">
      <strong>local HTTP / WS</strong><br />
      &#10132;
    </td>
    <td align="center" width="36%">
      <img src="klovys99.png" alt="klovys99" width="104" /><br />
      <strong>klovys99 local proxy</strong><br />
      Sits on the developer machine and anonymizes prompts before they leave the box
    </td>
    <td align="center" width="10%">
      <strong>anonymized traffic</strong><br />
      &#10132;
    </td>
    <td align="center" width="22%">
      <img src="docs/readme-assets/logos/anthropic.svg" alt="Anthropic / Claude" width="46" />
      <img src="docs/readme-assets/logos/openai.svg" alt="OpenAI / Codex" width="46" /><br />
      <strong>Remote APIs</strong><br />
      Claude / Anthropic<br />
      Codex / OpenAI-compatible
    </td>
  </tr>
</table>

Klovys99 sits locally between the editor client and the upstream API so only
anonymized prompt content is sent to the remote model.

<table>
  <tr>
    <td align="center" width="22%">
      <img src="klovys99.png" alt="klovys99" width="96" /><br />
      <strong>klovys99 runtime</strong><br />
      Starts local web surfaces alongside the proxy
    </td>
    <td align="center" width="8%">
      <strong>spawns</strong><br />
      &#10132;
    </td>
    <td align="center" width="28%">
      <strong>Admin UI</strong><br />
      Dashboard<br />
      Stats, controls, anonymization preview
    </td>
    <td align="center" width="8%">
      <strong>spawns</strong><br />
      &#10132;
    </td>
    <td align="center" width="34%">
      <strong>User chat UI</strong><br />
      <img src="docs/readme-assets/logos/gemini.svg" alt="Google Gemini" width="30" />
      <img src="docs/readme-assets/logos/anthropic.svg" alt="Claude" width="30" />
      <img src="docs/readme-assets/logos/openai.svg" alt="ChatGPT" width="30" />
      <img src="docs/readme-assets/logos/mistralai.svg" alt="Mistral AI" width="30" /><br />
      Gemini / Claude / ChatGPT / Mistral AI<br />
      API key based today, OAuth sign-in in progress
    </td>
  </tr>
</table>

<table>
  <tr>
    <td align="center" width="15%">
      <strong>Prompt in</strong><br />
      clear-text source
    </td>
    <td align="center" width="8%">
      &#10132;
    </td>
    <td align="center" width="39%">
      <strong>Parallel detection engine</strong><br />
      regex core<br />
      <img src="docs/readme-assets/logos/presidio.svg" alt="Presidio" width="88" />
      <img src="docs/readme-assets/logos/gitleaks.svg" alt="Gitleaks" width="36" /><br />
      Microsoft Presidio rules + Gitleaks rules + open-source NER
    </td>
    <td align="center" width="8%">
      &#8646;
    </td>
    <td align="center" width="30%">
      <img src="docs/readme-assets/logos/sqlite.svg" alt="SQLite" width="44" /><br />
      <strong>SQLite token store</strong><br />
      Stable anon/de-anon mapping for request and response round-trips
    </td>
  </tr>
  <tr>
    <td align="center" colspan="2">
      &nbsp;
    </td>
    <td align="center">
      <strong>Output</strong><br />
      anonymized request upstream
    </td>
    <td align="center">
      &#10132;
    </td>
    <td align="center">
      <strong>Return path</strong><br />
      restored response back to the local client
    </td>
  </tr>
</table>

<p align="center">
  <img src="docs/readme-assets/logos/gemini.svg" alt="Google Gemini" width="38" />
  <img src="docs/readme-assets/logos/anthropic.svg" alt="Claude" width="38" />
  <img src="docs/readme-assets/logos/openai.svg" alt="ChatGPT / Codex" width="38" />
  <img src="docs/readme-assets/logos/mistralai.svg" alt="Mistral AI" width="38" />
</p>

## Get Started

If you cloned this repository, start with these commands from the repository
root:

1. Install the package and the local CLI.

```sh
npm install
```

2. Configure the client you want to route through Klovys99. Run one of:

```sh
npm run cli -- configure codex
npm run cli -- configure claude
npm run cli -- configure both
```

3. Start the local proxy.

```sh
npm run cli -- start
```

Klovys99 listens on `http://127.0.0.1:8080` by default and exposes:

- `http://127.0.0.1:8080/anthropic` for Claude Code and other Anthropic clients
- `http://127.0.0.1:8080/openai/v1` for Codex and other OpenAI-compatible
  clients

If you installed the published npm package instead of cloning this repository,
use the same flow with `npx klovys99`:

```sh
npx klovys99 configure both
npx klovys99 start
```

The historical command name `npx klovis` still works. The historical unprefixed
route also still exists and forwards to `KLOVIS_TARGET_URL`, which defaults to
`https://api.anthropic.com`.

If you want the install step to also update your client configuration
immediately:

```sh
KLOVIS_CLIENT=claude npm install
```

Supported values are `codex`, `claude`, and `both`.

## Features

- Local reverse proxy for Anthropic and OpenAI-compatible JSON requests.
- `npm install` workflow that downloads a prebuilt binary for the current OS
  and architecture and exposes a `klovys99` command.
- Client configuration helpers for Codex and Claude Code.
- Built-in deterministic detectors for common PII and sensitive identifiers.
- Local GLiNER sidecar enabled by default through the standard `klovys99 start`
  flow, with explicit `full` and `off` modes.
- Dynamic detector loading from the official Gitleaks and Microsoft Presidio
  rule sources.
- Stable pseudonym tokens for the lifetime of the proxy process.
- Structured logs with anonymization counters instead of raw prompt values.
- Disk cache for downloaded external rules to avoid repeated network fetches on
  every startup.

## Requirements

- Node.js 18 or newer.
- Network access on first startup to download the default Gitleaks and Presidio
  rule sources.
- An Anthropic API key, Claude subscription, or OpenAI API key depending on the
  client you route through Klovys99.
- Docker Desktop or Docker Engine when you use the standard GLiNER-backed
  startup flow.

Go 1.25 or newer is only required if you work from a source checkout or build
release binaries yourself.

Check your local tooling:

```sh
node -v
npm -v
go version
```

## Installation

From the repository root:

```sh
npm install
```

If you are installing the published package instead of working from a checkout:

```sh
npm install klovys99
```

The install step downloads the matching binary from the GitHub release for the
package version into `dist/` and exposes the CLI entrypoints `klovys99` and
`klovis`. `klovys99` is the preferred name and `klovis` remains available for
compatibility.

Supported prebuilt targets:

- macOS `arm64`
- macOS `x64`
- Linux `arm64`
- Linux `x64`
- Windows `arm64`
- Windows `x64`

## Client Configuration

Examples below use the published CLI form. From a local repository checkout,
replace `npx klovys99` with `npm run cli --`.

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
| `KLOVIS_PROXY_DEBUG` | `false` | Enables additional sanitized diagnostic logging. Raw traffic is never logged. |
| `KLOVIS_LOG_PII_FINDINGS` | `false` | Deprecated and ignored for privacy; raw findings are never logged. |
| `KLOVIS_LOG_TO_FILE` | `false` | Writes logs to `proxy.log` instead of stdout when set to `true`. |

### Contextual GLiNER protection modes

The standard `klovys99 start` flow now starts with GLiNER in `full` mode by
default. The raw Go binary keeps `off` unless you set `KLOVIS_GLINER_MODE`
yourself. Two modes are available and explicit:

- `full`: all configured contextual labels.
- `off`: regex-only mode without contextual GLiNER analysis.

The default pinned model identity is:

- model: `urchade/gliner_multi_pii-v1`
- revision: `1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d`

You can pre-install or refresh the pinned model explicitly:

```sh
npx klovys99 gliner install \
  --model urchade/gliner_multi_pii-v1 \
  --revision 1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d
```

The standard startup commands are:

```sh
npx klovys99 start
npx klovys99 start --gliner-mode off
```

When `full` is selected, the npm wrapper ensures that the pinned model is
installed locally under `~/.klovys99/gliner`, starts the local sidecar on
`127.0.0.1:8091`, and then launches the Go proxy. `off` skips the sidecar
entirely and runs the regex-only proxy.

The `full` mode requests:

- `person name`
- `organization`
- `location`
- `employer`
- `school or educational institution`
- `medical provider or healthcare institution`
- `street address`

A sample direct sidecar latency benchmark is available in [docs/benchmarks/gliner-benchmark.md](docs/benchmarks/gliner-benchmark.md).

| Variable | Default | Description |
| --- | --- | --- |
| `KLOVIS_GLINER_MODE` | `off` | Explicit contextual mode for the raw Go binary: `full` or `off`. The npm `klovys99 start` wrapper injects `full` by default. |
| `KLOVIS_GLINER_ENABLED` | deprecated | Legacy bool compatibility shim. Prefer `KLOVIS_GLINER_MODE`. |
| `KLOVIS_GLINER_URL` | `http://127.0.0.1:8091` | Loopback sidecar URL; non-loopback URLs are rejected. |
| `KLOVIS_GLINER_MODEL` | `urchade/gliner_multi_pii-v1` | Exact model identifier used by the npm wrapper or direct env config. |
| `KLOVIS_GLINER_MODEL_REVISION` | `1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d` | Immutable revision/digest used by the npm wrapper or direct env config. |
| `KLOVIS_GLINER_TIMEOUT` | `5s` | Per-batch deadline. |
| `KLOVIS_GLINER_THRESHOLD` | `0.50` | Global confidence threshold. |
| `KLOVIS_GLINER_LABEL_THRESHOLDS` | `{}` | JSON object overriding thresholds for fixed labels. |
| `KLOVIS_GLINER_MAX_CONCURRENCY` | `2` | Maximum concurrent inference calls. |
| `KLOVIS_GLINER_MAX_BATCH_CHARS` | `32768` | Maximum Unicode characters per request batch. |
| `KLOVIS_GLINER_FAILURE_POLICY` | `fail-closed` | Only supported policy in V1. |
| `KLOVIS_GLINER_DATA_DIR` | `~/.klovys99/gliner` | npm lifecycle model directory. |

When `full` is active, a timeout, unavailable sidecar, saturated queue,
malformed span, or model identity mismatch returns `503` and makes zero
upstream calls. If the npm wrapper cannot build, install, or start GLiNER, the
startup command exits with an explicit error instead of silently falling back.
`/healthz` reports Go liveness, `/readyz` includes contextual readiness, and
`/api/status` exposes sanitized metadata including the active GLiNER mode.

The npm wrapper also honors:

| Variable | Description |
| --- | --- |
| `KLOVIS_CLIENT` | Client to configure during `npm install`: `codex`, `claude`, or `both`. |
| `KLOVIS_BASE_URL` | Base URL written by `klovys99 configure` or `npm install` auto-configuration. |
| `KLOVIS_SKIP_DOWNLOAD` | Skips the prebuilt binary download during `postinstall` when set to `true`. |
| `KLOVIS_SKIP_BUILD` | Skips the local Go build fallback during `postinstall` when set to `true`. |
| `KLOVIS_SKIP_CONFIGURE` | Skips client configuration during `postinstall` when set to `true`. |

Boolean variables accept only `true` or `false`.

## Logs

Klovys99 writes structured application logs to stdout by default. To write logs to
`proxy.log` instead, enable file logging:

```sh
KLOVIS_LOG_TO_FILE=true npx klovys99 start
```

Debug and file logging remain available, but request bodies, detected values,
token mappings, credentials, and model inputs are never logged. The historical
`KLOVIS_LOG_PII_FINDINGS` setting is ignored.

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
| `DATE` | Built-in / Presidio | 600 / external | Conservatively labelled birth dates and supported contextual dates. |
| `BLOOD_TYPE` | Built-in | 600 | Contextual blood groups such as `Groupe sanguin O+`. |
| `SECRET` | Gitleaks | 600 | Secrets loaded dynamically from the official Gitleaks config. |
| `CRYPTO` | Presidio | 600 | Cryptocurrency wallet identifiers loaded from supported Presidio recognizers. |
| `ADDRESS` | Built-in | 900 / 700 | French postal addresses and labelled addresses. |
| `NAME` | Built-in | 900 | Contextual names following strong French or English cues and form labels. |
| `NUMERIC_ID` | Built-in | 100 | Generic long numeric IDs. |
| `REFERENCE_ID` | Built-in | 100 | Labelled alphanumeric references requiring letters and digits. |

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

Tagged releases build one binary per supported OS and architecture in GitHub
Actions. If `NPM_TOKEN_KLOVYS` is configured in repository secrets, the same tag
workflow also publishes the npm package after uploading the release assets.

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
