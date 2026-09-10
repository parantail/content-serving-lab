"""Summarize complete AWS preflight runs without promoting them to measurements.

Input is the directory produced by export_aws.py after cleanup. Re-run analyze.py
and check_outputs.py separately for independent image and output-hash validation.
"""
import argparse
import hashlib
import json
from pathlib import Path


def read(path):
    return json.loads(path.read_text(encoding="utf-8-sig"))


def summarize(raw):
    execution = read(raw / "execution.json")
    hashes = read(raw / "sha256.json")
    for name, expected in hashes.items():
        if hashlib.sha256((raw / name).read_bytes()).hexdigest() != expected:
            raise ValueError(f"Exported artifact hash mismatch: {name}")
    summaries = []
    for run in execution["runs"]:
        root = raw / run["run_id"]
        manifest, completion = read(root / "manifest.json"), read(root / "completion.json")
        if not completion["valid"] or completion["completed_batches"] != len(manifest["jobs"]):
            raise ValueError("Only complete valid runs can enter this checkpoint")
        results, events = [], []
        for job in manifest["jobs"]:
            batch = job["batch"]
            result = read(root / (batch["id"] + ".json"))
            if result["exit_code"] != 0 or result.get("error"):
                raise ValueError("Failed worker")
            results.append(result)
            events.extend(json.loads(line) for line in
                          (root / (batch["id"] + ".jsonl")).read_text().splitlines())
        finishes = [e for e in events if e["type"] == "finish"]
        measured = sum(not e.get("warmup", False) for e in finishes)
        warm = sum(bool(e.get("warmup", False)) for e in finishes)
        if any(e.get("error") for e in finishes) or measured != manifest["planned"] or warm != manifest["warmup_planned"]:
            raise ValueError("Unexpected native completion counts")
        wall = completion["wall_ns"] / 1e9
        worker_wall = sum(r["wall_ns"] for r in results) / 1e9
        summary = dict(mode=manifest["mode"], run_id=run["run_id"], batches=len(results),
                       completed=measured, warm_completed=warm,
                       preflight_diagnostic_calls=manifest.get("preflight_diagnostic_calls", 0),
                       loop_seconds=wall, worker_lifetime_seconds_sum=worker_wall,
                       between_worker_seconds=wall-worker_wall,
                       max_native_seconds=max(e["duration_ns"] for e in finishes)/1e9,
                       self_wait_rss_equal=all(r["self_rss_bytes"] == r["wait_rss_bytes"] for r in results),
                       cgroup_sources=sorted({r["cgroup_source"] for r in results}), engines={})
        for engine in ("vips", "magick"):
            group = [r for r in results if r["job"]["engine"] == engine]
            summary["engines"][engine] = dict(
                peak_worker_rss_mib=max(r["wait_rss_bytes"] for r in group)/2**20,
                peak_cgroup_mib=max(r["cgroup_sampled_peak_bytes"] for r in group)/2**20,
                sampled_worker_threads_max=max(r["worker_sampled_peak_threads"] for r in group))
        if manifest.get("preflight_diagnostic_calls"):
            summary["same_task_avif_settings"] = read(root / "preflight/settings.json")
        if manifest["mode"] == "calibrate":
            summary["measurement_time_gate"] = dict(
                repetitions=5, margin_multiplier=1.25, limit_seconds=3600,
                estimated_seconds=wall*5, with_margin_seconds=wall*5*1.25,
                passed=wall*5*1.25 <= 3600)
        summaries.append(summary)
    return dict(schema="e2-aws-checkpoint-v1", source_commit=execution["source_commit"],
                image_digest=execution["image_digest"],
                all_five_modes_present=execution["all_five_modes_present"],
                native_calls_including_warmup_and_preflight=sum(
                    s["completed"]+s["warm_completed"]+s["preflight_diagnostic_calls"] for s in summaries),
                runs=summaries)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("raw", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    result = summarize(args.raw)
    with args.output.open("x", encoding="utf-8", newline="\n") as stream:
        stream.write(json.dumps(result, indent=2, sort_keys=True, allow_nan=False)+"\n")
