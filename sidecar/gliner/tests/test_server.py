import importlib.util
import json
import os
import sys
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).parents[1] / "server.py"


class ServerSchemaTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        os.environ["GLINER_MODEL"] = "test/model"
        os.environ["GLINER_MODEL_REVISION"] = "immutable-test-revision"
        os.environ["GLINER_FAKE_BACKEND"] = "1"
        spec = importlib.util.spec_from_file_location("gliner_server", MODULE_PATH)
        cls.module = importlib.util.module_from_spec(spec)
        assert spec.loader
        sys.modules["gliner_server"] = cls.module
        spec.loader.exec_module(cls.module)

    def test_fake_backend_preserves_batch_shape(self):
        backend = self.module.Backend()
        self.assertEqual(
            backend.predict(["Élodie 😀", "Paris"], ["person name"], 0.5),
            [[], []],
        )

    def test_allowed_labels_are_fixed(self):
        self.assertIn("person name", self.module.LABELS)
        self.assertNotIn("email", self.module.LABELS)

    def test_manifest_rejects_wrong_identity(self):
        from tempfile import TemporaryDirectory

        with TemporaryDirectory() as directory:
            path = Path(directory)
            (path / "klovis-model-manifest.json").write_text(
                json.dumps({"model": "wrong", "revision": "wrong", "files": {}})
            )
            with self.assertRaises(RuntimeError):
                self.module.verify_manifest(path, "expected", "revision")


if __name__ == "__main__":
    unittest.main()

