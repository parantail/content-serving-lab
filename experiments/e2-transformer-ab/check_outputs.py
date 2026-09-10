"""Check that Q80 measured outputs match independently decoded validation bytes."""
import argparse
import hashlib
import json
from pathlib import Path


def read(path):
    return json.loads(path.read_text())


def check(validation, measured, output):
    baseline, target = read(validation / "verification.json"), read(measured / "verification.json")
    if baseline["mode"] != "validate" or baseline["decoded_outputs"] != 576:
        raise ValueError("A complete independently decoded validation baseline is required")
    if baseline["cohort"] != target["cohort"] or baseline["corpus_sha256"] != target["corpus_sha256"]:
        raise ValueError("Do not compare hashes across cohorts/corpora")
    key = lambda r: (r["engine"], r["operation"], r["format"], r["quality"], r["id"])
    reference = {key(r): (r["sha256"], r["bytes"]) for r in read(validation / "transforms.json")}
    rows = [r for r in read(measured / "transforms.json") if r["quality"] == 80]
    failures = [dict(batch=r["batch"], id=r["id"]) for r in rows if reference.get(key(r)) != (r["sha256"], r["bytes"])]
    result = {"schema":"e2-output-hash-check-v1", "compared":len(rows), "mismatches":failures,
              "validation_transforms_sha256":hashlib.sha256((validation / "transforms.json").read_bytes()).hexdigest(),
              "measured_transforms_sha256":hashlib.sha256((measured / "transforms.json").read_bytes()).hexdigest(),
              "valid":bool(rows) and not failures}
    if output.exists():
        raise ValueError("Output already exists")
    output.write_text(json.dumps(result, indent=2, sort_keys=True)+"\n")
    if not result["valid"]:
        raise ValueError("Output differs from validation: inspect saved raw and configuration before interpreting quality")
    print(json.dumps(result))


if __name__=="__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("validation",type=Path);parser.add_argument("measured",type=Path);parser.add_argument("output",type=Path)
    args=parser.parse_args();check(args.validation,args.measured,args.output)
