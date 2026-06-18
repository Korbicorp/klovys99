# klovis

Klovis is a small Go CLI that anonymizes text from `stdin` and writes the
anonymized text to `stdout`.

By default, Klovis is regex-based and uses stable pseudonyms for a single
execution: the same detected value is replaced by the same token during the run.
An optional local LLM mode can be enabled to catch contextual PII that regexes
miss. At startup, Klovis also downloads the official Gitleaks rule set and the
default Microsoft Presidio recognizer config, then adds the supported external
regex detectors dynamically. These external payloads are cached on disk for
24 hours in the user cache directory, so they are not re-downloaded on every
execution.

Recent additions include:

- unlabelled French postal address detection, for example `14 Rue de la République, 69002 Lyon`
- contextual name detection such as `Je m'appelle Jean-Pierre`, `Hello this is Alice`, or `**Nom :** JULIEN MOREAU`
- contextual birth date detection
- contextual blood type detection such as `Groupe sanguin O+` or `Blood type AB negative`
- dynamic external regex loading from Gitleaks and Microsoft Presidio

## Requirements

Core regex mode only requires Go:

```sh
go version
```

LLM mode requires Ollama to be installed and available in `PATH`, because Klovis
starts `ollama serve` automatically when `--llm` is enabled:

```sh
which ollama
ollama --version
```

If Ollama is missing, install it first:

```sh
curl -fsSL https://ollama.com/install.sh | sh
```

The model must also be available locally before running Klovis with `--llm`:

```sh
ollama pull mistral
```

## Usage

```sh
go run ./cmd/klovis < input.txt > output.txt
```

Show anonymization statistics on `stderr`:

```sh
go run ./cmd/klovis --stats < input.txt > output.txt
```

`--stats` prints entity counts and timing metrics on `stderr`, including stdin
read time, external rule cache hits and misses, Gitleaks and Presidio
download/parse/compile timings, detector initialization, Ollama startup, LLM
extraction, anonymization, stdout write, Ollama shutdown, and total runtime.
Entity counts produced by the LLM are prefixed with `llm.`, for example
`llm.PERSON_NAME count=1`; regex counts keep their raw category name, for
example `EMAIL count=1`.

Disable extra detectors such as URLs, IBANs, credit card-like numbers and MAC
addresses:

```sh
go run ./cmd/klovis --no-extra < input.txt > output.txt
```

Disable downloading Gitleaks secret detectors:

```sh
go run ./cmd/klovis --no-gitleaks < input.txt > output.txt
```

Disable downloading Presidio regex detectors:

```sh
go run ./cmd/klovis --no-presidio < input.txt > output.txt
```

Enable local LLM extraction through Ollama:

```sh
go run ./cmd/klovis --llm --llm-model mistral < input.txt > output.txt
```

When `--llm` is enabled, Klovis checks the local Ollama API and automatically
starts `ollama serve` if it is not already running. This only applies to local
URLs such as `localhost` or `127.0.0.1`; remote URLs are treated as already
managed outside Klovis.

Useful LLM flags:

- `--llm`: enables local LLM extraction.
- `--llm-url`: Ollama base URL, default `http://localhost:11434`.
- `--llm-model`: Ollama model name, default `mistral`.
- `--llm-timeout`: request timeout, default `30s`.
- `--llm-max-chars`: maximum input bytes sent per chunk, default `3000`.

Useful Gitleaks flags:

- `--no-gitleaks`: disables downloading and loading external Gitleaks secret detectors.
- `--gitleaks-url`: Gitleaks TOML config URL, default `https://raw.githubusercontent.com/gitleaks/gitleaks/master/config/gitleaks.toml`.
- `--gitleaks-timeout`: timeout for downloading and parsing Gitleaks rules, default `10s`.

Useful Presidio flags:

- `--no-presidio`: disables downloading and loading external Presidio regex detectors.
- `--presidio-url`: Presidio YAML config URL, default `https://raw.githubusercontent.com/microsoft/presidio/main/presidio-analyzer/presidio_analyzer/conf/default_recognizers.yaml`.
- `--presidio-timeout`: timeout for downloading and parsing Presidio rules, default `10s`.

Microsoft Presidio integration:

