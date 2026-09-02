package e1runner

import (
	"bytes"
	"fmt"
	"html"
	"math"
)

const chartFont = "font-family:system-ui,-apple-system,Segoe UI,sans-serif"

func transformCountSVG(output AnalysisOutput) []byte {
	const width, height = 960, 560
	const left, top, plotWidth, plotHeight = 90.0, 90.0, 790.0, 360.0
	concurrencies := []int{10, 50, 100}
	maxValue := 1.0
	for _, row := range output.Summary {
		if (row.Mode == "none" || row.Mode == "process-singleflight") && float64(row.TransformAttemptsMax) > maxValue {
			maxValue = float64(row.TransformAttemptsMax)
		}
	}
	maxValue = math.Ceil(maxValue/10) * 10
	if maxValue < 10 {
		maxValue = 10
	}
	var svg bytes.Buffer
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, width, height, width, height)
	fmt.Fprintf(&svg, `<rect width="100%%" height="100%%" fill="#fff"/><style>text{%s;fill:#17202a}.axis{stroke:#657786;stroke-width:1}.grid{stroke:#dfe6e9;stroke-width:1}.label{font-size:13px}.title{font-size:23px;font-weight:700}.subtitle{font-size:13px;fill:#52616b}</style>`, chartFont)
	fmt.Fprintf(&svg, `<text x="90" y="38" class="title">Actual transforms per cold burst</text><text x="90" y="62" class="subtitle">run %s · per-trial mean; whiskers show min–max · valid trials only%s</text>`, html.EscapeString(output.RunID), calibrationLabel(output.Calibration))
	for tick := 0; tick <= 5; tick++ {
		value := maxValue * float64(tick) / 5
		y := top + plotHeight - plotHeight*value/maxValue
		fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" class="grid"/><text x="80" y="%.1f" text-anchor="end" class="label">%.0f</text>`, left, y, left+plotWidth, y, y+5, value)
	}
	fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" class="axis"/><line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" class="axis"/>`, left, top, left, top+plotHeight, left, top+plotHeight, left+plotWidth, top+plotHeight)
	colors := map[string]string{"none": "#d35454", "process-singleflight": "#2874a6"}
	for groupIndex, concurrency := range concurrencies {
		center := left + plotWidth*float64(groupIndex+1)/4
		fmt.Fprintf(&svg, `<text x="%.1f" y="480" text-anchor="middle" class="label">%d concurrent requests</text>`, center, concurrency)
		for modeIndex, mode := range []string{"none", "process-singleflight"} {
			row, found := findSummary(output.Summary, mode, concurrency)
			if !found || row.ValidTrials == 0 {
				continue
			}
			x := center - 45 + float64(modeIndex)*55
			barHeight := plotHeight * row.TransformAttemptsMean / maxValue
			y := top + plotHeight - barHeight
			fmt.Fprintf(&svg, `<rect x="%.1f" y="%.1f" width="38" height="%.1f" rx="3" fill="%s"/><text x="%.1f" y="%.1f" text-anchor="middle" class="label">%.1f</text>`, x, y, barHeight, colors[mode], x+19, y-8, row.TransformAttemptsMean)
			minY := top + plotHeight - plotHeight*float64(row.TransformAttemptsMin)/maxValue
			maxY := top + plotHeight - plotHeight*float64(row.TransformAttemptsMax)/maxValue
			fmt.Fprintf(&svg, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#17202a"/><line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#17202a"/><line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#17202a"/>`, x+19, minY, x+19, maxY, x+13, minY, x+25, minY, x+13, maxY, x+25, maxY)
		}
	}
	fmt.Fprintf(&svg, `<rect x="280" y="520" width="14" height="14" fill="#d35454"/><text x="300" y="532" class="label">none</text><rect x="400" y="520" width="14" height="14" fill="#2874a6"/><text x="420" y="532" class="label">process-singleflight</text></svg>`)
	return svg.Bytes()
}

