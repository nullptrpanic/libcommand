package runtime

import (
	"context"
	"errors"
	iofs "io/fs"
	"testing"
)

func TestMemoryFSPathAndDirectorySemantics(t *testing.T) {
	fs := newMemoryFS(defaultMaxMemoryBytes)
	if got := fs.resolve("/work", "relative/file"); got != "/work/relative/file" {
		t.Fatalf("resolved relative path = %q", got)
	}
	if got := fs.resolve("/work", "/absolute"); got != "/absolute" {
		t.Fatalf("resolved absolute path = %q", got)
	}
	if contents, unknown, exists := fs.readFile("/dev/null"); len(contents) != 0 || unknown || !exists {
		t.Fatalf("/dev/null = %q, %v, %v", contents, unknown, exists)
	}
	if _, _, exists := fs.readFile("/missing"); exists {
		t.Fatal("missing file exists")
	}
	if err := fs.ensureDir("/tree/nested"); err != nil {
		t.Fatal(err)
	}
	if err := fs.write("/tree/nested/file", []byte("value"), false); err != nil {
		t.Fatal(err)
	}
	if err := fs.write("/tree", nil, false); !errors.Is(err, errIsDirectory) {
		t.Fatalf("write directory error = %v", err)
	}
	if err := fs.write("/missing/file", nil, false); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("write missing parent error = %v", err)
	}
	if err := fs.ensureDir("/tree/nested/file/child"); !errors.Is(err, errNotDirectory) {
		t.Fatalf("mkdir below file error = %v", err)
	}
	if !pathWithin("/tree/nested/file", "/tree") || pathWithin("/other", "/tree") || !pathWithin("/anything", "/") {
		t.Fatal("pathWithin() returned an invalid containment result")
	}
}

func TestMemoryFSRemovalVariants(t *testing.T) {
	fs := newMemoryFS(defaultMaxMemoryBytes)
	if err := fs.remove(context.Background(), "/missing", false); err != nil {
		t.Fatalf("remove missing: %v", err)
	}
	if err := fs.remove(context.Background(), "/dev/null", true); err != nil {
		t.Fatalf("remove device: %v", err)
	}
	if err := fs.writeWithParents("/tree/file", []byte("value"), false); err != nil {
		t.Fatal(err)
	}
	if err := fs.remove(context.Background(), "/tree", false); !errors.Is(err, errIsDirectory) {
		t.Fatalf("non-recursive directory removal = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fs.remove(cancelled, "/tree/file", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled file removal = %v", err)
	}
	if fs.pathKind("/tree/file") != PathFile {
		t.Fatal("cancelled removal changed the file")
	}
	if err := fs.remove(context.Background(), "/tree/file", false); err != nil {
		t.Fatal(err)
	}
	if fs.pathKind("/tree/file") != PathMissing {
		t.Fatal("file remains after removal")
	}
}

func TestMemoryFSReadDirEntries(t *testing.T) {
	fs := newMemoryFS(defaultMaxMemoryBytes)
	if err := fs.writeWithParents("/root/z-file", []byte("z"), false); err != nil {
		t.Fatal(err)
	}
	if err := fs.writeWithParents("/root/a-dir/file", []byte("a"), false); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.readDir(context.Background(), "/root")
	if err != nil || len(entries) != 2 || entries[0].Name() != "a-dir" || entries[1].Name() != "z-file" {
		t.Fatalf("readDir() = %#v, %v", entries, err)
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || info.Name() != entry.Name() || info.IsDir() != entry.IsDir() || info.Size() != 0 || info.ModTime().IsZero() == false || info.Sys() != nil {
			t.Fatalf("entry %q info = %#v, %v", entry.Name(), info, infoErr)
		}
		if entry.IsDir() != entry.Type().IsDir() {
			t.Fatalf("entry %q type mismatch", entry.Name())
		}
	}
	if _, err := fs.readDir(context.Background(), "/missing"); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("read missing directory = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fs.readDir(cancelled, "/root"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled readDir = %v", err)
	}
}