- Klovis downloads the default Presidio recognizer orchestration file from the official `microsoft/presidio` repository.
- Klovis then loads the supported regex-based generic recognizers from Presidio source code.
- Currently mapped Presidio entities are `EMAIL`, `IP`, `URL`, `IBAN`, `CREDIT_CARD`, `DATE`, `MAC_ADDRESS`, and `CRYPTO`.
- Non-regex or validator-heavy recognizers which are not directly reusable in this Go pipeline are skipped.
- Gitleaks and Presidio payloads are cached locally for 24 hours to avoid repeated downloads on each execution.

Use a lower `--llm-max-chars` value to force smaller LLM chunks on dense texts:

```sh
go run ./cmd/klovis --llm --llm-max-chars 800 < input.txt > output.txt
```

The LLM never rewrites text directly. It returns JSON entities with a `type` and
the exact `text` to anonymize; Go then relocalizes those strings and applies the
same overlap resolution as regex detectors. If `--llm` is enabled and Ollama
fails or returns invalid JSON, the command exits with an error.

LLM extraction runs in a single pass. The prompt asks the model to return
contextual PII missed by regexes, including dates, document IDs, vehicle plates,
medical providers, schools, employers, and pet identifiers when they are tied to
the profile.

## Detectors

When matches overlap, the detector with the highest priority wins. If priorities
are equal, the longest match wins.

| Category | Scope | Priority | Description |
| --- | --- | ---: | --- |
| `EMAIL` | Core | 1000 | Email addresses, normalized in lowercase for stable tokens. |
| `NIR` | Core | 1000 | French social security numbers, including spaced formats and Corsica departments `2A`/`2B`. |
| `IBAN` | Extra | 1000 | IBAN-like account identifiers, normalized by removing separators. |
| `IP` | Core | 900 | IPv4 and IPv6 addresses. |
| `URL` | Extra | 900 | HTTP(S) and `www.` URLs. Lower priority than emails, so emails inside URLs can win. |
| `CREDIT_CARD` | Extra | 900 | Credit card-like digit sequences. Lower than NIR/IBAN to avoid stealing structured IDs. |
| `MAC_ADDRESS` | Extra | 900 | MAC addresses with `:` or `-` separators. |
| `PHONE` | Core | 700 | French and common international phone numbers. |
| `BIRTH_DATE` | Core | 600 | Conservatively labelled birth dates such as `Date de naissance: 14 mars 1988` or `Née le 12/01/1988`. |
| `BLOOD_TYPE` | Core | 600 | Contextual blood groups such as `Groupe sanguin O+` or `Blood type AB negative`. |
| `SECRET` | Gitleaks | 600 | Secrets loaded dynamically from the official Gitleaks config at startup. Path-scoped Gitleaks rules are skipped because Klovis only scans raw stdin text. |
| `CRYPTO` | Presidio | 600 | Cryptocurrency wallet identifiers loaded from supported Microsoft Presidio regex recognizers. |
| `ADDRESS` | Core / LLM | 900 / 700 / 50 | Regex catches both unlabelled French postal addresses such as `14 Rue de la République, 69002 Lyon` and conservatively labelled addresses, including common street types and optional residence/building complements; LLM can add complex contextual addresses with lower priority. |
| `NAME` | Core | 900 | Contextual names following strong FR/EN cues or form labels, including Markdown variants such as `Je m'appelle Jean-Pierre`, `Hello this is Alice`, or `**Nom :** JULIEN MOREAU`. |
| `FIRST_NAME` | Core | 500 | Conservatively labelled first names, for example `Prénom: Jean`. |
| `LAST_NAME` | Core | 500 | Conservatively labelled last names, for example `Nom: Dupont`. |
| `NUMERIC_ID` | Extra | 100 | Generic long numeric IDs of at least 7 digits. Low priority fallback. |
| `REFERENCE_ID` | Extra | 100 | Labelled alphanumeric IDs such as `Ref: ABC12345`, requiring letters and digits. |
| `PERSON_NAME` | LLM | 50 | Contextual full names found by the local model. |
| `DATE` | LLM | 50 | Dates tied to identity, documents, family, employment, education, health, or events. |

Extra detectors are enabled by default and can be disabled with `--no-extra`.
Gitleaks secret detectors are enabled by default and can be disabled with `--no-gitleaks`.
Supported Presidio regex detectors are enabled by default and can be disabled with `--no-presidio`.
LLM detectors are disabled by default and require `--llm`.
