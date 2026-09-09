package e2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const Contract = "e2-native-v1"
const MaxInputBytes = 32 << 20
const MaxPixels = 20_000_000
const MaxDimension = 8192

type Reference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type Fixture struct {
	ID            string               `json:"id"`
	Path          string               `json:"path"`
	Group         string               `json:"group"`
	Size          string               `json:"size"`
	Format        string               `json:"format"`
	Width         int                  `json:"width"`
	Height        int                  `json:"height"`
	Orientation   int                  `json:"orientation"`
	HasAlpha      bool                 `json:"has_alpha"`
	Bytes         int                  `json:"bytes"`
	SHA256        string               `json:"sha256"`
	QualitySample bool                 `json:"quality_sample"`
	References    map[string]Reference `json:"references"`
}
type Corpus struct {
	Schema   string      `json:"schema"`
	Fixtures []Fixture   `json:"fixtures"`
	Invalid  []Reference `json:"invalid"`
}
type Spec struct {
	Operation string `json:"operation"`
	Format    string `json:"format"`
	Quality   int    `json:"quality"`
}

func (s Spec) Validate() error {
	if s.Operation != "resize" && s.Operation != "contain" && s.Operation != "cover" {
		return errors.New("invalid operation")
	}
	if s.Format != "jpeg" && s.Format != "png" && s.Format != "webp" && s.Format != "avif" {
		return errors.New("invalid format")
	}
	if s.Quality < 1 || s.Quality > 100 {
		return errors.New("invalid quality")
	}
	return nil
}
func InputBytes(b []byte) error {
	if len(b) == 0 || len(b) > MaxInputBytes {
		return errors.New("input byte limit")
	}
	if bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) || bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
		return nil
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return nil
	}
	if len(b) >= 16 && string(b[4:8]) == "ftyp" && (bytes.Contains(b[8:min(len(b), 40)], []byte("avif"))) {
		return nil
	}
	return errors.New("unsupported input format")
}
func Dimensions(w, h int) error {
	if w < 1 || h < 1 || w > MaxDimension || h > MaxDimension || int64(w)*int64(h) > MaxPixels {
		return errors.New("input pixel limit")
	}
	return nil
}
func Geometry(w, h int, op string) (rw, rh, x, y, ow, oh int) {
	scale := math.Min(1, 640/float64(max(w, h)))
	if op == "contain" {
		scale = math.Min(1, math.Min(640/float64(w), 480/float64(h)))
	}
	if op == "cover" {
		scale = math.Min(1, math.Max(640/float64(w), 640/float64(h)))
	}
	rw, rh = int(float64(w)*scale+.5), int(float64(h)*scale+.5)
	ow, oh = rw, rh
	if op == "cover" {
		ow, oh = min(640, rw), min(640, rh)
		x, y = (rw-ow)/2, (rh-oh)/2
	}
	return
}
func Hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func LoadCorpus(root string) (Corpus, string, error) {
	var c Corpus
	b, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return c, "", err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, "", err
	}
	if c.Schema != "e2-corpus-v1" || len(c.Fixtures) != 24 {
		return c, "", errors.New("corpus schema/count")
	}
	seen := map[string]bool{}
	q := 0
	for _, f := range c.Fixtures {
		if seen[f.ID] || !SafeName(f.ID) || !SafeName(f.Path) {
			return c, "", errors.New("fixture identity")
		}
		seen[f.ID] = true
		if f.QualitySample {
			q++
		}
		data, e := os.ReadFile(filepath.Join(root, f.Path))
		if e != nil {
			return c, "", e
		}
		if len(data) != f.Bytes || Hash(data) != f.SHA256 {
			return c, "", fmt.Errorf("fixture hash %s", f.ID)
		}
		if len(f.References) != 3 {
			return c, "", errors.New("references missing")
		}
		for op, r := range f.References {
			if !strings.HasPrefix(r.Path, "references/") || strings.Contains(r.Path, "..") {
				return c, "", errors.New("reference path")
			}
			v, e := os.ReadFile(filepath.Join(root, r.Path))
			if e != nil {
				return c, "", e
			}
			if Hash(v) != r.SHA256 {
				return c, "", errors.New("reference hash")
			}
			_, _, _, _, w, h := Geometry(f.Width, f.Height, op)
			if w != r.Width || h != r.Height {
				return c, "", errors.New("reference dimensions")
			}
		}
	}
	if q != 8 {
		return c, "", errors.New("quality sample count")
	}
	return c, Hash(b), nil
}
func SafeName(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/\\:") && !strings.Contains(s, "..")
}

type Transformer interface {
	Transform([]byte, Spec) ([]byte, error)
	Version() string
}

type Batch struct {
	ID          string `json:"id"`
	Root        string `json:"root"`
	Output      string `json:"output"`
	Spec        Spec   `json:"spec"`
	Concurrency int    `json:"concurrency"`
	Repetition  int    `json:"repetition"`
	Seed        int64  `json:"seed"`
	Quality     bool   `json:"quality"`
	Save        bool   `json:"save"`
	Warmup      bool   `json:"warmup"`
}
type Event struct {
	Type         string `json:"type"`
	Batch        string `json:"batch"`
	ID           string `json:"id,omitempty"`
	Index        int    `json:"index,omitempty"`
	Warmup       bool   `json:"warmup,omitempty"`
	TimeNS       int64  `json:"time_ns"`
	DurationNS   int64  `json:"duration_ns,omitempty"`
	QueueNS      int64  `json:"queue_ns,omitempty"`
	Bytes        int    `json:"bytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Error        string `json:"error,omitempty"`
	PeakRSSBytes int64  `json:"peak_rss_bytes,omitempty"`
	CPUUserNS    int64  `json:"cpu_user_ns,omitempty"`
	CPUSystemNS  int64  `json:"cpu_system_ns,omitempty"`
	Version      string `json:"version,omitempty"`
	Success      int    `json:"success,omitempty"`
	Attempts     int    `json:"attempts,omitempty"`
}
