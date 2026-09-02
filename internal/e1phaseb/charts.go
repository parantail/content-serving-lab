package e1phaseb

import (
	"fmt"
	"strings"
)

func unrelatedLatencySVG(summary []summaryRow) string {
	scenarios := []string{ScenarioS3ColdControl, ScenarioS3Cold, ScenarioS3WarmControl, ScenarioS3Warm}
	values := make([]float64, len(scenarios))
	maximum := 1.0
	for index, scenario := range scenarios {
		if row, ok := scenarioSummary(summary, scenario, RequestClassUnrelated); ok {
			values[index] = row.P99MeanMS
			if row.P99MeanMS > maximum {
				maximum = row.P99MeanMS
			}
		}
	}
	var bars strings.Builder
	for index, scenario := range scenarios {
		x := 95 + index*165
		height := int(values[index] / maximum * 220)
		y := 300 - height
		fmt.Fprintf(&bars, `<rect x="%d" y="%d" width="90" height="%d" fill="#3b82f6"/><text x="%d" y="325" text-anchor="middle" font-size="12">%s</text><text x="%d" y="%d" text-anchor="middle" font-size="12">%.2f ms</text>`, x, y, height, x+45, xmlEscape(scenario), x+45, y-8, values[index])
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="780" height="380" viewBox="0 0 780 380"><rect width="100%%" height="100%%" fill="white"/><text x="30" y="30" font-size="20" font-family="sans-serif">S3 unrelated request mean trial p99</text><line x1="60" y1="300" x2="750" y2="300" stroke="#111"/>%s</svg>`+"\n", bars.String())
}

func multiprocessWorkSVG(summary []summaryRow) string {
	scenarios := []string{ScenarioS42, ScenarioS44}
	var bars strings.Builder
	for index, scenario := range scenarios {
		row, _ := scenarioSummary(summary, scenario, RequestClassHot)
		x := 170 + index*280
		height := int(row.TransformAttemptsMean / 4 * 220)
		y := 300 - height
		fmt.Fprintf(&bars, `<rect x="%d" y="%d" width="100" height="%d" fill="#ef4444"/><text x="%d" y="325" text-anchor="middle" font-size="14">%s</text><text x="%d" y="%d" text-anchor="middle" font-size="13">%.2f transforms</text>`, x, y, height, x+50, scenario, x+50, y-8, row.TransformAttemptsMean)
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="700" height="380" viewBox="0 0 700 380"><rect width="100%%" height="100%%" fill="white"/><text x="30" y="30" font-size="20" font-family="sans-serif">Local S4 process count and duplicate transforms</text><line x1="60" y1="300" x2="650" y2="300" stroke="#111"/>%s</svg>`+"\n", bars.String())
}

func cancellationTimelineSVG(events []map[string]string) string {
	selected, err := firstF2Events(events)
	if err != nil || len(selected) == 0 {
		return `<svg xmlns="http://www.w3.org/2000/svg" width="900" height="160"><text x="20" y="30">No F2 events</text></svg>` + "\n"
	}
	start := parseEventTime(selected[0]["timestamp"])
	var points strings.Builder
	for index, event := range selected {
		x := 70 + index*135
		elapsed := parseEventTime(event["timestamp"]).Sub(start).Seconds() * 1000
		fmt.Fprintf(&points, `<circle cx="%d" cy="80" r="6" fill="#7c3aed"/><text x="%d" y="110" text-anchor="middle" font-size="11">%s</text><text x="%d" y="130" text-anchor="middle" font-size="10">+%.1f ms</text>`, x, x, xmlEscape(event["name"]), x, elapsed)
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="900" height="160" viewBox="0 0 900 160"><rect width="100%%" height="100%%" fill="white"/><text x="20" y="25" font-size="18" font-family="sans-serif">F2 first valid trial event timeline</text><line x1="70" y1="80" x2="850" y2="80" stroke="#111"/>%s</svg>`+"\n", points.String())
}
