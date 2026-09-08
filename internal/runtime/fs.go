package runtime

import (
	"context"
	"errors"
	iofs "io/fs"
	"maps"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nullptrpanic/libcommand/internal/materialize"
)

var (
	errIsDirectory  = errors.New("is a directory")
	errNotDirectory = errors.New("not a directory")
)

type memoryFS struct {
	files             map[string][]byte
	dirs              map[string]struct{}
	unknownFiles      map[string]struct{}
	maximumBytes      int
	materializedBytes int
	shared            bool
}

func newMemoryFS(maximum int) *memoryFS {
	return &memoryFS{
		files:        make(map[string][]byte),
		dirs:         map[string]struct{}{`/`: {}},
		maximumBytes: normalizedMaxMemoryBytes(maximum),
	}
}

func (fs *memoryFS) clone() *memoryFS {
	fs.shared = true
	return &memoryFS{
		files:             fs.files,
		dirs:              fs.dirs,
		unknownFiles:      fs.unknownFiles,
		maximumBytes:      fs.maximumBytes,
		materializedBytes: fs.materializedBytes,
		shared:            true,
	}
}

func (fs *memoryFS) ensureMutable() {
	if !fs.shared {
		return
	}
	// File contents are immutable; mutation replaces only the target payload.
	fs.files = maps.Clone(fs.files)
	fs.dirs = maps.Clone(fs.dirs)
	fs.unknownFiles = maps.Clone(fs.unknownFiles)
	fs.shared = false
}

func (fs *memoryFS) resolve(dir, name string) string {
	if !path.IsAbs(name) {
		name = path.Join(dir, name)
	}
	return path.Clean(name)
}

func (fs *memoryFS) readValue(name string) ([]byte, bool) {
	contents, unknown, _ := fs.readFile(name)
	return contents, unknown
}

func (fs *memoryFS) readFile(name string) ([]byte, bool, bool) {
	name = path.Clean(name)
	if name == "/dev/null" {
		return nil, false, true
	}
	contents, exists := fs.files[name]
	_, unknown := fs.unknownFiles[name]
	return append([]byte(nil), contents...), unknown, exists
}

func (fs *memoryFS) pathKind(name string) PathKind {
	name = path.Clean(name)
	if name == "/dev/null" {
		return PathDevice
	}
	if _, exists := fs.dirs[name]; exists {
		return PathDirectory
	}
	if _, exists := fs.files[name]; exists {
		return PathFile
	}
	return PathMissing
}

func (fs *memoryFS) write(name string, contents []byte, appendMode bool) error {
	return fs.writeValue(name, contents, appendMode, false)
}

func (fs *memoryFS) writeValue(name string, contents []byte, appendMode, unknown bool) error {
	return fs.writeValueMode(name, contents, appendMode, unknown, false)
}

func (fs *memoryFS) writeWithParents(name string, contents []byte, appendMode bool) error {
	return fs.writeValueMode(name, contents, appendMode, false, true)
}

func (fs *memoryFS) writeValueMode(name string, contents []byte, appendMode, unknown, createParents bool) error {
	name = path.Clean(name)
	if name == "/dev/null" {
		return nil
	}
	if _, exists := fs.dirs[name]; exists {
		return &iofs.PathError{Op: "open", Path: name, Err: errIsDirectory}
	}
	var missing []string
	missingBytes := 0
	parent := path.Dir(name)
	if createParents {
		var err error
		missing, missingBytes, err = fs.missingDirectories(parent)
		if err != nil {
			return err
		}
	} else if _, exists := fs.dirs[parent]; !exists {
		return &iofs.PathError{Op: "open", Path: name, Err: iofs.ErrNotExist}
	}
	resultBytes, ok := materialize.Add(fs.materializedBytes, missingBytes, fs.maximumBytes)
	if !ok {
		return materialize.LimitError(fs.maximumBytes)
	}
	previous, exists := fs.files[name]
	previousUnknown := fs.fileUnknown(name)
	if exists {
		if !appendMode {
			resultBytes -= len(previous)
		}
	} else {
		for _, size := range []int{materialize.EntryBytes, len(name)} {
			resultBytes, ok = materialize.Add(resultBytes, size, fs.maximumBytes)
			if !ok {
				return materialize.LimitError(fs.maximumBytes)
			}
		}
	}
	resultBytes, ok = materialize.Add(resultBytes, len(contents), fs.maximumBytes)
	if !ok {
		return materialize.LimitError(fs.maximumBytes)
	}
	resultUnknown := unknown || appendMode && previousUnknown
	if resultUnknown != previousUnknown {
		unknownBytes := materialize.EntryBytes + len(name)
		if resultUnknown {
			resultBytes, ok = materialize.Add(resultBytes, unknownBytes, fs.maximumBytes)
			if !ok {
				return materialize.LimitError(fs.maximumBytes)
			}
		} else {
			resultBytes -= unknownBytes
		}
	}

	fs.ensureMutable()
	for _, directory := range missing {
		fs.dirs[directory] = struct{}{}
	}
	if appendMode {
		fs.files[name] = append(previous[:len(previous):len(previous)], contents...)
	} else {
		fs.files[name] = append([]byte(nil), contents...)
	}
	if resultUnknown {
		if fs.unknownFiles == nil {
			fs.unknownFiles = make(map[string]struct{})
		}
		fs.unknownFiles[name] = struct{}{}
	} else {
		delete(fs.unknownFiles, name)
	}
	fs.materializedBytes = resultBytes
	return nil
}

func (fs *memoryFS) fileUnknown(name string) bool {
	_, unknown := fs.unknownFiles[name]
	return unknown
}

