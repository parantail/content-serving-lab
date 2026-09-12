package readmefigures

import (
	"fmt"
	"math"
	"strings"
)

// Figure is one generated README chart.
type Figure struct {
	Name string
	SVG  []byte
}

// Generate reads the retained analysis files and renders the README figures.
func Generate(sources Sources) ([]Figure, error) {
	e1, err := loadE1(sources)
	if err != nil {
		return nil, err
	}
	e2, err := loadE2(sources)
	if err != nil {
		return nil, err
	}
	e3, err := loadE3(sources)
	if err != nil {
		return nil, err
	}
	return []Figure{
		{Name: "e1-duplicate-transforms.svg", SVG: renderE1(e1)},
		{Name: "e2-throughput-ratio.svg", SVG: renderE2(e2)},
		{Name: "e3-blast-radius.svg", SVG: renderE3(e3)},
	}, nil
}

// renderE1 draws actual transforms per 100-request cold burst: the two
// coordinator modes in one process, then the AWS task counts with coalescing on.
func renderE1(data E1Data) []byte {
	const width, height = 960, 470
	const labelX, barX, barEnd, barHeight, rowPitch = 40.0, 330.0, 880.0, 22.0, 44.0
	c := newCanvas(width, height)
	c.text(40, 34, "title", "", fmt.Sprintf("같은 이미지의 동시 cold 요청 %d개 → 실제 이미지 변환 횟수", data.None.Concurrency))
	c.text(40, 58, "subtitle", "", fmt.Sprintf("cold = 파생 이미지가 아직 없는 상태 · 막대 = trial당 실제 변환 횟수 평균 · 모든 요청은 같은 %s 파생 이미지를 요청", data.LocalDerivative))

	maxValue := math.Max(data.None.TransformsMean, data.Singleflight.TransformsMean)
	for _, scenario := range data.AWSTasks {
		maxValue = math.Max(maxValue, scenario.TransformsMean)
	}
	scale := (barEnd - barX) / maxValue

	type row struct {
		label, code string
		value       float64
		color       string
	}
	drawRow := func(centerY float64, r row) {
		c.text(labelX, centerY+4, "label", "", r.label)
		c.text(labelX, centerY+19, "small", "", r.code)
		w := r.value * scale
		c.hbar(barX, centerY-barHeight/2, w, barHeight, r.color)
		c.text(barX+w+8, centerY+5, "value", "", fmt.Sprintf("%s회", num(r.value)))
	}

	y := 100.0
	c.text(labelX, y, "group", "", fmt.Sprintf("로컬 Docker %s · 프로세스 1개 · %d회 평균", vcpu(data.LocalCPUQuota, data.LocalMemoryBytes), data.None.ValidTrials))
	y += 32
	drawRow(y, row{"요청 합치기 없음", data.None.Mode, data.None.TransformsMean, colorBefore})
	y += rowPitch
	drawRow(y, row{"프로세스 안에서 같은 요청 합치기", data.Singleflight.Mode, data.Singleflight.TransformsMean, colorAfter})

	y += 56
	c.text(labelX, y, "group", "", fmt.Sprintf("AWS Fargate %s · Task별 1 vCPU·2 GiB · 프로세스 안에서 합치기 적용 · 요청 %d개를 Task에 균등 분배 · %d회 평균", data.AWSRegion, data.AWSRequests, data.AWSTasks[0].ValidTrials))
	y += 32
	for _, scenario := range data.AWSTasks {
		drawRow(y, row{fmt.Sprintf("Task %d개", scenario.Tasks), scenario.Scenario, scenario.TransformsMean, colorAfter})
		y += rowPitch
	}
	y += barHeight/2 + 12 - rowPitch
	c.line(barX, y, barEnd, y, "axis")

	legendY := y + 30
	x := c.swatchText(labelX, legendY, colorBefore, "label", "요청 합치기 없음 (none)")
	c.swatchText(x, legendY, colorAfter, "label", "프로세스 안에서 같은 요청 합치기 (process-singleflight)")
	c.text(labelX, legendY+26, "muted", "", fmt.Sprintf("로컬 %d개 요청의 p99 %s → %s · peak cgroup memory %s → %s (none → process-singleflight, %d회 평균)",
		data.None.Concurrency, seconds(data.None.P99MeanMS), seconds(data.Singleflight.P99MeanMS), mib(data.None.PeakMemoryBytes), mib(data.Singleflight.PeakMemoryBytes), data.None.ValidTrials))
	return c.bytes()
}

