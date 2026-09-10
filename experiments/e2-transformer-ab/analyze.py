"""Independent E2 output verification and deterministic report data.

Run in Dockerfile.tools. Timing is read from worker events, never from this process.
SSIM: RGB in encoded sRGB, data_range=255, window=7, uniform weights,
sample covariance, mean across channels; alpha composited in encoded sRGB on
black and white. PSNR uses the same RGB samples (infinity represented by null).
"""
import argparse
import json
import math
import re
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw
from skimage.metrics import structural_similarity

from corpus import ROOT, decode, sha


def read(path):
    return json.loads(path.read_text())


def write(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True, allow_nan=False) + "\n")


def composite(rgba, background):
    alpha = rgba[:, :, 3:4] / 255.0
    return rgba[:, :, :3] * alpha + background * (1 - alpha)


def metrics(reference, output, jpeg):
    a, b = np.asarray(reference, dtype=np.float32), np.asarray(output, dtype=np.float32)
    result = {}
    for background, label in ((0, "black"), (255, "white")):
        x = composite(a, 255 if jpeg else background)
        y = composite(b, background)
        mse = float(np.mean((x.astype(np.float64) - y) ** 2))
        result[f"ssim_{label}"] = float(structural_similarity(
            x, y, data_range=255, channel_axis=2, win_size=7,
            gaussian_weights=False, use_sample_covariance=True))
        result[f"psnr_{label}"] = None if mse == 0 else 10 * math.log10(255**2 / mse)
        result[f"mse_{label}"] = mse
    alpha_error = np.abs((np.full_like(a[:, :, 3], 255) if jpeg else a[:, :, 3]) - b[:, :, 3])
    result.update(alpha_mae=float(alpha_error.mean()), alpha_max=float(alpha_error.max()),
                  alpha_min=int(b[:, :, 3].min()), alpha_max_value=int(b[:, :, 3].max()))
    return result


def verify_effective_quality(run, manifest):
    contract = manifest.get("codec_quality_contract")
    if contract is None:
        return  # Historical raw predates actual quality queries; preserve reanalysis.
    if contract != "effective-avif-q80-v1":
        raise ValueError("Unknown codec quality contract")
    diagnostic = manifest["mode"] == "diagnose"
    path = run if diagnostic else run / "preflight"
    logs = list(path.glob("*.stderr.log"))
    if len(logs) != 2:
        raise ValueError("Missing effective quality probe logs")
    pattern = re.compile(r"^E2_CODEC encoder=AOMedia Project AV1 Encoder v3\.12\.1 threads=(\d+) speed=5 quality=80 query_errors=0,0,0$", re.M)
    for log in logs:
        matches = pattern.findall(log.read_text())
        if len(matches) != 8 or len(set(matches)) != 1 or not 1 <= int(matches[0]) <= 64:
            raise ValueError("Actual encoder quality/configuration mismatch")
    if not diagnostic:
        settings = read(path / "settings.json")
        if any(s.get("effective_quality") != 80 or s.get("quality_query_error") != 0 for s in settings["settings"]):
            raise ValueError("Effective quality settings missing or invalid")


