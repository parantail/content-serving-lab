import json
import tempfile
import unittest
from pathlib import Path

from report import report


class ReportProvenance(unittest.TestCase):
    def test_identical_first_three_qualities_for_all_inputs_block_comparison(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            performance, quality = root / "performance", root / "quality"
            performance.mkdir(); quality.mkdir()
            (performance / "verification.json").write_text(json.dumps(dict(
                mode="measure", cohort="aws", transforms=5760, corpus_sha256="a"*64)))
            (quality / "verification.json").write_text(json.dumps(dict(
                mode="quality", cohort="aws", decoded_outputs=192, corpus_sha256="a"*64)))
            for name in ("batches.json", "transforms.json"):
                (performance / name).write_text("[]")
            rows = [dict(engine="magick", format="avif", id=str(i), quality=q, sha256=str(i)*64)
                    for i in range(8) for q in (50,65,80)]
            (quality / "quality.json").write_text(json.dumps(rows))
            with self.assertRaisesRegex(ValueError, "Unresponsive quality sweep: magick/avif"):
                report(performance, quality, root / "figures")
            self.assertFalse((root / "figures").exists())

    def test_same_cohort_with_different_corpora_is_rejected_before_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            performance, quality = root / "performance", root / "quality"
            performance.mkdir(); quality.mkdir()
            (performance / "verification.json").write_text(json.dumps(dict(
                mode="measure", cohort="aws", transforms=5760, corpus_sha256="a"*64)))
            (quality / "verification.json").write_text(json.dumps(dict(
                mode="quality", cohort="aws", decoded_outputs=192, corpus_sha256="b"*64)))
            with self.assertRaisesRegex(ValueError, "one cohort and corpus"):
                report(performance, quality, root / "figures")
            self.assertFalse((root / "figures").exists())


if __name__ == "__main__":
    unittest.main()
