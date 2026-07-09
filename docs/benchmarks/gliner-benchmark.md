# GLiNER Sidecar Benchmark

## Scope

This document records a first latency benchmark for the local GLiNER sidecar API exposed by `klovys99`.

It covers direct calls to `POST /v1/analyze` on the sidecar bound to `127.0.0.1:8091`.

It does not yet cover end-to-end proxy latency through `POST /api/anonymization/test` or upstream provider calls.

## Environment

- Date: 2026-07-09
- Repository commit: `5a68bf7459b601b5ee024329327e02c5f7ad2ed1`
- Host OS: `Darwin 25.4.0`
- Machine: `MacBook Air`
- CPU: `Apple M4`
- CPU cores: `10`
- Memory: `17179869184` bytes (16 GB)
- Architecture: `arm64`
- Docker: `29.3.1`
- Model: `urchade/gliner_multi_pii-v1`
- Model revision: `1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d`

## Readiness

The sidecar was ready before running the measurements:

```json
{"status":"ready","model":"urchade/gliner_multi_pii-v1","model_revision":"1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d"}
```

## Test Scenarios

### Scenario 1: Single short request

- Endpoint: `POST http://127.0.0.1:8091/v1/analyze`
- Text count: `1`
- Labels: `person name`, `organization`, `location`
- Threshold: `0.5`
- Input text: `John Smith works at Sanofi in Lyon.`

Result:

- HTTP status: `200`
- Client total time: `208.370 ms`
- Sidecar reported latency: `204 ms`

Returned entities:

```json
{"model":"urchade/gliner_multi_pii-v1","model_revision":"1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d","results":[[{"start":0,"end":10,"label":"person name","score":0.7433786988258362},{"start":20,"end":26,"label":"organization","score":0.9989569187164307},{"start":30,"end":34,"label":"location","score":0.9871487021446228}]],"latency_ms":204}
```

### Scenario 2: Warm short request, 10 runs

Same payload as Scenario 1, repeated 10 times sequentially.

Raw client timings:

```text
run=1  134.554 ms
run=2  105.256 ms
run=3  112.396 ms
run=4  106.610 ms
run=5  102.195 ms
run=6  100.065 ms
run=7  100.651 ms
run=8  101.553 ms
run=9  100.328 ms
run=10 101.206 ms
```

Summary:

| Metric | Value |
| --- | --- |
| Mean | `106.481 ms` |
| Median (p50) | `101.874 ms` |
| Approx. p95 | `134.554 ms` |
| Min | `100.065 ms` |
| Max | `134.554 ms` |
| Final sidecar reported latency | `100 ms` |

Final returned payload:

```json
{"model":"urchade/gliner_multi_pii-v1","model_revision":"1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d","results":[[{"start":0,"end":10,"label":"person name","score":0.7433786988258362},{"start":20,"end":26,"label":"organization","score":0.9989569187164307},{"start":30,"end":34,"label":"location","score":0.9871487021446228}]],"latency_ms":100}
```

### Scenario 3: Single longer request

- Endpoint: `POST http://127.0.0.1:8091/v1/analyze`
- Text count: `1`
- Labels: `person name`, `organization`, `location`, `medical provider or healthcare institution`
- Threshold: `0.5`

Result:

- HTTP status: `200`
- Client total time: `223.237 ms`
- Sidecar reported latency: `219 ms`

Returned payload:

```json
{"model":"urchade/gliner_multi_pii-v1","model_revision":"1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d","results":[[{"start":0,"end":10,"label":"person name","score":0.6005325317382812},{"start":20,"end":26,"label":"organization","score":0.9966626763343811},{"start":30,"end":34,"label":"location","score":0.973319411277771},{"start":57,"end":70,"label":"person name","score":0.5708106160163879},{"start":134,"end":152,"label":"medical provider or healthcare institution","score":0.9352200031280518},{"start":210,"end":222,"label":"person name","score":0.5189929008483887}]],"latency_ms":219}
```

### Scenario 4: Batched request, 10 texts

- Endpoint: `POST http://127.0.0.1:8091/v1/analyze`
- Text count: `10`
- Labels: `person name`, `organization`, `location`
- Threshold: `0.5`

Result:

- HTTP status: `200`
- Client total time: `385.111 ms`
- Sidecar reported latency: `379 ms`

Returned payload:

```json
{"model":"urchade/gliner_multi_pii-v1","model_revision":"1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d","results":[[{"start":0,"end":10,"label":"person name","score":0.7433853149414062},{"start":20,"end":26,"label":"organization","score":0.9989570379257202},{"start":30,"end":34,"label":"location","score":0.9871490597724915}],[{"start":23,"end":31,"label":"organization","score":0.996212363243103},{"start":35,"end":40,"label":"location","score":0.9593015909194946}],[{"start":36,"end":45,"label":"location","score":0.9899742007255554}],[{"start":0,"end":12,"label":"person name","score":0.7522104978561401},{"start":31,"end":37,"label":"organization","score":0.9990460276603699},{"start":41,"end":49,"label":"location","score":0.9871469736099243}],[{"start":0,"end":13,"label":"person name","score":0.6311084628105164},{"start":29,"end":35,"label":"organization","score":0.997626006603241},{"start":39,"end":44,"label":"location","score":0.9924978613853455}],[{"start":0,"end":11,"label":"person name","score":0.9227795004844666},{"start":23,"end":30,"label":"organization","score":0.9983580708503723},{"start":34,"end":42,"label":"location","score":0.9847760796546936}],[{"start":34,"end":40,"label":"location","score":0.9838250875473022}],[{"start":0,"end":12,"label":"person name","score":0.8198691010475159},{"start":31,"end":38,"label":"organization","score":0.9989538192749023},{"start":42,"end":51,"label":"location","score":0.979888379573822}],[{"start":0,"end":11,"label":"person name","score":0.8149309754371643},{"start":23,"end":26,"label":"organization","score":0.9989000558853149},{"start":30,"end":37,"label":"location","score":0.9820656180381775}],[{"start":33,"end":41,"label":"location","score":0.990500807762146}]],"latency_ms":379}
```

## Hyperfine Measurement

`hyperfine` was available at `/opt/homebrew/bin/hyperfine`.

Command benchmarked:

```text
curl -s -o /dev/null -X POST http://127.0.0.1:8091/v1/analyze -H 'Content-Type: application/json' -d '{"texts":["John Smith works at Sanofi in Lyon."],"labels":["person name","organization","location"],"model":"urchade/gliner_multi_pii-v1","model_revision":"1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d","threshold":0.5}'
```

Result:

- Mean: `123.6 ms`
- Standard deviation: `3.2 ms`
- Range: `119.7 ms` to `132.3 ms`
- Sample size: `23 runs`