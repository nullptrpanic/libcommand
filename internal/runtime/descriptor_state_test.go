package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestDescriptorBudgetRetainsInputAndRestoration(t *testing.T) {
	s := newState(&Request{}, 4096)
	baseline, _ := stateMaterialization(s)
	s.setDescriptors(map[int]*descriptorTarget{3: {key: new(byte), input: newCertain([]byte(strings.Repeat("x", 1000)))}})
	withInput, _ := stateMaterialization(s)
	if withInput < baseline+1000 {
		t.Fatalf("descriptor input not charged: baseline=%d, actual=%d", baseline, withInput)
	}
	s.redirectionFrames = []*redirectionPlan{{saved: map[int]*descriptorTarget{4: {key: new(byte), input: newCertain([]byte(strings.Repeat("y", 1200)))}}}}
	s.redirectionBytes = descriptorMaterialization(s.redirectionFrames[0].saved, nil)
	withRestore, _ := stateMaterialization(s)
	if withRestore < withInput+1200 {
		t.Fatalf("saved descriptor not charged: before=%d, after=%d", withInput, withRestore)
	}
}

func TestRedirectedOutputBudgetIsAtomic(t *testing.T) {
	s := newState(&Request{}, 4096)
	e := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: 4096}}
	if err := s.fs.write("/out", []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	s.setDescriptors(map[int]*descriptorTarget{
		1: {key: new(byte), output: &outputTarget{file: "/out", id: 3}},
		2: {key: new(byte), output: &outputTarget{captureFD: 2, id: 2}},
	})
	if status := e.appendStreams(s, []byte("partial"), []byte(strings.Repeat("x", 4096)), false, false, unknownLocation); status != StatusIncomplete {
		t.Fatalf("status=%v", status)
	}
	data, unknown, _ := s.fs.readFile("/out")
	if string(data) != "old" || unknown || len(s.stderr.Value) != 0 {
		t.Fatalf("partially committed: file=%q, stderr=%q", data, s.stderr.Value)
	}
}
