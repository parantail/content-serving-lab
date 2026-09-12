"""Export an E3 run with deployment identifiers removed from run metadata.

Request, resource and analysis files retain their original bytes. This exports
one recovered run directory, never a Terraform directory or launch request.
"""
import argparse
import copy
import gzip
import json
import re
import shutil
from pathlib import Path


RAW_FILES = {
    "run.json", "trials.csv", "requests.csv.gz", "resources.csv.gz",
    "requests.csv", "resources.csv", "state.csv", "metrics.prom", "logs.jsonl",
}
PRIVATE = re.compile(
    rb"(?:AKIA|ASIA)[A-Z0-9]{16}|\b\d{12}\.dkr\.ecr\."
    rb"|arn:aws[^:\s]*:[^:\s]*:[^:\s]*:\d{12}:"
    rb"|-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"
    rb"|\b(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{20,}"
    rb"|(?i:aws_secret_access_key|aws_session_token|x-amz-signature)"
    rb"|[A-Za-z]:[\\/](?:Users|Projects)[\\/]"
    rb"|ip-\d+-\d+-\d+-\d+\.[a-z0-9.-]+\.internal"
)


def sanitize_metadata(metadata):
    if metadata.get("storage") != "s3":
        raise ValueError("Expected an E3 S3 run")
    result = copy.deepcopy(metadata)
    original_image = result["container_image"]
    digest = re.search(r"sha256:[0-9a-f]{64}$", original_image)
    if not digest:
        raise ValueError("An exact image digest is required")
    replacements = {original_image: "content-serving-e3@" + digest.group()}
    buckets = [result["original_bucket"], result["derivative_bucket"]]
    if not all(isinstance(value, str) and value for value in buckets):
        raise ValueError("Missing bucket metadata")
    if buckets[0] == buckets[1]:
        replacements[buckets[0]] = "e3-experiment-bucket"
    else:
        replacements.update(zip(buckets, ("e3-original-bucket", "e3-derivative-bucket")))
    result["container_image"] = replacements[original_image]
    result["original_bucket"] = replacements[buckets[0]]
    result["derivative_bucket"] = replacements[buckets[1]]
    if result.get("hostname"):
        replacements[result["hostname"]] = "fargate-task"
        result["hostname"] = "fargate-task"
    commands = []
    for command in result.get("commands", []):
        for old, new in sorted(replacements.items(), key=lambda item: -len(item[0])):
            command = command.replace(old, new)
        commands.append(command)
    result["commands"] = commands
    encoded = (json.dumps(result, ensure_ascii=False, indent=2) + "\n").encode()
    if PRIVATE.search(encoded):
        raise ValueError("Credential or private identifier remains in metadata")
    # Check removed values in every field, including future metadata additions.
    removed = [value.encode() for value, replacement in replacements.items() if value != replacement]
    if any(value in encoded for value in removed):
        raise ValueError("Deployment identifier remains outside the sanitized fields")
    return encoded, removed


def export(source, output):
    if output.exists():
        raise ValueError("Output must be fresh")
    metadata = json.loads((source / "run.json").read_text(encoding="utf-8-sig"))
    sanitized, removed = sanitize_metadata(metadata)
    files = []
    for path in source.rglob("*"):
        if path.is_symlink():
            raise ValueError("Symlinks are not allowed in a run export")
        if not path.is_file():
            continue
        relative = path.relative_to(source)
        if not (len(relative.parts) == 1 and path.name in RAW_FILES) and not (
            relative.parts[0] == "analysis" and path.suffix in (".json", ".csv", ".svg")
        ):
            raise ValueError("Unexpected file in run directory")
        data = sanitized if relative.as_posix() == "run.json" else path.read_bytes()
        text = gzip.decompress(data) if path.suffix == ".gz" else data
        if PRIVATE.search(text) or any(value in text for value in removed):
            raise ValueError("Credential or private identifier in run data; export stopped")
        files.append((path, relative))
    # Validate the whole input before creating any public output.
    output.mkdir(parents=True)
    for path, relative in files:
        destination = output / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        if relative.as_posix() == "run.json":
            destination.write_bytes(sanitized)
        else:
            shutil.copyfile(path, destination)
    return len(files)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    print(json.dumps({"files": export(args.source, args.output), "metadata_sanitized": True}))
