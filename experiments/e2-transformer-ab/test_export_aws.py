import json
import tempfile
import unittest
from pathlib import Path

from export_aws import PRIVATE, export


class PublicationBoundary(unittest.TestCase):
    def test_identifiers_not_cpu_counters(self):
        self.assertIsNone(PRIVATE.search('{"cpu_ns":123456789012}'))
        for value in ("arn:aws:iam::123456789012:role/test",
                      "123456789012.dkr.ecr.ap-northeast-2.amazonaws.com/test",
                      "ASIA"+"A"*16, "D:/Projects/private/data"):
            self.assertIsNotNone(PRIVATE.search(value))

    def test_unverified_cleanup_stops_export(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "residual-check.json").write_text(json.dumps({"valid":False,"deployment_id":"test"}))
            with self.assertRaisesRegex(ValueError,"Verified cleanup"):
                export(root,"test",[],root/"public")
            self.assertFalse((root/"public").exists())


if __name__ == "__main__":
    unittest.main()
