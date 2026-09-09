//go:build linux

package e2

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func WorkerMain(t Transformer) {
	if err := worker(t); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func worker(t Transformer) error {
	var b Batch
	if err := json.NewDecoder(os.Stdin).Decode(&b); err != nil {
		return err
	}
	if !SafeName(b.ID) || (b.Concurrency != 1 && b.Concurrency != 4) {
		return fmt.Errorf("invalid batch")
	}
	if err := b.Spec.Validate(); err != nil {
		return err
	}
	c, _, err := LoadCorpus(b.Root)
	if err != nil {
		return err
	}
	var fixtures []Fixture
	data := map[string][]byte{}
	for _, f := range c.Fixtures {
		if b.Quality && !f.QualitySample {
			continue
		}
		fixtures = append(fixtures, f)
		v, e := os.ReadFile(filepath.Join(b.Root, f.Path))
		if e != nil {
			return e
		}
		data[f.ID] = v
	}
	rand.New(rand.NewSource(b.Seed)).Shuffle(len(fixtures), func(i, j int) { fixtures[i], fixtures[j] = fixtures[j], fixtures[i] })
	if b.Save {
		if err = os.MkdirAll(b.Output, 0755); err != nil {
			return err
		}
	}
	var mu sync.Mutex
	enc := json.NewEncoder(os.Stdout)
	emit := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		e.Batch = b.ID
		if err := enc.Encode(e); err != nil {
			panic(err)
		}
	}
	emit(Event{Type: "ready", TimeNS: time.Now().UnixNano(), Version: t.Version()})
	passes := []bool{false}
	if b.Warmup {
		passes = []bool{true, false}
	}
	var failed atomic.Bool
	for _, warm := range passes {
		dispatch := time.Now()
		emit(Event{Type: "pass_start", Warmup: warm, TimeNS: dispatch.UnixNano()})
		var wg sync.WaitGroup
		var success atomic.Int64
		jobs := make(chan int)
		for i := 0; i < b.Concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					f := fixtures[index]
					start := time.Now()
					emit(Event{Type: "start", ID: f.ID, Index: index, Warmup: warm, TimeNS: start.UnixNano()})
					// Start protocol emission is outside the transform-duration interval.
					begin := time.Now()
					out, e := t.Transform(data[f.ID], b.Spec)
					duration := time.Since(begin)
					end := time.Now()
					event := Event{Type: "finish", ID: f.ID, Index: index, Warmup: warm, TimeNS: end.UnixNano(), DurationNS: duration.Nanoseconds(), QueueNS: start.Sub(dispatch).Nanoseconds(), Bytes: len(out)}
					if e != nil {
						event.Error = e.Error()
						failed.Store(true)
					} else {
						event.SHA256 = Hash(out)
						success.Add(1)
					}
					// Persist measured outputs after the timestamp; batch wall time includes
					// result accounting/file writes when enabled, so performance runs disable it.
					if e == nil && b.Save && !warm {
						if e = os.WriteFile(filepath.Join(b.Output, f.ID+"."+b.Spec.Format), out, 0644); e != nil {
							returnFromWorker(e)
						}
					}
					emit(event)
				}
			}()
		}
		for i := range fixtures {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		emit(Event{Type: "pass_end", Warmup: warm, TimeNS: time.Now().UnixNano(), DurationNS: time.Since(dispatch).Nanoseconds(), Success: int(success.Load()), Attempts: len(fixtures)})
	}
	var usage syscall.Rusage
	if err = syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return err
	}
	emit(Event{Type: "summary", TimeNS: time.Now().UnixNano(), PeakRSSBytes: usage.Maxrss * 1024, CPUUserNS: usage.Utime.Nano(), CPUSystemNS: usage.Stime.Nano()})
	if failed.Load() {
		return fmt.Errorf("batch transform errors")
	}
	return nil
}
func returnFromWorker(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
