//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestCodecConfigurationGate(t *testing.T) {
	line := "E2_CODEC encoder=AOMedia Project AV1 Encoder v3.12.1 threads=1 speed=5 query_errors=0,0\n"
	for _, test := range []struct {
		engine, log string
		want        int
	}{{"vips", strings.Repeat(line, 8), 1}, {"magick", strings.Repeat(strings.ReplaceAll(line, "threads=1", "threads=28"), 8), 28}, {"vips", strings.Repeat(line, 7), 0}, {"vips", strings.Repeat(strings.ReplaceAll(line, "threads=1", "threads=2"), 8), 0}, {"magick", strings.Repeat(strings.ReplaceAll(line, "speed=5", "speed=6"), 8), 0}, {"magick", strings.Repeat(strings.ReplaceAll(line, "query_errors=0,0", "query_errors=1,0"), 8), 0}} {
		got, err := parseCodec(test.log, test.engine)
		if got != test.want || (err != nil) != (test.want == 0) {
			t.Fatalf("%s got %d/%v, want %d", test.engine, got, err, test.want)
		}
	}
}
