"""Export recovered E2 raw plus allowlisted AWS metadata after verified cleanup.

Never publish the Terraform state, account identifiers, launch requests, or task
attachments. Raw measurement bytes are copied unchanged, or export fails.
"""
import argparse
import hashlib
import json
import re
import shutil
from pathlib import Path


def read(path):
    return json.loads(path.read_text(encoding="utf-8-sig"))


def write(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True, allow_nan=False)+"\n", encoding="utf-8", newline="\n")


PRIVATE = re.compile(r"(?:AKIA|ASIA)[A-Z0-9]{16}|arn:aws[^:]*:[^:\s]*:[^:\s]*:\d{12}:|\b\d{12}\.dkr\.ecr|[A-Za-z]:[\\/](?:Users|Projects)[\\/]", re.I)


def export(local, deployment, runs, output):
    cleanup = read(local / "residual-check.json")
    if not cleanup.get("valid") or cleanup["deployment_id"] != deployment:
        raise ValueError("Verified cleanup for this deployment is required before publication")
    if output.exists():
        raise ValueError("Output must be fresh")
    image = read(local / "image.json")
    metadata = []
    sources = []
    for run_id in runs:
        if not re.fullmatch(r"[a-z0-9][a-z0-9-]+", run_id):
            raise ValueError("invalid run id")
        source = local / "recovered" / deployment / run_id
        manifest = read(source / "manifest.json")
        completion = read(source / "completion.json")
        task = read(local / (run_id+"-stop.json"))
        if manifest["cohort"] != "aws" or manifest["commit"] != image["commit"]:
            raise ValueError("Run/image provenance mismatch")
        if len(task["containers"]) != 1 or task["containers"][0]["imageDigest"] != image["digest"]:
            raise ValueError("Unexpected executed image")
        record = {"run_id":run_id, "mode":manifest["mode"], "commit":manifest["commit"],
                  "image_digest":image["digest"], "cpu_units":task["cpu"], "memory_mib":task["memory"],
                  "launch_type":task["launchType"], "platform_version":task["platformVersion"],
                  "stop_code":task.get("stopCode"), "exit_code":task["containers"][0].get("exitCode"),
                  "completion_valid":completion["valid"], "planned":manifest["planned"],
                  "warmup_planned":manifest["warmup_planned"],
                  "preflight_diagnostic_calls":manifest.get("preflight_diagnostic_calls",0)}
        for key in ("createdAt","pullStartedAt","pullStoppedAt","startedAt","executionStoppedAt","stoppedAt"):
            if key in task:
                record[key] = task[key]
        metadata.append(record)
        for path in source.rglob("*"):
            if path.is_file() and path.suffix in (".json", ".jsonl", ".log", ".txt"):
                if PRIVATE.search(path.read_text()):
                    raise ValueError(f"Private identifier in raw: {run_id}/{path.relative_to(source)}; review explicitly")
        sources.append((run_id, source))
    output.mkdir(parents=True)
    for run_id, source in sources:
        shutil.copytree(source, output / run_id)
    write(output / "execution.json", {"schema":"e2-aws-execution-v1", "deployment_id":deployment,
          "source_commit":image["commit"], "image_digest":image["digest"], "runs":metadata,
          "all_five_modes_present":{r["mode"] for r in metadata} == {"diagnose","validate","calibrate","measure","quality"},
          "notes":["Allowlisted ECS task metadata; account/resource identifiers omitted.",
                   "Container-visible CPU quota can be -1 on Fargate; CPU units are the ECS Task allocation.",
                   "Raw measurement files are copied byte-for-byte. Local and AWS cohorts are separate."]})
    cleanup_source = read(local / "cleanup.json")
    cleanup_record = {k:cleanup_source[k] for k in ("started","ended","stopped_tasks","recovered","verified") if k in cleanup_source}
    cleanup_record["recovery_error_present"] = "recovery_error" in cleanup_source
    write(output / "cleanup.json", cleanup_record)
    write(output / "residual-check.json", cleanup)
    write(output / "reviewed-plan.json", read(local / "reviewed-plan.json"))
    for name in ("live-verification.json", "calibration-gate.json"):
        if (local / name).exists():
            write(output / name, read(local / name))
    for path in output.rglob("*"):
        if path.is_file() and path.suffix in (".json", ".jsonl", ".log", ".txt") and PRIVATE.search(path.read_text()):
            raise ValueError("Private identifier in exported metadata; do not publish")
    hashes = {str(p.relative_to(output)).replace("\\", "/"):hashlib.sha256(p.read_bytes()).hexdigest()
              for p in sorted(output.rglob("*")) if p.is_file()}
    write(output / "sha256.json", hashes)
    print(json.dumps({"runs":len(metadata),"files_hashed":len(hashes),"cleanup_verified":True}))


if __name__ == "__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("local",type=Path);parser.add_argument("deployment");parser.add_argument("output",type=Path)
    parser.add_argument("runs",nargs="+")
    args=parser.parse_args();export(args.local,args.deployment,args.runs,args.output)