// renderE2 draws the libvips ÷ ImageMagick throughput ratio of every measured
// condition, grouped by output format, against a 1× reference.
func renderE2(data E2Data) []byte {
	const width, height = 960, 400
	const left, right, top, rowPitch = 130.0, 900.0, 92.0, 56.0
	c := newCanvas(width, height)
	c.text(40, 34, "title", "", "같은 조건에서 libvips 처리량은 ImageMagick의 몇 배인가")
	c.text(40, 58, "subtitle", "", fmt.Sprintf("AWS Fargate 1 vCPU·2 GiB, 요청 Q80 · 24입력 batch 다섯 반복 중앙값의 비율 · 포맷별 점 %d개 = geometry 3종 × 동시성 1/4, 막대 = 최소~최대", len(data.Formats[0].Ratios)))

	maxRatio := 0.0
	for _, format := range data.Formats {
		maxRatio = math.Max(maxRatio, format.Ratios[len(format.Ratios)-1])
	}
	axisMax := math.Ceil(maxRatio)
	scale := (right - left) / axisMax
	bottom := top + rowPitch*float64(len(data.Formats))
	for tick := 0.0; tick <= axisMax; tick++ {
		x := left + tick*scale
		class := "grid"
		if tick == 1 {
			class = "axis"
		}
		c.line(x, top, x, bottom, class)
		c.text(x, bottom+20, "label", "middle", fmt.Sprintf("%s×", num(tick)))
	}
	c.text(left+scale, top-8, "muted", "middle", "1× = 같은 처리량")
	c.line(left, bottom, right, bottom, "axis")

	for i, format := range data.Formats {
		y := top + rowPitch*float64(i) + rowPitch/2
		c.text(40, y+5, "group", "", strings.Replace(strings.ToUpper(format.Format), "WEBP", "WebP", 1))
		low, high := format.Ratios[0], format.Ratios[len(format.Ratios)-1]
		c.rect(left+low*scale, y-4, (high-low)*scale, 8, colorRange, 4)
		for _, ratio := range format.Ratios {
			c.dot(left+ratio*scale, y, 5, colorAfter)
		}
		c.text(left+low*scale-12, y+5, "value", "end", fmt.Sprintf("%.2f×", low))
		c.text(left+high*scale+12, y+5, "value", "", fmt.Sprintf("%.2f×", high))
	}
	c.text(40, bottom+50, "muted", "", fmt.Sprintf("worker peak RSS는 %d개 조건 중 %d개에서 libvips가 더 높았음 · 품질(SSIM)과 파일 크기의 입력별 상충은 보고서 참조", data.Conditions, data.RSSHigherWithVips))
	return c.bytes()
}

