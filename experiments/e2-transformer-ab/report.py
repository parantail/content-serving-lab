"""Regenerate E2 figures from independently analyzed, single-cohort run data."""
import argparse
import json
from collections import defaultdict
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.transforms import Bbox
import numpy as np


def read(path):
    return json.loads(path.read_text())


def report(performance, quality, output, diagnostic_note=None):
    pv, qv = read(performance / "verification.json"), read(quality / "verification.json")
    if (pv["mode"] != "measure" or qv["mode"] != "quality" or pv["cohort"] != qv["cohort"]
            or pv["corpus_sha256"] != qv["corpus_sha256"]):
        raise ValueError("Require performance/quality runs from one cohort and corpus")
    if pv["transforms"] != 5760 or qv["decoded_outputs"] != 192:
        raise ValueError("full matrix required")
    batches, transforms, qualities = read(performance / "batches.json"), read(performance / "transforms.json"), read(quality / "quality.json")
    for engine in ("vips", "magick"):
        for fmt in ("jpeg", "webp", "avif"):
            first_three = defaultdict(list)
            for q in qualities:
                if q["engine"] == engine and q["format"] == fmt and q["quality"] in (50,65,80):
                    first_three[q["id"]].append(q)
            if (len(first_three) == 8 and all(len(rows) == 3 and
                    len({q["sha256"] for q in rows}) == 1 for rows in first_three.values())
                    and not diagnostic_note):
                raise ValueError(f"Unresponsive quality sweep: {engine}/{fmt}; investigate before comparison. "
                                 "Use --diagnostic-note only to render explicitly labeled investigation results.")
    output.mkdir(parents=True, exist_ok=False)
    notice = diagnostic_note+"\n" if diagnostic_note else ""
    colors = {"vips":"#1565c0", "magick":"#d45d00"}
    formats = ["jpeg", "png", "webp", "avif"]
    ops = ["resize", "contain", "cover"]
    groups = defaultdict(list)
    for b in batches:
        groups[(b["engine"], b["operation"], b["format"], b["concurrency"])].append(b)
    summary = []
    fig, axes = plt.subplots(2, 2, figsize=(12, 9))
    labels = defaultdict(list)
    for ax, fmt in zip(axes.flat, formats):
        for (engine, op, format_, c), values in groups.items():
            if format_ != fmt:
                continue
            if len(values) != 5:
                raise ValueError("five repetitions required")
            x = np.array([b["self_rss_bytes"] / 2**20 for b in values])
            y = np.array([b["throughput"] for b in values])
            xm, ym = np.median(x), np.median(y)
            ax.errorbar(xm, ym, xerr=[[xm-x.min()], [x.max()-xm]], yerr=[[ym-y.min()], [y.max()-ym]],
                        color=colors[engine], marker={"resize":"o","contain":"s","cover":"^"}[op],
                        fillstyle="full" if c == 4 else "none", capsize=2, linestyle="none")
            label = {"resize":"r", "contain":"f", "cover":"c"}[op]
            engine_label = "V" if engine == "vips" else "M"
            labels[ax].append((xm, ym, f"{engine_label}-{label}{c}", colors[engine]))
            summary.append(dict(engine=engine, operation=op, format=fmt, concurrency=c,
                                throughput_median=float(ym), throughput_min=float(y.min()), throughput_max=float(y.max()),
                                rss_mib_median=float(xm), rss_mib_min=float(x.min()), rss_mib_max=float(x.max()),
                                p95_ms_median=float(np.median([b["p95_ms"] for b in values]))))
        ax.set(title=fmt.upper(), xlabel="Worker lifetime peak RSS (MiB)", ylabel="Successful transforms / second")
        ax.grid(alpha=.2)
    fig.suptitle(notice+f"{pv['cohort']}: 1 vCPU / 2 GiB, requested Q80, 24 inputs, median and min/max of 5 repeats\nV: libvips; M: ImageMagick. Labels: resize/fit/cover (r/f/c) + concurrency; filled: c4", fontsize=11)
    fig.tight_layout(rect=(0,0,1,.94))
    # Place labels after layout, keeping nearby geometry results readable.
    fig.canvas.draw()
    renderer = fig.canvas.get_renderer()
    for ax, points in labels.items():
        occupied = []
        markers = [Bbox.from_bounds(x-4, y-4, 8, 8) for x,y in
                   (ax.transData.transform((x,y)) for x,y,_,_ in points)]
        for x, y, label, color in points:
            for dx,dy in [(6,7),(6,-12),(-6,7),(-6,-12),(6,22),(-6,22),
                          (6,-27),(-6,-27),(22,7),(22,-12),(-22,7),(-22,-12)]:
                artist = ax.annotate(label, (x,y), xytext=(dx,dy), textcoords="offset points",
                    ha="left" if dx>0 else "right", fontsize=7, color=color,
                    arrowprops=dict(arrowstyle="-",color=color,lw=.5))
                artist.get_window_extent(renderer)  # Resolve the annotation's offset transform.
                # Annotation extent includes its leader line; test text alone.
                text_box = matplotlib.text.Text.get_window_extent(artist, renderer).expanded(1.08,1.15)
                if (ax.bbox.contains(text_box.x0,text_box.y0) and ax.bbox.contains(text_box.x1,text_box.y1)
                        and not any(text_box.overlaps(other) for other in occupied+markers)):
                    occupied.append(text_box)
                    break
                artist.remove()
            else:
                raise ValueError(f"Could not place scatter label {label}")
    fig.savefig(output / "throughput-rss.png", dpi=160); plt.close(fig)
    ids = sorted({q["id"] for q in qualities})
    fig, axes = plt.subplots(3, 8, figsize=(25, 10))
    for row, fmt in enumerate(["jpeg","webp","avif"]):
        for column, fixture in enumerate(ids):
            ax = axes[row,column]
            for engine in colors:
                points = sorted([q for q in qualities if q["format"]==fmt and q["id"]==fixture and q["engine"]==engine], key=lambda q:q["quality"])
                if len(points)!=4:
                    raise ValueError("four quality points required")
                x=[q["bytes"]/1024 for q in points]; y=[min(q["ssim_black"],q["ssim_white"]) for q in points]
                style = "o-" if engine == "vips" else "s--"
                ax.plot(x,y,style,color=colors[engine],markersize=3)
                for px,py,q in zip(x,y,points):
                    ax.annotate(str(q["quality"]),(px,py),fontsize=6,color=colors[engine],
                                xytext=(2,3 if engine=="vips" else -8),textcoords="offset points")
            ax.set_title(f"{fmt.upper()} / {fixture}",fontsize=7)
            ax.set_xlabel("KiB",fontsize=8);ax.set_ylabel("min SSIM black/white",fontsize=8);ax.grid(alpha=.2);ax.tick_params(labelsize=7)
            ax.margins(x=.12,y=.15)
    fig.suptitle(notice+f"{pv['cohort']}: cover 640, independent reference SSIM vs encoded bytes; labels requested Q50/65/80/90\nSolid circles: libvips; dashed squares: ImageMagick. Same Q does not mean equal quality.",fontsize=12)
    fig.tight_layout(rect=(0,0,1,.94));fig.savefig(output / "quality-bytes.png",dpi=140);plt.close(fig)
    # Keep concurrency separate. Ratios use matched per-fixture medians across repeats.
    fixtures=sorted({r["id"] for r in transforms})
    columns=[(op,fmt) for op in ops for fmt in formats]
    fig,axes=plt.subplots(1,4,figsize=(24,13))
    for ax,(c,metric) in zip(axes,[(1,"duration_ms"),(4,"duration_ms"),(1,"bytes"),(4,"bytes")]):
        values=np.zeros((len(fixtures),len(columns)))
        for i,fixture in enumerate(fixtures):
            for j,(op,fmt) in enumerate(columns):
                med={engine:np.median([r[metric] for r in transforms if r["id"]==fixture and r["engine"]==engine and r["operation"]==op and r["format"]==fmt and r["concurrency"]==c]) for engine in colors}
                values[i,j]=np.log2(med["magick"]/med["vips"])
        plot=ax.imshow(values,aspect="auto",cmap="RdBu",vmin=-2,vmax=2)
        for i in range(len(fixtures)):
            for j in range(len(columns)):
                ax.text(j,i,f"{values[i,j]:+.1f}",ha="center",va="center",fontsize=5,
                        color="white" if abs(values[i,j])>1.2 else "black")
        ax.set_yticks(range(len(fixtures)),fixtures,fontsize=7)
        ax.set_xticks(range(len(columns)),[f"{op}/{fmt}" for op,fmt in columns],rotation=90,fontsize=7)
        ax.set_title(f"c{c}: log2(ImageMagick/libvips {metric})",fontsize=10)
        fig.colorbar(plot,ax=ax,shrink=.7,label="0=equal, +1=2x, -1=0.5x")
    fig.suptitle(notice+f"{pv['cohort']}: 1 vCPU / 2 GiB, input/geometry/output matrix, requested Q80, 5 repeats; execution errors: 0\nPositive duration ratio means libvips faster; positive byte ratio means libvips smaller. Quality is not held equal.\nCell labels show log2 ratios; colors saturate at -2 and +2.",fontsize=13)
    fig.tight_layout(rect=(0,0,1,.95));fig.savefig(output / "input-matrix.png",dpi=130);plt.close(fig)
    (output / "summary.json").write_text(json.dumps(sorted(summary,key=lambda x:(x['format'],x['operation'],x['concurrency'],x['engine'])),indent=2,allow_nan=False)+"\n")
    matched = []
    for fmt in formats:
        for op in ops:
            for c in (1,4):
                pair = {r["engine"]:r for r in summary if r["format"]==fmt and r["operation"]==op and r["concurrency"]==c}
                v,m = pair["vips"],pair["magick"]
                matched.append(dict(format=fmt,operation=op,concurrency=c,
                    vips_throughput=v["throughput_median"],magick_throughput=m["throughput_median"],
                    throughput_ratio_vips_over_magick=v["throughput_median"]/m["throughput_median"],
                    vips_rss_mib=v["rss_mib_median"],magick_rss_mib=m["rss_mib_median"],
                    rss_ratio_vips_over_magick=v["rss_mib_median"]/m["rss_mib_median"]))
    (output / "conditions.json").write_text(json.dumps(matched,indent=2,allow_nan=False)+"\n")
    q80 = []
    for fmt in ("jpeg","webp","avif"):
        for fixture in ids:
            pair={q["engine"]:q for q in qualities if q["format"]==fmt and q["id"]==fixture and q["quality"]==80}
            v,m=pair["vips"],pair["magick"]
            q80.append(dict(format=fmt,id=fixture,vips_bytes=v["bytes"],magick_bytes=m["bytes"],
                vips_ssim_min=min(v["ssim_black"],v["ssim_white"]),
                magick_ssim_min=min(m["ssim_black"],m["ssim_white"]),
                vips_alpha_mae=v["alpha_mae"],magick_alpha_mae=m["alpha_mae"]))
    (output / "quality-q80.json").write_text(json.dumps(q80,indent=2,allow_nan=False)+"\n")
    print(f"Generated 3 figures, {len(summary)} aggregate rows, and matched condition/Q80 tables for {pv['cohort']}")


if __name__=="__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("performance",type=Path);parser.add_argument("quality",type=Path);parser.add_argument("output",type=Path)
    parser.add_argument("--diagnostic-note",help="Required visible notice for investigation plots with a failed quality contract")
    args=parser.parse_args();report(args.performance,args.quality,args.output,args.diagnostic_note)
