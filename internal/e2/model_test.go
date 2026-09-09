package e2

import "testing"

func TestGeometry(t *testing.T) {
	for _, tc := range []struct {
		w, h                 int
		op                   string
		rw, rh, x, y, ow, oh int
	}{{4096, 2713, "resize", 640, 424, 0, 0, 640, 424}, {1024, 768, "contain", 640, 480, 0, 0, 640, 480}, {4096, 2713, "cover", 966, 640, 163, 0, 640, 640}, {768, 1024, "cover", 640, 853, 0, 106, 640, 640}} {
		rw, rh, x, y, ow, oh := Geometry(tc.w, tc.h, tc.op)
		if rw != tc.rw || rh != tc.rh || x != tc.x || y != tc.y || ow != tc.ow || oh != tc.oh {
			t.Fatalf("%+v: got %d,%d,%d,%d,%d,%d", tc, rw, rh, x, y, ow, oh)
		}
	}
}
func TestInputPolicy(t *testing.T) {
	for _, v := range [][]byte{nil, []byte("hello"), make([]byte, MaxInputBytes+1)} {
		if InputBytes(v) == nil {
			t.Fatal("invalid bytes accepted")
		}
	}
	for _, p := range [][2]int{{0, 5}, {100000, 100000}, {8193, 1}, {5000, 5000}} {
		if Dimensions(p[0], p[1]) == nil {
			t.Fatal("invalid dimensions accepted")
		}
	}
	if Dimensions(4096, 3072) != nil {
		t.Fatal("corpus rejected")
	}
}
