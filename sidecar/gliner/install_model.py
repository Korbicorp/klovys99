"""Explicit, atomic model installer with a reusable SHA-256 manifest."""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import tempfile
from pathlib import Path

from huggingface_hub import snapshot_download


def main() -> None:
    model = required("GLINER_MODEL")
    revision = required("GLINER_MODEL_REVISION")
    destination = Path(required("GLINER_MODEL_DIR"))
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=destination.parent) as temporary:
        staging = Path(temporary) / "model"
        snapshot_download(
            repo_id=model,
            revision=revision,
            local_dir=staging,
            local_dir_use_symlinks=False,
        )
        files = {}
        for path in sorted(staging.rglob("*")):
            if path.is_file():
                files[str(path.relative_to(staging))] = sha256(path)
        (staging / "klovis-model-manifest.json").write_text(
            json.dumps(
                {"model": model, "revision": revision, "files": files},
                indent=2,
                sort_keys=True,
            )
            + "\n",
            encoding="utf-8",
        )
        previous = destination.with_suffix(".previous")
        if previous.exists():
            shutil.rmtree(previous)
        if destination.exists():
            destination.rename(previous)
        staging.rename(destination)
        if previous.exists():
            shutil.rmtree(previous)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def required(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


if __name__ == "__main__":
    main()

