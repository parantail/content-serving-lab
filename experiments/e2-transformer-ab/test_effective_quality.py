import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from analyze import verify_effective_quality


class EffectiveQualityTest(unittest.TestCase):
    def test_wrong_or_missing_actual_quality_rejected(self):
        line = "E2_CODEC encoder=AOMedia Project AV1 Encoder v3.12.1 threads=2 speed=5 quality=80 query_errors=0,0,0\n"
        manifest = {"codec_quality_contract": "effective-avif-q80-v1", "mode": "diagnose"}
        with TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in ("vips", "magick"):
                (root / f"{name}.stderr.log").write_text(line * 8)
            verify_effective_quality(root, manifest)
            for bad in (line.replace("quality=80", "quality=50"), line.replace("quality=80 ", ""), line.replace("0,0,0", "0,0,1")):
                (root / "magick.stderr.log").write_text(line * 7 + bad)
                with self.assertRaisesRegex(ValueError, "Actual encoder"):
                    verify_effective_quality(root, manifest)

    def test_new_contract_requires_settings(self):
        with TemporaryDirectory() as tmp:
            root = Path(tmp)
            path = root / "preflight"
            path.mkdir()
            line = "E2_CODEC encoder=AOMedia Project AV1 Encoder v3.12.1 threads=1 speed=5 quality=80 query_errors=0,0,0\n"
            for name in ("vips", "magick"):
                (path / f"{name}.stderr.log").write_text(line * 8)
            (path / "settings.json").write_text(json.dumps({"settings": [{"engine": "vips"}, {"engine": "magick"}]}))
            with self.assertRaisesRegex(ValueError, "Effective quality settings"):
                verify_effective_quality(root, {"codec_quality_contract": "effective-avif-q80-v1", "mode": "validate"})
            verify_effective_quality(root, {"mode": "validate"})


if __name__ == "__main__":
    unittest.main()
