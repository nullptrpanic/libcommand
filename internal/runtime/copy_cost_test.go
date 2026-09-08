package runtime

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
)

// Count bytes, not wall-clock time: these regressions protect against copying
// the complete output history or unrelated payloads on every small mutation.
func allocatedBytes(run func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestStreamAppendAllocationGrowth(t *testing.T) {
	e := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: 2 << 20}}
	s := newState(&Request{}, 2<<20)
	chunk := []byte(strings.Repeat("x", 1024))
	bytes := allocatedBytes(func() {
		for range 400 {
			// Ordinary statements retain rollback snapshots too, not just forks.
			snapshot := s.clone()
			if e.appendStreams(s, chunk, nil, false, false, unknownLocation) != StatusCompleted {
				t.Fatal(s.issue)
			}
			runtime.KeepAlive(snapshot)
		}
	})
	if len(s.stdout.Value) != 400*1024 || bytes > 20*400*1024 {
		t.Fatalf("output=%d bytes, allocated=%d; expected amortized growth", len(s.stdout.Value), bytes)
	}
}

func TestStreamAppendPreservesForksAndRollback(t *testing.T) {
	e := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: 4096}}
	s := newState(&Request{}, 4096)
	e.appendStreams(s, []byte("prefix"), []byte("error"), false, false, unknownLocation)
	snapshot := s.clone()
	left, right := s.clone(), s.clone()
	for _, test := range []*struct {
		state *State
		text  string
	}{{left, "left"}, {right, "right"}, {s, "parent"}, {snapshot, "restored"}} {
		if e.appendStreams(test.state, []byte(test.text), []byte(test.text), false, false, unknownLocation) != StatusCompleted {
			t.Fatal(test.state.issue)
		}
	}
	for _, test := range []*struct {
		state *State
		text  string
	}{{left, "left"}, {right, "right"}, {s, "parent"}, {snapshot, "restored"}} {
		if string(test.state.stdout.Value) != "prefix"+test.text || string(test.state.stderr.Value) != "error"+test.text {
			t.Fatalf("cross-path stream mutation: %q %q", test.state.stdout.Value, test.state.stderr.Value)
		}
	}
}

func TestCOWWriteDoesNotCopyUnrelatedPayloads(t *testing.T) {
	fs := newMemoryFS(2 << 20)
	if err := fs.write("/large", []byte(strings.Repeat("x", 512<<10)), false); err != nil {
		t.Fatal(err)
	}
	bytes := allocatedBytes(func() {
		for range 200 {
			child := fs.clone()
			if err := child.write("/small", []byte("ok"), false); err != nil {
				t.Fatal(err)
			}
		}
	})
	if bytes > 8<<20 {
		t.Fatalf("small file writes copied unrelated payloads: %d bytes", bytes)
	}
	vars := newVariables(nil)
	vars.put("large", expand.Variable{Set: true, Kind: expand.Indexed, List: make([]string, 32768)})
	bytes = allocatedBytes(func() {
		for range 200 {
			child := vars.clone()
			child.put("small", expand.Variable{Set: true, Kind: expand.String, Str: "ok"})
		}
	})
	if bytes > 8<<20 {
		t.Fatalf("small variable writes copied unrelated arrays: %d bytes", bytes)
	}
}

func TestMemoryFSAppendAfterForkDoesNotOverwriteSibling(t *testing.T) {
	fs := newMemoryFS(4096)
	if err := fs.write("/file", []byte("prefix"), false); err != nil {
		t.Fatal(err)
	}
	left, right := fs.clone(), fs.clone()
	for _, test := range []*struct {
		fs     *memoryFS
		suffix string
	}{{left, "L"}, {right, "R"}, {fs, "P"}} {
		if err := test.fs.write("/file", []byte(test.suffix), true); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []*struct {
		fs     *memoryFS
		suffix string
	}{{left, "L"}, {right, "R"}, {fs, "P"}} {
		got, _, _ := test.fs.readFile("/file")
		if string(got) != "prefix"+test.suffix {
			t.Fatalf("fork output = %q", got)
		}
	}
}
