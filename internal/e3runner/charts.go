package e3runner

import (
	"bytes"
	"fmt"
	"html"
	"math"
)

const chartFont = "font-family:system-ui,-apple-system,Segoe UI,sans-serif"

type modeStyle struct {
	color string
	dash  string
	label string
}

var modeStyles = map[string]modeStyle{
	"baseline":     {color: "#c0392b", dash: "", label: "M0 baseline"},
	"bounded-wait": {color: "#1f618d", dash: "7 4", label: "M1 bounded wait + shedding"},
	"kill-switch":  {color: "#1e8449", dash: "2 3", label: "M2 kill switch"},
}

// timelineSVG draws, for one fault, four panels: hit p99, hit affected rate,
// healthy-miss p99 and healthy-miss affected rate over the trial timeline,
// one line per isolation mode. The fault window is shaded and the mean kill
// switch on/off offsets are marked.
func timelineSVG(output AnalysisOutput, fault string) []byte {
	const width, height = 1180, 960
	const left, plotWidth = 110.0, 960.0
	const panelTop, panelHeight, panelGap = 110.0, 150.0, 52.0
	metadata := output.Metadata
	total := metadata.NormalDurationMS + metadata.FaultDurationMS + metadata.RecoveryDurationMS
	faultStart := metadata.NormalDurationMS
	faultEnd := faultStart + metadata.FaultDurationMS
	toX := func(offsetMS float64) float64 { return left + plotWidth*offsetMS/total }

	var svg bytes.Buffer
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, width, height, width, height)
	fmt.Fprintf(&svg, `<rect width="100%%" height="100%%" fill="#fff"/><style>text{%s;fill:#17202a}.axis{stroke:#657786;stroke-width:1}.grid{stroke:#e5eaee;stroke-width:1}.label{font-size:12px}.small{font-size:11px;fill:#52616b}.title{font-size:22px;font-weight:700}.subtitle{font-size:13px;fill:#52616b}.panel{font-size:14px;font-weight:600}</style>`, chartFont)
	fmt.Fprintf(&svg, `<text x="%.0f" y="36" class="title">Healthy request impact during fault %s</text>`, left, html.EscapeString(fault))
	fmt.Fprintf(&svg, `<text x="%.0f" y="58" class="subtitle">run %s · %d-second buckets by request start · mean over valid trials · shaded band = fault window%s</text>`, left, html.EscapeString(output.RunID), int(metadata.TimelineBucketMS/1000), calibrationLabel(output.Calibration))
	fmt.Fprintf(&svg, `<text x="%.0f" y="78" class="subtitle">affected = failed or slower than %.0f ms (hit) / %.0f ms (healthy miss)</text>`, left, output.Thresholds[StreamHit], output.Thresholds[StreamHealthyMiss])

	panels := []struct {
		stream string
		metric string
		title  string
	}{
		{StreamHit, "p99", "Hit stream p99 latency (ms, log scale)"},
		{StreamHit, "affected", "Hit stream affected rate (%)"},
		{StreamHealthyMiss, "p99", "Healthy miss stream p99 latency (ms, log scale)"},
		{StreamHealthyMiss, "affected", "Healthy miss stream affected rate (%)"},
	}
	killSwitch, hasKillSwitch := output.KillSwitch[fault]
	for index, panel := range panels {
		top := panelTop + float64(index)*(panelHeight+panelGap)
		bottom := top + panelHeight
		fmt.Fprintf(&svg, `<text x="%.0f" y="%.1f" class="panel">%s</text>`, left, top-10, html.EscapeString(panel.title))
		fmt.Fprintf(&svg, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="#fdebd0" opacity="0.7"/>`, toX(faultStart), top, toX(faultEnd)-toX(faultStart), panelHeight)
		if hasKillSwitch && killSwitch.Count > 0 {
			for _, offset := range []float64{killSwitch.OnMS, killSwitch.OffMS} {
				fmt.Fprintf(&svg, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#1e8449" stroke-width="1.5" stroke-dasharray="4 3"/>`, toX(offset), top, toX(offset), bottom)
			}
		}

		var toY func(value float64) float64
		if panel.metric == "p99" {
			maxValue := 10.0
			for _, row := range output.Timeline {
				if row.Fault == fault && row.Stream == panel.stream && row.Trials > 0 {
					maxValue = math.Max(maxValue, row.P99MeanMS)
				}
			}
			maxLog := math.Ceil(math.Log10(maxValue * 1.2))
			// Hit latencies sit well below 1 ms, so their panel starts at 0.1 ms.
			minLog := 0.0
			if panel.stream == StreamHit {
				minLog = -1
			}
			if maxLog <= minLog {
				maxLog = minLog + 1
			}
			floor := math.Pow(10, minLog)
			toY = func(value float64) float64 {
				value = math.Max(value, floor)
				return bottom - (math.Log10(value)-minLog)/(maxLog-minLog)*panelHeight
			}
			for exponent := minLog; exponent <= maxLog; exponent++ {
				value := math.Pow(10, exponent)
				y := toY(value)
				label := fmt.Sprintf("%.0f ms", value)
				if value < 1 {
					label = fmt.Sprintf("%.1f ms", value)
				}
				if value >= 1000 {
					label = fmt.Sprintf("%.0f s", value/1000)
				}
				fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" class="grid"/><text x="%.0f" y="%.1f" text-anchor="end" class="label">%s</text>`, left, y, left+plotWidth, y, left-8, y+4, label)
			}
		} else {
			toY = func(value float64) float64 { return bottom - value*panelHeight }
			for tick := 0; tick <= 4; tick++ {
				value := float64(tick) / 4
				y := toY(value)
				fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" class="grid"/><text x="%.0f" y="%.1f" text-anchor="end" class="label">%.0f%%</text>`, left, y, left+plotWidth, y, left-8, y+4, value*100)
			}
		}
		fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" class="axis"/><line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" class="axis"/>`, left, top, left, bottom, left, bottom, left+plotWidth, bottom)
		for second := 0.0; second <= total; second += 30000 {
			x := toX(second)
			fmt.Fprintf(&svg, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" class="axis"/><text x="%.1f" y="%.1f" text-anchor="middle" class="label">%.0f s</text>`, x, bottom, x, bottom+4, x, bottom+18, second/1000)
		}

		for _, mode := range metadata.Modes {
			style := modeStyles[mode]
			if style.color == "" {
				style = modeStyle{color: "#7f8c8d", label: mode}
			}
			points := ""
			for _, row := range output.Timeline {
				if row.Fault != fault || row.Mode != mode || row.Stream != panel.stream || row.Trials == 0 {
					continue
				}
				x := toX(row.BucketStartMS + metadata.TimelineBucketMS/2)
				value := row.P99MeanMS
				if panel.metric == "affected" {
					value = row.AffectedRateMean
				}
				points += fmt.Sprintf("%.1f,%.1f ", x, toY(value))
			}
			if points != "" {
				fmt.Fprintf(&svg, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2.2" stroke-dasharray="%s"/>`, points, style.color, style.dash)
			}
		}
	}

	legendY := panelTop + 4*(panelHeight+panelGap) + 4
	x := left
	for _, mode := range metadata.Modes {
		style := modeStyles[mode]
		if style.color == "" {
			style = modeStyle{color: "#7f8c8d", label: mode}
		}
		fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="%s" stroke-width="2.2" stroke-dasharray="%s"/><text x="%.0f" y="%.0f" class="label">%s</text>`, x, legendY, x+30, legendY, style.color, style.dash, x+36, legendY+4, html.EscapeString(style.label))
		x += 260
	}
	fmt.Fprintf(&svg, `<rect x="%.0f" y="%.0f" width="14" height="14" fill="#fdebd0"/><text x="%.0f" y="%.0f" class="label">fault window</text>`, x, legendY-7, x+20, legendY+4)
	if hasKillSwitch && killSwitch.Count > 0 {
		fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="#1e8449" stroke-width="1.5" stroke-dasharray="4 3"/><text x="%.0f" y="%.0f" class="label">kill switch on/off (mean)</text>`, x+130, legendY-8, x+130, legendY+8, x+138, legendY+4)
	}
	svg.WriteString(`</svg>`)
	return svg.Bytes()
}

func calibrationLabel(calibration bool) string {
	if calibration {
		return " · CALIBRATION (excluded from retained results)"
	}
	return ""
}