func latencyErrorSVG(output AnalysisOutput) []byte {
	const width, height = 1080, 650
	const left, top, plotWidth, plotHeight = 100.0, 95.0, 870.0, 390.0
	concurrencies := []int{10, 50, 100}
	maxLatency := 1000.0
	for _, row := range output.Summary {
		if row.P99MeanMS > maxLatency {
			maxLatency = row.P99MeanMS
		}
	}
	maxLog := math.Ceil(math.Log10(maxLatency * 1.15))
	minLog := 2.0
	toY := func(value float64) float64 {
		value = math.Max(value, math.Pow(10, minLog))
		return top + plotHeight - (math.Log10(value)-minLog)/(maxLog-minLog)*plotHeight
	}
	var svg bytes.Buffer
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, width, height, width, height)
	fmt.Fprintf(&svg, `<rect width="100%%" height="100%%" fill="#fff"/><style>text{%s;fill:#17202a}.axis{stroke:#657786;stroke-width:1}.grid{stroke:#dfe6e9;stroke-width:1}.label{font-size:13px}.small{font-size:11px}.title{font-size:23px;font-weight:700}.subtitle{font-size:13px;fill:#52616b}</style>`, chartFont)
	fmt.Fprintf(&svg, `<text x="100" y="38" class="title">Per-trial latency percentiles and error rate</text><text x="100" y="62" class="subtitle">run %s · means of per-trial percentiles; log latency axis · valid trials only%s</text>`, html.EscapeString(output.RunID), calibrationLabel(output.Calibration))
	for exponent := minLog; exponent <= maxLog; exponent++ {
		value := math.Pow(10, exponent)
		y := toY(value)
		label := fmt.Sprintf("%.0f ms", value)
		if value >= 1000 {
			label = fmt.Sprintf("%.0f s", value/1000)
		}
		fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" class="grid"/><text x="90" y="%.1f" text-anchor="end" class="label">%s</text>`, left, y, left+plotWidth, y, y+5, label)
	}
	fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" class="axis"/><line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" class="axis"/>`, left, top, left, top+plotHeight, left, top+plotHeight, left+plotWidth, top+plotHeight)
	type series struct{ mode, quantile, color, dash string }
	seriesList := []series{
		{"none", "p50", "#c0392b", ""}, {"none", "p95", "#c0392b", "7 4"}, {"none", "p99", "#c0392b", "2 3"},
		{"process-singleflight", "p50", "#1f618d", ""}, {"process-singleflight", "p95", "#1f618d", "7 4"}, {"process-singleflight", "p99", "#1f618d", "2 3"},
	}
	for _, item := range seriesList {
		points := ""
		for index, concurrency := range concurrencies {
			row, found := findSummary(output.Summary, item.mode, concurrency)
			if !found || row.ValidTrials == 0 {
				continue
			}
			x := left + plotWidth*float64(index+1)/4
			value := row.P50MeanMS
			if item.quantile == "p95" {
				value = row.P95MeanMS
			}
			if item.quantile == "p99" {
				value = row.P99MeanMS
			}
			y := toY(value)
			points += fmt.Sprintf("%.1f,%.1f ", x, y)
			fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="4" fill="%s"/>`, x, y, item.color)
		}
		fmt.Fprintf(&svg, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2.5" stroke-dasharray="%s"/>`, points, item.color, item.dash)
	}
	for index, concurrency := range concurrencies {
		x := left + plotWidth*float64(index+1)/4
		fmt.Fprintf(&svg, `<text x="%.1f" y="515" text-anchor="middle" class="label">%d concurrent</text>`, x, concurrency)
		for offset, mode := range []string{"none", "process-singleflight"} {
			row, found := findSummary(output.Summary, mode, concurrency)
			if found {
				fmt.Fprintf(&svg, `<text x="%.1f" y="%d" text-anchor="middle" class="small" fill="%s">%s errors %.1f%%</text>`, x, 538+offset*17, map[string]string{"none": "#c0392b", "process-singleflight": "#1f618d"}[mode], mode, row.ErrorRateMean*100)
			}
		}
	}
	legendY := 598
	for index, item := range seriesList {
		x := 90 + (index%3)*150 + (index/3)*500
		fmt.Fprintf(&svg, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="2.5" stroke-dasharray="%s"/><text x="%d" y="%d" class="label">%s %s</text>`, x, legendY, x+28, legendY, item.color, item.dash, x+34, legendY+5, item.mode, item.quantile)
	}
	svg.WriteString(`</svg>`)
	return svg.Bytes()
}

func findSummary(summary []SummaryRow, mode string, concurrency int) (SummaryRow, bool) {
	for _, row := range summary {
		if row.Mode == mode && row.Concurrency == concurrency && (row.Scenario == fmt.Sprintf("S1-%d", concurrency) || row.Scenario == fmt.Sprintf("S2-%d", concurrency)) {
			return row, true
		}
	}
	return SummaryRow{}, false
}

func calibrationLabel(calibration bool) string {
	if calibration {
		return " · CALIBRATION (excluded from retained results)"
	}
	return ""
}
