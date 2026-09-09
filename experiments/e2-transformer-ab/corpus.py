"""Generate or verify the fixed E2 corpus. Run in Dockerfile.tools from repo root."""
import argparse
import hashlib
import io
import json
import struct
import subprocess
import tempfile
import zlib
from pathlib import Path

import numpy as np
from PIL import Image, ImageCms, ImageDraw, ImageOps, __version__ as pillow_version

ROOT = Path(__file__).resolve().parent
SOURCES = [
    ("landscape", ROOT.parent / "e1-cache-stampede/fixtures/landscape-4928x3264.jpg",
     "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"),
    ("fruit", ROOT / "fixtures/sources/fruit-5040x3234.jpg",
     "8f0186504ef5a225272692c7e3bfaf725a33f8bf7e4813eab37ce8d606bb1000"),
]
QUALITY_IDS = ["landscape-small-jpeg", "landscape-large-webp", "fruit-small-png",
               "fruit-large-avif", "alpha-small-png", "alpha-large-webp",
               "alpha-small-avif", "orientation-6-jpeg"]


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def call(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def decode(path):
    if path.suffix == ".avif":
        with tempfile.TemporaryDirectory() as tmp:
            png = Path(tmp) / "decoded.png"
            call("avifdec", "--jobs", "1", str(path), str(png))
            return Image.open(png).convert("RGBA")
    image = Image.open(path)
    image = ImageOps.exif_transpose(image)
    if image.info.get("icc_profile"):
        image = ImageCms.profileToProfile(image, ImageCms.ImageCmsProfile(io.BytesIO(image.info["icc_profile"])),
                                          ImageCms.createProfile("sRGB"), outputMode="RGB")
    image = image.convert("RGBA")
    # LCMS-generated sRGB profiles contain their creation time. Keep normalized
    # pixels, but do not propagate the transient profile into generated files.
    return Image.frombytes("RGBA", image.size, image.tobytes())


def resized(image, longest):
    w, h = image.size
    factor = min(1, longest / max(w, h))
    return image.resize((int(w * factor + 0.5), int(h * factor + 0.5)), Image.Resampling.LANCZOS)


def geometry(image, operation):
    w, h = image.size
    if operation == "resize":
        return resized(image, 640)
    if operation == "contain":
        factor = min(1, 640 / w, 480 / h)
        return image.resize((int(w * factor + .5), int(h * factor + .5)), Image.Resampling.LANCZOS)
    # Premultiplied RGBA resampling in Pillow, with a centered crop of the resized raster.
    factor = min(1, max(640 / w, 640 / h))
    image = image.resize((max(640, int(w * factor + .5)), max(640, int(h * factor + .5))), Image.Resampling.LANCZOS)
    x, y = (image.width - 640) // 2, (image.height - 640) // 2
    return image.crop((x, y, x + 640, y + 640))


def encode(image, path, fmt):
    if fmt == "jpeg":
        image.convert("RGB").save(path, quality=95, subsampling=0, optimize=False)
    elif fmt == "png":
        image.save(path, compress_level=6)
    elif fmt == "webp":
        image.save(path, quality=90, method=4, lossless=False, exact=True)
    else:
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "input.png"
            image.save(source)
            call("avifenc", "--jobs", "1", "--codec", "aom", "--speed", "8", "--qcolor", "90",
                 "--qalpha", "100", "--yuv", "444", "--depth", "8", str(source), str(path))


def generate(out):
    if out.exists():
        raise ValueError("Output exists; use a fresh directory or --verify")
    for _, source, expected in SOURCES:
        if sha(source) != expected:
            raise ValueError(f"Source hash mismatch: {source.name}")
    out.mkdir(parents=True)
    (out / "references").mkdir()
    records = []

    def add(name, image, fmt, group, size, orientation=1):
        path = out / (name + (".jpg" if fmt == "jpeg" else "." + fmt))
        if orientation != 1:
            # Rotate stored pixels in the inverse direction so tag 6/8 gives the same upright raster.
            stored = image.transpose(Image.Transpose.ROTATE_90 if orientation == 6 else Image.Transpose.ROTATE_270)
            exif = Image.Exif()
            exif[274] = orientation
            stored.convert("RGB").save(path, quality=95, subsampling=0, exif=exif)
        else:
            encode(image, path, fmt)
        decoded = decode(path)
        references = {}
        for operation in ("resize", "contain", "cover"):
            reference = geometry(decoded, operation)
            ref_path = out / "references" / f"{name}-{operation}.png"
            # Remove inherited metadata; reference pixels only.
            Image.frombytes("RGBA", reference.size, reference.tobytes()).save(ref_path, compress_level=6)
            references[operation] = {"path": str(ref_path.relative_to(out)), "sha256": sha(ref_path),
                                     "width": reference.width, "height": reference.height}
        records.append({"id": name, "path": path.name, "group": group, "size": size, "format": fmt,
                        "width": decoded.width, "height": decoded.height, "orientation": orientation,
                        "has_alpha": decoded.getextrema()[3][0] < 255,
                        "bytes": path.stat().st_size, "sha256": sha(path),
                        "quality_sample": name in QUALITY_IDS, "references": references})
        print(f"generated {name}", flush=True)

    for group, source, _ in SOURCES:
        original = decode(source).convert("RGB")
        for label, longest in (("small", 1024), ("large", 4096)):
            image = resized(original, longest)
            for fmt in ("jpeg", "png", "webp", "avif"):
                add(f"{group}-{label}-{fmt}", image, fmt, group, label)
    for label, w in (("small", 1024), ("large", 4096)):
        h = w * 3 // 4
        pixels = np.zeros((h, w, 4), dtype=np.uint8)
        pixels[:, :, 0] = 220
        pixels[:, :, 1] = np.linspace(20, 230, w, dtype=np.uint8)[None, :]
        pixels[:, :, 2] = 90
        pixels[:, :, 3] = np.linspace(0, 255, w, dtype=np.uint8)[None, :]
        image = Image.fromarray(pixels)
        draw = ImageDraw.Draw(image)
        draw.rectangle((w//12, h//12, w//3, h//2), fill=(20, 80, 240, 255))
        draw.ellipse((w//3, h//5, 4*w//5, 4*h//5), fill=(245, 190, 15, 120))
        draw.polygon([(w//6, 3*h//4), (w//2, h//2), (4*w//5, 7*h//8)], fill=(10, 160, 100, 220))
        for i in range(16):
            x = w//2 + i * w//64
            draw.line((x, h//10, x, h//4), fill=(0, 0, 0, 255), width=max(1, w//1024))
        for fmt in ("png", "webp", "avif"):
            add(f"alpha-{label}-{fmt}", image, fmt, "alpha", label)
    image = Image.new("RGB", (1024, 768), "white")
    draw = ImageDraw.Draw(image)
    for box, color in [((0, 0, 511, 383), "red"), ((512, 0, 1023, 383), "green"),
                       ((0, 384, 511, 767), "blue"), ((512, 384, 1023, 767), "yellow")]:
        draw.rectangle(box, fill=color)
    draw.polygon([(512, 60), (390, 240), (460, 240), (460, 650), (564, 650), (564, 240), (634, 240)], fill="black")
    for orientation in (6, 8):
        add(f"orientation-{orientation}-jpeg", image, "jpeg", "orientation", "small", orientation)
    invalid = out / "invalid"
    invalid.mkdir()
    (invalid / "truncated.jpg").write_bytes((out / "landscape-small-jpeg.jpg").read_bytes()[:100])
    (invalid / "unsupported.txt").write_text("not an image\n")
    # Valid PNG chunk CRCs, but dimensions exceed the policy before decoding IDAT.
    raw = (out / "alpha-small-png.png").read_bytes()
    ihdr = struct.pack(">II", 100000, 100000) + raw[24:29]
    huge = raw[:16] + ihdr + struct.pack(">I", zlib.crc32(b"IHDR" + ihdr)) + raw[33:]
    (invalid / "oversize.png").write_bytes(huge)
    manifest = {"schema": "e2-corpus-v1", "pillow": pillow_version,
                "avifenc": call("avifenc", "--version").strip(),
                "packages": call("dpkg-query", "-W").splitlines(),
                "sources": [{"id": name, "sha256": expected} for name, _, expected in SOURCES],
                "fixtures": records,
                "invalid": [{"path": str(p.relative_to(out)), "sha256": sha(p)} for p in sorted(invalid.iterdir())]}
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    verify(out)


def verify(out):
    manifest = json.loads((out / "manifest.json").read_text())
    fixtures = manifest["fixtures"]
    assert len(fixtures) == 24 and len({x["id"] for x in fixtures}) == 24
    assert {x["id"] for x in fixtures if x["quality_sample"]} == set(QUALITY_IDS)
    for fixture in fixtures:
        path = out / fixture["path"]
        assert sha(path) == fixture["sha256"] and path.stat().st_size == fixture["bytes"]
        decoded = decode(path)
        assert decoded.size == (fixture["width"], fixture["height"])
        assert (decoded.getextrema()[3][0] < 255) == fixture["has_alpha"]
        for operation, reference in fixture["references"].items():
            assert sha(out / reference["path"]) == reference["sha256"]
            expected = geometry(decoded, operation)
            actual = Image.open(out / reference["path"])
            assert actual.size == (reference["width"], reference["height"])
            assert actual.tobytes() == expected.tobytes()
    for invalid in manifest["invalid"]:
        assert sha(out / invalid["path"]) == invalid["sha256"]
    print("verified 24 inputs, 8 quality samples, 72 independent references, 3 invalid fixtures", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=ROOT / "fixtures/generated")
    parser.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    (verify if args.verify else generate)(args.output)
