import gzip
import json
import tempfile
import unittest
from pathlib import Path

from export_aws import export, sanitize_metadata


def metadata():
    image = "123456789012.dkr.ecr.ap-northeast-2.amazonaws.com/test@sha256:" + "a" * 64
    return {
        "storage": "s3", "container_image": image,
        "original_bucket": "test-bucket", "derivative_bucket": "test-bucket",
        "hostname": "ip-10-0-0-1.ap-northeast-2.compute.internal",
        "git_commit": "b" * 40, "repetitions": 2, "fixture_bytes": 4075024,
        "commands": ["run --container-image " + image + " --original-bucket test-bucket"],
    }


class PublicationBoundary(unittest.TestCase):
    def test_metadata_keeps_digest_measurements_and_shared_bucket_relationship(self):
        original = metadata()
        encoded, _ = sanitize_metadata(original)
        actual = json.loads(encoded)
        self.assertEqual(actual["container_image"], "content-serving-e3@sha256:" + "a" * 64)
        self.assertEqual(actual["original_bucket"], actual["derivative_bucket"])
        for key in ("git_commit", "repetitions", "fixture_bytes"):
            self.assertEqual(actual[key], original[key])
        self.assertEqual(original["original_bucket"], "test-bucket")

    def test_unhandled_identifier_in_new_field_is_rejected(self):
        value = metadata()
        value["debug"] = value["hostname"]
        with self.assertRaises(ValueError):
            sanitize_metadata(value)

    def test_compressed_credential_is_rejected_without_creating_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "input"
            source.mkdir()
            (source / "run.json").write_text(json.dumps(metadata()))
            (source / "requests.csv.gz").write_bytes(gzip.compress(("ASIA" + "A" * 16).encode()))
            with self.assertRaises(ValueError):
                export(source, root / "output")
            self.assertFalse((root / "output").exists())

    def test_export_preserves_compressed_raw_and_refuses_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "input"
            source.mkdir()
            (source / "run.json").write_text(json.dumps(metadata()))
            raw = gzip.compress(b"latency_ms\n2001.3\n")
            (source / "requests.csv.gz").write_bytes(raw)
            self.assertEqual(export(source, root / "output"), 2)
            self.assertEqual((root / "output/requests.csv.gz").read_bytes(), raw)
            with self.assertRaises(ValueError):
                export(source, root / "output")


if __name__ == "__main__":
    unittest.main()