def analyze(run, output, corpus):
    manifest, completion = read(run / "manifest.json"), read(run / "completion.json")
    fixtures = {f["id"]: f for f in read(corpus / "manifest.json")["fixtures"]}
    if sha(corpus / "manifest.json") != manifest["corpus_sha256"]:
        raise ValueError("corpus hash mismatch")
    if not completion["valid"] or completion["completed_batches"] != len(manifest["jobs"]):
        raise ValueError("Incomplete/failed run: preserve raw; cannot report as valid")
    verify_effective_quality(run, manifest)
    if manifest.get("preflight_diagnostic_calls"):
        settings = read(run / "preflight/settings.json")
        if settings["probe_enabled_in_performance"] or {s["engine"] for s in settings["settings"]} != {"vips", "magick"}:
            raise ValueError("same-task preflight invalid")
        if any(s["samples"] != 8 or s["speed"] != 5 or not 1 <= s["encoder_threads"] <= 64 for s in settings["settings"]):
            raise ValueError("codec parameters invalid")
    output.mkdir(parents=True, exist_ok=False)
    rows, batches, quality = [], [], []
    montage = []
    for job in manifest["jobs"]:
        batch, engine = job["batch"], job["engine"]
        batch_id, spec = batch["id"], batch["spec"]
        events = [json.loads(line) for line in (run / (batch_id + ".jsonl")).read_text().splitlines()]
        result = read(run / (batch_id + ".json"))
        if result["exit_code"] != 0 or result.get("error") or result["wait_rss_bytes"] < result["self_rss_bytes"]:
            raise ValueError(f"invalid batch {batch_id}")
        if not result["scope"]["membership_equal"] or any(not c["parent_member"] or not c["child_member"] for c in result["scope"]["controllers"]):
            raise ValueError("cgroup scope invalid")
        finished = [e for e in events if e["type"] == "finish" and not e.get("warmup")]
        expected = {f["id"] for f in fixtures.values() if not batch["quality"] or f["quality_sample"]}
        if len(finished) != len(expected) or {e["id"] for e in finished} != expected or any(e.get("error") for e in finished):
            raise ValueError("invalid completion coverage")
        ends = [e for e in events if e["type"] == "pass_end" and not e.get("warmup")]
        if len(ends) != 1 or ends[0]["success"] != len(expected):
            raise ValueError("invalid pass summary")
        wall = ends[0]["duration_ns"] / 1e9
        durations = [e["duration_ns"] / 1e6 for e in finished]
        common = dict(engine=engine, operation=spec["operation"], format=spec["format"],
                      quality=spec["quality"], concurrency=batch["concurrency"], repetition=batch["repetition"], batch=batch_id)
        batches.append(dict(common, success=len(finished), wall_seconds=wall,
                            throughput=len(finished) / wall, p50_ms=float(np.percentile(durations, 50)),
                            p95_ms=float(np.percentile(durations, 95)), self_rss_bytes=result["self_rss_bytes"],
                            wait_rss_bytes=result["wait_rss_bytes"], cgroup_peak_bytes=result["cgroup_sampled_peak_bytes"],
                            worker_cpu_ns=result["wait_cpu_user_ns"] + result["wait_cpu_system_ns"],
                            cgroup_cpu_ns=result["cgroup_cpu_ns"]))
        for e in finished:
            f = fixtures[e["id"]]
            record = dict(common, id=f["id"], group=f["group"], size=f["size"], input_format=f["format"],
                          duration_ms=e["duration_ns"] / 1e6, queue_ms=e.get("queue_ns", 0) / 1e6,
                          bytes=e["bytes"], sha256=e["sha256"])
            rows.append(record)
            if not batch["save"]:
                continue
            encoded = run / "outputs" / batch_id / (f["id"] + "." + spec["format"])
            if sha(encoded) != e["sha256"] or encoded.stat().st_size != e["bytes"]:
                raise ValueError(f"output hash/bytes mismatch {encoded}")
            if spec["format"] == "jpeg":
                with Image.open(encoded) as header:
                    if any(layer[1:3] != (1, 1) for layer in header.layer):
                        raise ValueError("JPEG 4:4:4 contract violated")
            if spec["format"] == "png":
                with Image.open(encoded) as header:
                    if header.mode not in ("RGB", "RGBA"):
                        raise ValueError("PNG must be 8-bit RGB/RGBA without palette")
            ref = f["references"][spec["operation"]]
            if sha(corpus / ref["path"]) != ref["sha256"]:
                raise ValueError("reference hash mismatch")
            reference, decoded = Image.open(corpus / ref["path"]).convert("RGBA"), decode(encoded)
            if decoded.size != reference.size:
                raise ValueError(f"geometry mismatch {encoded}: {decoded.size} != {reference.size}")
            m = metrics(reference, decoded, spec["format"] == "jpeg")
            if spec["format"] == "jpeg" and m["alpha_min"] != 255:
                raise ValueError("JPEG not opaque")
            if f["has_alpha"] and spec["format"] != "jpeg" and m["alpha_min"] == 255:
                raise ValueError(f"alpha lost {encoded}")
            quality.append(dict(record, **m))
            if spec["operation"] == "cover" and spec["quality"] == 80 and f["id"] in (
                    "orientation-6-jpeg", "orientation-8-jpeg", "alpha-small-png", "fruit-small-png"):
                # Enlarged central 160px crop, composited on gray for transparency inspection.
                x, y = (decoded.width - 160) // 2, (decoded.height - 160) // 2
                crop = decoded.crop((x, y, x+160, y+160))
                bg = Image.new("RGBA", crop.size, (128, 128, 128, 255))
                bg.alpha_composite(crop)
                montage.append((f'{engine} {spec["format"]} {f["id"]}', bg.convert("RGB").resize((320, 320))))
    if len(rows) != manifest["planned"]:
        raise ValueError("planned row count mismatch")
    write(output / "batches.json", batches)
    write(output / "transforms.json", rows)
    write(output / "quality.json", quality)
    write(output / "verification.json", {"schema": "e2-analysis-v1", "run_manifest_sha256": sha(run / "manifest.json"),
          "corpus_sha256": manifest["corpus_sha256"], "mode": manifest["mode"], "cohort": manifest["cohort"],
          "transforms": len(rows), "decoded_outputs": len(quality), "batches": len(batches),
          "metrics": "encoded-sRGB RGB SSIM window7 uniform sample covariance; float alpha composites black/white; PSNR null when MSE0",
          "valid": True})
    if montage:
        sheet = Image.new("RGB", (1280, math.ceil(len(montage)/4)*350), "white")
        draw = ImageDraw.Draw(sheet)
        for i, (label, tile) in enumerate(montage):
            x, y = (i%4)*320, (i//4)*350
            sheet.paste(tile, (x, y+30)); draw.text((x+2, y+5), label, fill="black")
        sheet.save(output / "crops.png")
    print(json.dumps({"batches":len(batches),"transforms":len(rows),"decoded_outputs":len(quality),"valid":True}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--corpus", type=Path, default=ROOT / "fixtures/generated")
    args = parser.parse_args()
    analyze(args.run, args.output, args.corpus)
