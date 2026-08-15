package runtime

import (
	"strings"
	"testing"
)

func TestMemoryFSRejectsMaterializationWithoutMutation(t *testing.T) {
	const maximumBytes = 25
	fs := newMemoryFS(maximumBytes)
	if err := fs.write("/file", []byte("1234"), false); err != nil {
		t.Fatal(err)
	}
	if err := fs.write("/file", []byte("5"), true); err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 25 reached") {
		t.Fatalf("append error = %v", err)
	}
	if contents, _ := fs.readValue("/file"); string(contents) != "1234" {
		t.Fatalf("file changed after append failure: %q", contents)
	}
	if err := fs.write("/file", []byte("12345"), false); err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 25 reached") {
		t.Fatalf("replace error = %v", err)
	}
	if contents, _ := fs.readValue("/file"); string(contents) != "1234" {
		t.Fatalf("file changed after replace failure: %q", contents)
	}
}

func TestMemoryFSCertaintyCopyOnWrite(t *testing.T) {
	fs := newMemoryFS(defaultMaxMemoryBytes)
	if err := fs.writeValue("/file", []byte("representative"), false, true); err != nil {
		t.Fatal(err)
	}
	if contents, unknown := fs.readValue("/file"); string(contents) != "representative" || !unknown {
		t.Fatalf("unknown write = %q, %t", contents, unknown)
	}

	clone := fs.clone()
	if err := clone.write("/file", []byte("known"), false); err != nil {
		t.Fatal(err)
	}
	if contents, unknown := clone.readValue("/file"); string(contents) != "known" || unknown {
		t.Fatalf("known replacement = %q, %t", contents, unknown)
	}
	if contents, unknown := fs.readValue("/file"); string(contents) != "representative" || !unknown {
		t.Fatalf("source after clone write = %q, %t", contents, unknown)
	}
}

func TestMemoryFSAppendCertainty(t *testing.T) {
	for _, test := range []struct {
		name            string
		initialUnknown  bool
		additionUnknown bool
	}{
		{"known then unknown", false, true},
		{"unknown then known", true, false},
		{"unknown then unknown", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fs := newMemoryFS(defaultMaxMemoryBytes)
			if err := fs.writeValue("/file", []byte("a"), false, test.initialUnknown); err != nil {
				t.Fatal(err)
			}
			if err := fs.writeValue("/file", []byte("b"), true, test.additionUnknown); err != nil {
				t.Fatal(err)
			}
			if contents, unknown := fs.readValue("/file"); string(contents) != "ab" || !unknown {
				t.Fatalf("append = %q, %t", contents, unknown)
			}
		})
	}

	fs := newMemoryFS(defaultMaxMemoryBytes)
	if err := fs.writeValue("/dev/../dev/null", []byte("ignored"), false, true); err != nil {
		t.Fatal(err)
	}
	if contents, unknown := fs.readValue("/dev/null"); len(contents) != 0 || unknown {
		t.Fatalf("/dev/null = %q, %t", contents, unknown)
	}
	if kind := fs.pathKind("/dev/../dev/null"); kind != PathDevice {
		t.Fatalf("/dev/null kind = %d, want PathDevice", kind)
	}
	if err := fs.ensureDir("/dev/null"); err == nil {
		t.Fatal("creating a directory over /dev/null succeeded")
	}
}

func TestMemoryFSRejectsAggregateFilesWithoutMutation(t *testing.T) {
	fs := newMemoryFS(64)
	for _, name := range []string{"/a", "/b"} {
		if err := fs.write(name, []byte("1234567890"), false); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := fs.write("/c", []byte("1234567890"), false); err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
		t.Fatalf("aggregate write error = %v", err)
	}
	if _, _, exists := fs.readFile("/c"); exists {
		t.Fatal("failed aggregate write created /c")
	}
	for _, name := range []string{"/a", "/b"} {
		if contents, _, exists := fs.readFile(name); !exists || string(contents) != "1234567890" {
			t.Fatalf("preserved file %s = %q, %t", name, contents, exists)
		}
	}
}

func TestMemoryFSRejectsAggregateDirectoriesWithoutMutation(t *testing.T) {
	fs := newMemoryFS(64)
	err := fs.ensureDir("/12345678901234567890/12345678901234567890")
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
		t.Fatalf("ensureDir() error = %v", err)
	}
	if len(fs.dirs) != 1 {
		t.Fatalf("directories changed after failure: %#v", fs.dirs)
	}
}