func (fs *memoryFS) ensureDir(name string) error {
	missing, missingBytes, err := fs.missingDirectories(name)
	if err != nil {
		return err
	}
	resultBytes, ok := materialize.Add(fs.materializedBytes, missingBytes, fs.maximumBytes)
	if !ok {
		return materialize.LimitError(fs.maximumBytes)
	}
	fs.ensureMutable()
	for _, directory := range missing {
		fs.dirs[directory] = struct{}{}
	}
	fs.materializedBytes = resultBytes
	return nil
}

func (fs *memoryFS) remove(ctx context.Context, name string, recursive bool) error {
	name = path.Clean(name)
	switch fs.pathKind(name) {
	case PathMissing:
		return nil
	case PathDevice:
		return nil
	case PathFile:
		if err := ctx.Err(); err != nil {
			return err
		}
		removedBytes := materialize.EntryBytes + len(name) + len(fs.files[name])
		if fs.fileUnknown(name) {
			removedBytes += materialize.EntryBytes + len(name)
		}
		fs.ensureMutable()
		delete(fs.files, name)
		delete(fs.unknownFiles, name)
		fs.materializedBytes -= removedBytes
		return nil
	case PathDirectory:
		if !recursive {
			return &iofs.PathError{Op: "remove", Path: name, Err: errIsDirectory}
		}
	}

	files := make([]string, 0)
	directories := make([]string, 0)
	removedBytes := 0
	iteration := 0
	for filename, contents := range fs.files {
		if iteration%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		iteration++
		if !pathWithin(filename, name) {
			continue
		}
		files = append(files, filename)
		removedBytes += materialize.EntryBytes + len(filename) + len(contents)
		if fs.fileUnknown(filename) {
			removedBytes += materialize.EntryBytes + len(filename)
		}
	}
	for directory := range fs.dirs {
		if iteration%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		iteration++
		if directory == "/" || !pathWithin(directory, name) {
			continue
		}
		directories = append(directories, directory)
		removedBytes += materialize.EntryBytes + len(directory)
	}

	fs.ensureMutable()
	for _, filename := range files {
		delete(fs.files, filename)
		delete(fs.unknownFiles, filename)
	}
	for _, directory := range directories {
		delete(fs.dirs, directory)
	}
	fs.materializedBytes -= removedBytes
	return nil
}

func pathWithin(name, root string) bool {
	return root == "/" || name == root || strings.HasPrefix(name, root+"/")
}

func (fs *memoryFS) missingDirectories(name string) ([]string, int, error) {
	name = path.Clean(name)
	missing := make([]string, 0)
	materializedBytes := 0
	for {
		if name == "/dev/null" {
			return nil, 0, &iofs.PathError{Op: "mkdir", Path: name, Err: errNotDirectory}
		}
		if _, exists := fs.dirs[name]; exists {
			break
		}
		if _, exists := fs.files[name]; exists {
			return nil, 0, &iofs.PathError{Op: "mkdir", Path: name, Err: errNotDirectory}
		}
		var ok bool
		for _, size := range []int{materialize.EntryBytes, len(name)} {
			materializedBytes, ok = materialize.Add(materializedBytes, size, fs.maximumBytes-fs.materializedBytes)
			if !ok {
				return nil, 0, materialize.LimitError(fs.maximumBytes)
			}
		}
		missing = append(missing, name)
		if name == "/" {
			break
		}
		name = path.Dir(name)
	}
	return missing, materializedBytes, nil
}

func (fs *memoryFS) readDir(ctx context.Context, name string) ([]iofs.DirEntry, error) {
	name = path.Clean(name)
	if _, exists := fs.dirs[name]; !exists {
		return nil, &iofs.PathError{Op: "readdir", Path: name, Err: iofs.ErrNotExist}
	}
	entries := make(map[string]bool)
	iteration := 0
	for filename := range fs.files {
		if iteration%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		iteration++
		if path.Dir(filename) == name {
			entries[path.Base(filename)] = false
		}
	}
	for dirname := range fs.dirs {
		if iteration%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		iteration++
		if dirname != name && path.Dir(dirname) == name {
			entries[path.Base(dirname)] = true
		}
	}
	names := make([]string, 0, len(entries))
	for entry := range entries {
		names = append(names, entry)
	}
	sort.Strings(names)
	result := make([]iofs.DirEntry, 0, len(names))
	for _, entry := range names {
		result = append(result, &memoryDirEntry{name: entry, dir: entries[entry]})
	}
	return result, nil
}

type memoryDirEntry struct {
	name string
	dir  bool
}

func (entry *memoryDirEntry) Name() string                 { return entry.name }
func (entry *memoryDirEntry) IsDir() bool                  { return entry.dir }
func (entry *memoryDirEntry) Type() iofs.FileMode          { return entry.info().Mode() }
func (entry *memoryDirEntry) Info() (iofs.FileInfo, error) { return entry.info(), nil }
func (entry *memoryDirEntry) info() *memoryFileInfo {
	info := memoryFileInfo(*entry)
	return &info
}

type memoryFileInfo memoryDirEntry

func (info *memoryFileInfo) Name() string { return info.name }
func (*memoryFileInfo) Size() int64       { return 0 }
func (info *memoryFileInfo) Mode() iofs.FileMode {
	if info.dir {
		return iofs.ModeDir | 0o755
	}
	return 0o644
}
func (*memoryFileInfo) ModTime() time.Time { return time.Time{} }
func (info *memoryFileInfo) IsDir() bool   { return info.dir }
func (*memoryFileInfo) Sys() any           { return nil }
