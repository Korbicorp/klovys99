# Anonymization Corpus Tests

This directory contains a manual runner for testing anonymization on realistic
prompts.

The runner loads the same detectors as the proxy: built-in detectors, Gitleaks,
and Presidio.

## Run The Tests

Strict mode, useful for CI or blocking verification:

```sh
go run ./test/anonymization
```

Exploratory mode, useful for reading stats without failing the command:

```sh
go run ./test/anonymization -strict=false
```

Run the runner's Go tests:

```sh
go test ./test/anonymization
```

## Read The Output

The output prints:

- `expected`: number of entities expected by the JSON files.
- `found`: number of entities found by the anonymizer.
- `matched`: entities found with the same `type` and `value`.
- `missing`: expected entities that were not matched.
- `unexpected`: found entities that were not expected.
- `precision`: share of detections that were expected.
- `recall`: share of expected entities that were found.

Formulas:

```text
precision = matched / found
recall = matched / expected
```

A `missing` can be a real miss, a wrong category, or a too-wide match. A common
Gitleaks example: the corpus expects only the secret value, but the detector
finds the whole `.env` line.

## Add A Corpus Case

Add a prompt:

```text
test/anonymization/corpus/prompts/my_case.txt
```

Add the expected file with the same basename:

```text
test/anonymization/corpus/expected/my_case.json
```

Expected file format:

```json
{
  "entities": [
    { "type": "EMAIL", "value": "alice@example.com" },
    { "type": "SECRET", "value": "sk_test_example" }
  ]
}
```

Matching is exact on:

```text
type + value
```

So `SECRET=value` and `value` do not match, even if the PII is covered.

## Tips

- Put only one prompt in each file.
- Use fictional data only.
- Prefer explicit filenames: `fr_dev_env_leak`,
  `en_support_ticket`, `fr_medical_summary`.
- For exploratory corpus cases, run with `-strict=false`.
- For a stable corpus case, adjust `expected` to reflect the real spans returned
  by the runner.