// renderE3 draws the healthy-miss p99 timeline of the transform-timeout fault
// for the baseline and bounded-wait modes, with the hit stream as context.
func renderE3(data E3Data) []byte {
	const width, height = 960, 470
	const left, right, top, bottom = 70.0, 900.0, 100.0, 390.0
	c := newCanvas(width, height)
	c.text(40, 34, "title", "", "변환 정지 장애 중 다른 이미지 변환 요청의 p99 응답 시간")
	c.text(40, 56, "subtitle", "", fmt.Sprintf("로컬 Docker %s · 오염된 요청이 변환 slot %d개를 %s timeout까지 점유 · %s 구간 p99, %d회 평균",
		vcpu(data.CPUQuota, data.MemoryBytes), data.TransformSlots, seconds(data.TransformTimeoutMS), seconds(data.BucketMS), data.Repetitions))
	c.text(40, 74, "subtitle", "", "정상 요청 stream만 표시 · kill switch 모드(M2)는 보고서 참조")

	totalMS := data.NormalMS + data.FaultMS + data.RecoveryMS
	maxSeconds := 0.0
	for _, series := range [][]E3Point{data.BaselineMiss, data.BoundedMiss, data.BaselineHit} {
		for _, point := range series {
			maxSeconds = math.Max(maxSeconds, point.P99MS/1000)
		}
	}
	axisMax := math.Ceil(maxSeconds/5) * 5
	toX := func(ms float64) float64 { return left + (right-left)*ms/totalMS }
	toY := func(ms float64) float64 { return bottom - (bottom-top)*(ms/1000)/axisMax }

	faultStart, faultEnd := data.NormalMS, data.NormalMS+data.FaultMS
	c.rect(toX(faultStart), top, toX(faultEnd)-toX(faultStart), bottom-top, colorBand, 0)
	c.text((toX(faultStart)+toX(faultEnd))/2, top-8, "muted", "middle", fmt.Sprintf("장애 주입 %s~%s초", num(faultStart/1000), num(faultEnd/1000)))
	for tick := 0.0; tick <= axisMax; tick += 5 {
		y := toY(tick * 1000)
		c.line(left, y, right, y, "grid")
		c.text(left-10, y+4, "label", "end", fmt.Sprintf("%s초", num(tick)))
	}
	for tick := 0.0; tick <= totalMS; tick += 30000 {
		x := toX(tick)
		c.line(x, bottom, x, bottom+5, "axis")
		c.text(x, bottom+22, "label", "middle", fmt.Sprintf("%s초", num(tick/1000)))
	}
	c.line(left, top, left, bottom, "axis")
	c.line(left, bottom, right, bottom, "axis")
	c.text((left+right)/2, bottom+44, "muted", "middle", "요청 시작 시각 (trial 시작 기준)")

	plot := func(series []E3Point, color string) {
		points := make([][2]float64, 0, len(series))
		for _, point := range series {
			points = append(points, [2]float64{toX(point.StartMS + data.BucketMS/2), toY(point.P99MS)})
		}
		c.polyline(points, color)
	}
	plot(data.BaselineHit, colorContext)
	plot(data.BaselineMiss, colorBefore)
	plot(data.BoundedMiss, colorAfter)

	labelCentered := func(centerX, y float64, color, s string) {
		w := textWidth(s, 13)
		c.rect(centerX-w/2-18, y-10, 12, 12, color, 2)
		c.text(centerX-w/2, y, "label", "", s)
	}
	labelCentered(toX(faultStart+data.FaultMS/2), toY(data.BaselineFaultP99MS)-12, colorBefore,
		fmt.Sprintf("baseline: 장애 구간 p99 %s · 장애 종료 뒤 %s 더 영향", seconds(data.BaselineFaultP99MS), seconds(data.BaselineLastImpact)))
	labelCentered(toX(faultStart+data.FaultMS*0.4), toY(data.BoundedFaultP99MS)-12, colorAfter,
		fmt.Sprintf("%s bounded wait: p99 %s (%.1f%%는 즉시 503) · 장애 종료 뒤 영향 %s", seconds(data.SlotWaitLimitMS), seconds(data.BoundedFaultP99MS), data.BoundedErrorRate*100, seconds(data.BoundedLastImpact)))
	hitLabel := fmt.Sprintf("캐시 적중 요청(baseline): 오류 %s · p99 %.0fms 이하", num(data.HitFaultErrors), math.Ceil(data.HitMaxP99MS))
	hitX := right - 2 - textWidth(hitLabel, 13)
	c.rect(hitX-18, 240-10, 12, 12, colorContext, 2)
	c.text(hitX, 240, "label", "", hitLabel)

	legendY := bottom + 66
	x := c.swatchText(40, legendY, colorBefore, "label", "M0 baseline (변경 전)")
	x = c.swatchText(x, legendY, colorAfter, "label", fmt.Sprintf("M1 %s bounded wait + shedding (채택)", seconds(data.SlotWaitLimitMS)))
	c.swatchText(x, legendY, colorContext, "label", "캐시 적중 요청 stream (baseline)")
	return c.bytes()
}
