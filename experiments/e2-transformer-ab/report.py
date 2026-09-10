"""Regenerate E2 figures from independently analyzed, single-cohort run data."""
import argparse
import json
from collections import defaultdict
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np


def read(path):
    return json.loads(path.read_text())


def report(performance, quality, output):
    pv, qv = read(performance / "verification.json"), read(quality / "verification.json")
    if pv["mode"] != "measure" or qv["mode"] != "quality" or pv["cohort"] != qv["cohort"]:
        raise ValueError("Require performance/quality runs from one cohort")
    if pv["transforms"] != 5760 or qv["decoded_outputs"] != 192:
        raise ValueError("full matrix required")
    output.mkdir(parents=True, exist_ok=False)
    batches, transforms, qualities = read(performance / "batches.json"), read(performance / "transforms.json"), read(quality / "quality.json")
    colors = {"vips":"#1565c0", "magick":"#d45d00"}
    formats = ["jpeg", "png", "webp", "avif"]
    ops = ["resize", "contain", "cover"]
    groups = defaultdict(list)
    for b in batches:
        groups[(b["engine"], b["operation"], b["format"], b["concurrency"])].append(b)
    summary = []
    fig, axes = plt.subplots(2, 2, figsize=(12, 9))
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
            ax.annotate(f"{label}{c}", (xm,ym), xytext=(4,4), textcoords="offset points", fontsize=7)
            summary.append(dict(engine=engine, operation=op, format=fmt, concurrency=c,
                                throughput_median=float(ym), throughput_min=float(y.min()), throughput_max=float(y.max()),
                                rss_mib_median=float(xm), rss_mib_min=float(x.min()), rss_mib_max=float(x.max()),
                                p95_ms_median=float(np.median([b["p95_ms"] for b in values]))))
        ax.set(title=fmt.upper(), xlabel="Worker lifetime peak RSS (MiB)", ylabel="Successful transforms / second")
        ax.grid(alpha=.2)
    fig.suptitle(f"{pv['cohort']}: Q80, median and min/max of 5 repeats\nBlue: libvips; orange: ImageMagick. Labels: resize/fit/cover (r/f/c) + concurrency; filled: c4", fontsize=11)
    fig.tight_layout(rect=(0,0,1,.94)); fig.savefig(output / "throughput-rss.png", dpi=160); plt.close(fig)
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
                ax.plot(x,y,"o-",color=colors[engine],markersize=3)
                for px,py,q in zip(x,y,points): ax.annotate(str(q["quality"]),(px,py),fontsize=6,xytext=(2,2),textcoords="offset points")
            ax.set_title(f"{fmt.upper()} / {fixture}",fontsize=7)
            ax.set_xlabel("KiB",fontsize=8);ax.set_ylabel("min SSIM black/white",fontsize=8);ax.grid(alpha=.2);ax.tick_params(labelsize=7)
    fig.suptitle(f"{pv['cohort']}: independent reference SSIM vs encoded bytes; labels Q50/65/80/90\nBlue: libvips; orange: ImageMagick. Same Q does not mean equal quality.",fontsize=12)
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
        ax.set_yticks(range(len(fixtures)),fixtures,fontsize=7)
        ax.set_xticks(range(len(columns)),[f"{op}/{fmt}" for op,fmt in columns],rotation=90,fontsize=7)
        ax.set_title(f"c{c}: log2(ImageMagick/libvips {metric})",fontsize=10)
        fig.colorbar(plot,ax=ax,shrink=.7,label="0=equal, +1=2x, -1=0.5x")
    fig.suptitle(f"{pv['cohort']}: input/geometry/output matrix, Q80; all analyzed transforms successful\nPositive duration ratio means libvips faster; positive byte ratio means libvips smaller. Quality is not held equal.",fontsize=13)
    fig.tight_layout(rect=(0,0,1,.95));fig.savefig(output / "input-matrix.png",dpi=130);plt.close(fig)
    (output / "summary.json").write_text(json.dumps(sorted(summary,key=lambda x:(x['format'],x['operation'],x['concurrency'],x['engine'])),indent=2,allow_nan=False)+"\n")
    print(f"Generated 3 figures and {len(summary)} aggregate rows for {pv['cohort']}")


if __name__=="__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("performance",type=Path);parser.add_argument("quality",type=Path);parser.add_argument("output",type=Path)
    args=parser.parse_args();report(args.performance,args.quality,args.output)
