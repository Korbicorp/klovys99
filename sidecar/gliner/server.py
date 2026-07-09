"""Loopback-only GLiNER HTTP sidecar with a deterministic fake backend for tests."""

from __future__ import annotations

import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

LABELS = {
    "person name",
    "organization",
    "location",
    "employer",
    "school or educational institution",
    "medical provider or healthcare institution",
    "street address",
}
MAX_BODY_BYTES = int(os.environ.get("GLINER_MAX_BODY_BYTES", "4194304"))
MAX_BATCH_CHARS = int(os.environ.get("GLINER_MAX_BATCH_CHARS", "32768"))


class Backend:
    def __init__(self) -> None:
        self.model_name = required_env("GLINER_MODEL")
        self.revision = required_env("GLINER_MODEL_REVISION")
        self.fake = os.environ.get("GLINER_FAKE_BACKEND", "") == "1"
        if self.fake:
            self.model = None
            return
        model_dir = Path(required_env("GLINER_MODEL_DIR"))
        verify_manifest(model_dir, self.model_name, self.revision)
        from gliner import GLiNER  # Imported only after the explicit install.

        self.model = GLiNER.from_pretrained(str(model_dir), local_files_only=True)

    def predict(
        self, texts: list[str], labels: list[str], threshold: float
    ) -> list[list[dict[str, Any]]]:
        if self.fake:
            return [[] for _ in texts]
        assert self.model is not None
        raw = self.model.batch_predict_entities(texts, labels, threshold=threshold)
        results: list[list[dict[str, Any]]] = []
        for entities in raw:
            converted = []
            for entity in entities:
                start_byte = int(entity["start"])
                end_byte = int(entity["end"])
                text = str(entity["text"])
                # GLiNER releases expose Python-string or tokenizer offsets depending
                # on backend. Locate the returned value defensively, then publish
                # Unicode code-point offsets in our stable HTTP schema.
                converted.append(
                    {
                        "start": byte_or_char_to_char_offset(text, start_byte, entity),
                        "end": byte_or_char_to_char_offset(text, end_byte, entity, end=True),
                        "label": str(entity["label"]),
                        "score": float(entity["score"]),
                    }
                )
            results.append(converted)
        return results


def byte_or_char_to_char_offset(
    entity_text: str, offset: int, entity: dict[str, Any], end: bool = False
) -> int:
    source = entity.get("_source_text")
    if isinstance(source, str):
        try:
            decoded = source.encode("utf-8")[:offset].decode("utf-8")
            return len(decoded)
        except UnicodeDecodeError:
            pass
    # Current GLiNER APIs use character offsets; this remains the normal path.
    _ = entity_text
    _ = end
    return offset


class Handler(BaseHTTPRequestHandler):
    backend: Backend

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            self.respond(200, {"status": "ok"})
            return
        if self.path == "/readyz":
            self.respond(
                200,
                {
                    "status": "ready",
                    "model": self.backend.model_name,
                    "model_revision": self.backend.revision,
                },
            )
            return
        self.respond(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/v1/analyze":
            self.respond(404, {"error": "not found"})
            return
        try:
            payload = self.read_payload()
            texts = require_string_list(payload, "texts")
            labels = require_string_list(payload, "labels")
            if any(label not in LABELS for label in labels):
                raise ValueError("unsupported label")
            if payload.get("model") != self.backend.model_name:
                raise ValueError("model mismatch")
            if payload.get("model_revision") != self.backend.revision:
                raise ValueError("model revision mismatch")
            if sum(len(text) for text in texts) > MAX_BATCH_CHARS:
                raise ValueError("batch too large")
            threshold = float(payload.get("threshold", 0.5))
            if not 0 < threshold <= 1:
                raise ValueError("invalid threshold")
            started = time.monotonic()
            results = self.backend.predict(texts, labels, threshold)
            self.respond(
                200,
                {
                    "model": self.backend.model_name,
                    "model_revision": self.backend.revision,
                    "results": results,
                    "latency_ms": int((time.monotonic() - started) * 1000),
                },
            )
        except (ValueError, TypeError, json.JSONDecodeError):
            self.respond(400, {"error": "invalid request"})
        except Exception:
            # Never serialize exception details: model errors can include input text.
            self.respond(500, {"error": "inference failed"})

    def read_payload(self) -> dict[str, Any]:
        length = int(self.headers.get("content-length", "0"))
        if length <= 0 or length > MAX_BODY_BYTES:
            raise ValueError("invalid body size")
        value = json.loads(self.rfile.read(length))
        if not isinstance(value, dict):
            raise ValueError("body must be an object")
        return value

    def respond(self, status: int, payload: dict[str, Any]) -> None:
        encoded = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, format_string: str, *args: object) -> None:
        # Access logs contain no request bodies, but suppressing them also prevents
        # accidental future leakage through URL/query additions.
        _ = format_string
        _ = args


def require_string_list(payload: dict[str, Any], key: str) -> list[str]:
    value = payload.get(key)
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        raise ValueError(f"{key} must be a string list")
    return value


def required_env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def verify_manifest(model_dir: Path, model: str, revision: str) -> None:
    manifest_path = model_dir / "klovis-model-manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("model") != model or manifest.get("revision") != revision:
        raise RuntimeError("installed model identity mismatch")
    import hashlib

    for relative, expected in manifest.get("files", {}).items():
        path = model_dir / relative
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        if digest != expected:
            raise RuntimeError("installed model integrity check failed")


def main() -> None:
    backend = Backend()
    Handler.backend = backend
    host = os.environ.get("GLINER_HOST", "127.0.0.1")
    if host not in {"127.0.0.1", "::1", "0.0.0.0"}:
        raise RuntimeError("GLINER_HOST is invalid")
    port = int(os.environ.get("GLINER_PORT", "8091"))
    ThreadingHTTPServer((host, port), Handler).serve_forever()


if __name__ == "__main__":
    main()
