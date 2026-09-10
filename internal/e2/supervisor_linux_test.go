//go:build linux

package e2

import "testing"

func TestMatrixContract(t *testing.T) {
	for _, test := range []struct {
		mode                    string
		batches, measured, warm int
	}{{"validate", 24, 576, 0}, {"quality", 24, 192, 0}, {"calibrate", 48, 1152, 1152}, {"measure", 240, 5760, 5760}, {"diagnose", 2, 16, 0}} {
		jobs, err := Matrix(test.mode, "corpus", "output")
		if err != nil {
			t.Fatal(err)
		}
		measured, warm := 0, 0
		for i, j := range jobs {
			n := 24
			if j.Batch.Quality {
				n = 8
			}
			measured += n
			if j.Batch.Warmup {
				warm += n
			}
			if i%2 == 1 && j.Batch.Seed != jobs[i-1].Batch.Seed {
				t.Fatal("unpaired seed")
			}
			if test.mode == "measure" && j.Batch.Save {
				t.Fatal("performance output IO")
			}
		}
		if len(jobs) != test.batches || measured != test.measured || warm != test.warm {
			t.Fatalf("%s: %d/%d/%d", test.mode, len(jobs), measured, warm)
		}
	}
}
