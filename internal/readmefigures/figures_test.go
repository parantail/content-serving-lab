package readmefigures

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

// retainedSources returns the default sources or skips the test when the
// retained runs are absent, as in the Docker build context which excludes
// experiments/*/results and reports/.
func retainedSources(t *testing.T) Sources {
	t.Helper()
	sources := DefaultSources(repoRoot)
	for _, path := range []string{sources.E1LocalRun, sources.E1LocalSummary, sources.E1AWSRun, sources.E1AWSSummary, sources.E2Conditions, sources.E3Run, sources.E3AnalysisDir} {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("retained sources not available in this checkout: %v", err)
		}
	}
	return sources
}

func TestLoadE1UsesRetainedScenarios(t *testing.T) {
	data, err := loadE1(retainedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	if data.None.Mode != "none" || data.Singleflight.Mode != "process-singleflight" {
		t.Fatalf("unexpected modes %q and %q", data.None.Mode, data.Singleflight.Mode)
	}
	if data.None.Concurrency != 100 || data.Singleflight.Concurrency != 100 {
		t.Fatalf("expected the 100-request scenarios, got %d and %d", data.None.Concurrency, data.Singleflight.Concurrency)
	}
	if data.None.TransformsMean <= data.Singleflight.TransformsMean {
		t.Fatalf("coalescing must reduce transforms: none=%v singleflight=%v", data.None.TransformsMean, data.Singleflight.TransformsMean)
	}
	if len(data.AWSTasks) != 3 {
		t.Fatalf("expected three AWS task counts, got %+v", data.AWSTasks)
	}
	for i, scenario := range data.AWSTasks {
		if scenario.ValidTrials != data.AWSRepetitions {
			t.Errorf("%s: %d valid trials, want %d", scenario.Scenario, scenario.ValidTrials, data.AWSRepetitions)
		}
		if i > 0 && scenario.Tasks <= data.AWSTasks[i-1].Tasks {
			t.Errorf("AWS scenarios must be ordered by task count: %+v", data.AWSTasks)
		}
	}
}

func TestLoadE2GroupsSixConditionsPerFormat(t *testing.T) {
	data, err := loadE2(retainedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Formats) != 4 || data.Conditions != 24 {
		t.Fatalf("expected 4 formats over 24 conditions, got %d formats over %d", len(data.Formats), data.Conditions)
	}
	for _, format := range data.Formats {
		if len(format.Ratios) != 6 {
			t.Errorf("%s: %d conditions, want 6", format.Format, len(format.Ratios))
		}
		for i := 1; i < len(format.Ratios); i++ {
			if format.Ratios[i] < format.Ratios[i-1] {
				t.Errorf("%s: ratios not sorted: %v", format.Format, format.Ratios)
			}
		}
	}
}

func TestLoadE3CoversWholeTimeline(t *testing.T) {
	data, err := loadE3(retainedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	buckets := int((data.NormalMS + data.FaultMS + data.RecoveryMS) / data.BucketMS)
	for name, series := range map[string][]E3Point{"baseline miss": data.BaselineMiss, "bounded miss": data.BoundedMiss, "baseline hit": data.BaselineHit} {
		if len(series) != buckets {
			t.Errorf("%s: %d buckets, want %d", name, len(series), buckets)
		}
	}
	if data.BaselineFaultP99MS <= data.BoundedFaultP99MS {
		t.Errorf("bounded wait must cap the fault-phase p99: baseline=%v bounded=%v", data.BaselineFaultP99MS, data.BoundedFaultP99MS)
	}
	if data.BoundedFaultP99MS > data.SlotWaitLimitMS*1.1 {
		t.Errorf("bounded-wait p99 %v exceeds the slot wait limit %v", data.BoundedFaultP99MS, data.SlotWaitLimitMS)
	}
	if data.HitFaultErrors != 0 {
		t.Errorf("hit stream reported errors during the fault: %v", data.HitFaultErrors)
	}
}

func TestFiguresCarryValuesFromSources(t *testing.T) {
	sources := retainedSources(t)
	figures, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}
	e1, _ := loadE1(sources)
	e2, _ := loadE2(sources)
	e3, _ := loadE3(sources)
	want := map[string][]string{
		"e1-duplicate-transforms.svg": {
			fmt.Sprintf(">%s회<", num(e1.None.TransformsMean)),
			fmt.Sprintf(">%s회<", num(e1.Singleflight.TransformsMean)),
			fmt.Sprintf("Task %d개", e1.AWSTasks[len(e1.AWSTasks)-1].Tasks),
			seconds(e1.None.P99MeanMS),
		},
		"e2-throughput-ratio.svg": {
			fmt.Sprintf("%.2f×", e2.Formats[0].Ratios[0]),
			fmt.Sprintf("%.2f×", e2.Formats[3].Ratios[5]),
			fmt.Sprintf("%d개 조건 중 %d개", e2.Conditions, e2.RSSHigherWithVips),
		},
		"e3-blast-radius.svg": {
			seconds(e3.BaselineFaultP99MS),
			seconds(e3.BoundedFaultP99MS),
			seconds(e3.BaselineLastImpact),
		},
	}
	for _, figure := range figures {
		svg := string(figure.SVG)
		if !strings.HasPrefix(svg, `<?xml version="1.0" encoding="UTF-8"?>`) || !strings.HasSuffix(svg, "</svg>\n") {
			t.Errorf("%s: not a complete SVG document", figure.Name)
		}
		for _, fragment := range want[figure.Name] {
			if !strings.Contains(svg, fragment) {
				t.Errorf("%s: missing %q", figure.Name, fragment)
			}
		}
		delete(want, figure.Name)
	}
	if len(want) != 0 {
		t.Errorf("figures not generated: %v", want)
	}
}

// TestCommittedFiguresAreCurrent fails when docs/figures no longer matches the
// retained sources; run `go run ./cmd/readme-figures` to refresh them.
func TestCommittedFiguresAreCurrent(t *testing.T) {
	figures, err := Generate(retainedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, figure := range figures {
		path := filepath.Join(repoRoot, "docs", "figures", figure.Name)
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v (run: go run ./cmd/readme-figures)", figure.Name, err)
			continue
		}
		if !bytes.Equal(committed, figure.SVG) {
			t.Errorf("docs/figures/%s is stale; run: go run ./cmd/readme-figures", figure.Name)
		}
	}
}

func TestTextWidthTreatsHangulAsFullWidth(t *testing.T) {
	if hangul, latin := textWidth("가나", 10), textWidth("ab", 10); hangul <= latin {
		t.Errorf("hangul width %v should exceed latin width %v", hangul, latin)
	}
	if got := formatThousands(1234567); got != "1,234,567" {
		t.Errorf("formatThousands = %q", got)
	}
	if got := vcpu("100000 100000", 2147483648); got != "1 vCPU·2 GiB" {
		t.Errorf("vcpu = %q", got)
	}
	if got := seconds(31778.17); got != "31.8초" {
		t.Errorf("seconds = %q", got)
	}
	if got := seconds(355.79); got != "0.36초" {
		t.Errorf("seconds = %q", got)
	}
	if got := seconds(20000); got != "20초" {
		t.Errorf("seconds = %q", got)
	}
	if got := seconds(0); got != "0초" {
		t.Errorf("seconds = %q", got)
	}
}

// TestRenderSyntheticData exercises the renderers without the retained runs so
// the drawing code is still covered where the sources are absent.
func TestRenderSyntheticData(t *testing.T) {
	e1 := renderE1(E1Data{
		LocalCPUQuota: "100000 100000", LocalMemoryBytes: 2 << 30, LocalDerivative: "640×640 WebP",
		None:         E1LocalScenario{Mode: "none", Concurrency: 100, ValidTrials: 10, TransformsMean: 100, P99MeanMS: 31778, PeakMemoryBytes: 1417632563},
		Singleflight: E1LocalScenario{Mode: "process-singleflight", Concurrency: 100, ValidTrials: 10, TransformsMean: 1, P99MeanMS: 356, PeakMemoryBytes: 229259264},
		AWSRegion:    "ap-northeast-2", AWSRequests: 100,
		AWSTasks: []E1AWSScenario{{Scenario: "S4-AWS-1-CONTROL", Tasks: 1, ValidTrials: 10, TransformsMean: 1}, {Scenario: "S4-AWS-4", Tasks: 4, ValidTrials: 10, TransformsMean: 4}},
	})
	for _, fragment := range []string{">100회<", ">4회<", "31.8초", "1,352 MiB"} {
		if !strings.Contains(string(e1), fragment) {
			t.Errorf("E1 figure missing %q", fragment)
		}
	}

	formats := make([]E2Format, 0, len(e2FormatOrder))
	for i, format := range e2FormatOrder {
		formats = append(formats, E2Format{Format: format, Ratios: []float64{1.2 + float64(i)/10, 2.5}})
	}
	e2 := renderE2(E2Data{Formats: formats, Conditions: 8, RSSHigherWithVips: 7})
	for _, fragment := range []string{"WebP", "1.20×", "2.50×", "8개 조건 중 7개"} {
		if !strings.Contains(string(e2), fragment) {
			t.Errorf("E2 figure missing %q", fragment)
		}
	}

	var miss, capped, hit []E3Point
	for start := 0.0; start < 150000; start += 5000 {
		p99 := 570.0
		if start >= 30000 && start < 90000 {
			p99 = 17000
		}
		miss = append(miss, E3Point{StartMS: start, P99MS: p99})
		capped = append(capped, E3Point{StartMS: start, P99MS: math.Min(p99, 2000)})
		hit = append(hit, E3Point{StartMS: start, P99MS: 1})
	}
	e3 := renderE3(E3Data{
		Repetitions: 5, CPUQuota: "100000 100000", MemoryBytes: 2 << 30, TransformSlots: 4, TransformTimeoutMS: 20000, SlotWaitLimitMS: 2000,
		NormalMS: 30000, FaultMS: 60000, RecoveryMS: 60000, BucketMS: 5000,
		BaselineMiss: miss, BoundedMiss: capped, BaselineHit: hit,
		BaselineFaultP99MS: 17290, BaselineLastImpact: 11216, BoundedFaultP99MS: 2001, BoundedErrorRate: 0.8467, HitMaxP99MS: 63.1,
	})
	for _, fragment := range []string{"장애 주입 30~90초", "17.3초", "11.2초", "84.7%", "64ms", "<polyline"} {
		if !strings.Contains(string(e3), fragment) {
			t.Errorf("E3 figure missing %q", fragment)
		}
	}
}
