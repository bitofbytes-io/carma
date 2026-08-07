package assets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalStoreDetectsBytesAndRanges(t *testing.T) {
	s, e := NewLocalStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	o, e := s.Save(context.Background(), bytes.NewBufferString("%PDF-1.7\nfixture"), 100)
	if e != nil {
		t.Fatal(e)
	}
	if o.ContentType != "application/pdf" || o.Key[len(o.Key)-4:] != ".pdf" {
		t.Fatalf("bad object: %+v", o)
	}
	f, e := s.Open(context.Background(), o.Key)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if _, e = f.Seek(5, io.SeekStart); e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(f)
	if string(b) != "1.7\nfixture" {
		t.Fatalf("got %q", b)
	}
}

func writeCleanupFile(t *testing.T, path, contents string, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestLocalStorePruneClassifiesAndRemovesOnlyEligibleFiles(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-48 * time.Hour)
	referencedKey := "aa/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.pdf"
	freshKey := "bb/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.jpg"
	orphanKey := "cc/cccccccc-cccc-4ccc-8ccc-cccccccccccc.png"
	writeCleanupFile(t, filepath.Join(s.root, "objects", filepath.FromSlash(referencedKey)), "reference", cutoff.Add(-time.Hour))
	writeCleanupFile(t, filepath.Join(s.root, "objects", filepath.FromSlash(freshKey)), "fresh", cutoff)
	writeCleanupFile(t, filepath.Join(s.root, "objects", filepath.FromSlash(orphanKey)), "orphan-bytes", cutoff.Add(-time.Second))
	unknownPath := filepath.Join(s.root, "objects", "do-not-delete.txt")
	writeCleanupFile(t, unknownPath, "unknown", cutoff.Add(-time.Hour))
	temporaryPath := filepath.Join(s.root, "temporary", "upload-stale")
	writeCleanupFile(t, temporaryPath, "temporary", cutoff.Add(-time.Hour))
	freshTemporaryPath := filepath.Join(s.root, "temporary", "upload-fresh")
	writeCleanupFile(t, freshTemporaryPath, "new", cutoff.Add(time.Hour))
	unknownTemporaryPath := filepath.Join(s.root, "temporary", "operator-note")
	writeCleanupFile(t, unknownTemporaryPath, "leave me", cutoff.Add(-time.Hour))
	symlinkPath := filepath.Join(s.root, "objects", "symlink")
	if err := os.Symlink(filepath.Join(s.root, "objects", filepath.FromSlash(orphanKey)), symlinkPath); err != nil {
		t.Fatal(err)
	}

	report, err := s.Prune(t.Context(), map[string]struct{}{referencedKey: {}}, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if report.ObjectFilesScanned != 3 || report.TemporaryFilesScanned != 2 || report.ReferencedFilesRetained != 1 || report.FreshUnreferencedFilesRetained != 2 {
		t.Fatalf("scan report = %+v", report)
	}
	if report.OrphanObjectsPruned != 1 || report.StaleTemporaryFilesPruned != 1 || report.UnknownFilesSkipped != 2 || report.SymlinksSkipped != 1 || report.DeletionFailures != 0 {
		t.Fatalf("cleanup report = %+v", report)
	}
	if want := int64(len("orphan-bytes") + len("temporary")); report.BytesReclaimed != want {
		t.Fatalf("bytes reclaimed = %d, want %d", report.BytesReclaimed, want)
	}
	for _, path := range []string{filepath.Join(s.root, "objects", filepath.FromSlash(orphanKey)), temporaryPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pruned path %q still exists: %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(s.root, "objects", filepath.FromSlash(referencedKey)), filepath.Join(s.root, "objects", filepath.FromSlash(freshKey)), unknownPath, freshTemporaryPath, unknownTemporaryPath, symlinkPath} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("retained path %q: %v", path, err)
		}
	}
}

func TestLocalStorePruneContinuesAfterDeletionFailure(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-48 * time.Hour)
	failingKey := "dd/dddddddd-dddd-4ddd-8ddd-dddddddddddd.webp"
	deletedKey := "ee/eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee.heic"
	failingPath := filepath.Join(s.root, "objects", filepath.FromSlash(failingKey))
	deletedPath := filepath.Join(s.root, "objects", filepath.FromSlash(deletedKey))
	writeCleanupFile(t, failingPath, "failure", cutoff.Add(-time.Hour))
	writeCleanupFile(t, deletedPath, "success", cutoff.Add(-time.Hour))
	s.remove = func(path string) error {
		if path == failingPath {
			return errors.New("injected NFS failure")
		}
		return os.Remove(path)
	}

	report, err := s.Prune(t.Context(), nil, cutoff)
	if err == nil || !strings.Contains(err.Error(), "injected NFS failure") {
		t.Fatalf("prune error = %v", err)
	}
	if report.DeletionFailures != 1 || report.OrphanObjectsPruned != 1 || report.BytesReclaimed != int64(len("success")) || len(report.Failures) != 1 {
		t.Fatalf("partial report = %+v", report)
	}
	if report.Failures[0].Category != "object" || report.Failures[0].Key != failingKey {
		t.Fatalf("failure detail = %+v", report.Failures[0])
	}
	if _, err := os.Stat(failingPath); err != nil {
		t.Fatalf("failed object was removed: %v", err)
	}
	if _, err := os.Stat(deletedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("later object was not removed: %v", err)
	}
}

func TestLocalStorePruneCountsUnknownTemporaryDirectory(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unknownDirectory := filepath.Join(s.root, "temporary", "operator-files")
	if err := os.Mkdir(unknownDirectory, 0750); err != nil {
		t.Fatal(err)
	}

	report, err := s.Prune(t.Context(), nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if report.UnknownFilesSkipped != 1 {
		t.Fatalf("unknown files skipped = %d, want 1", report.UnknownFilesSkipped)
	}
	if info, err := os.Stat(unknownDirectory); err != nil || !info.IsDir() {
		t.Fatalf("unknown temporary directory was not retained: info=%v err=%v", info, err)
	}
}

func TestLocalStorePruneHonorsCancellation(t *testing.T) {
	s, _ := NewLocalStore(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := s.Prune(ctx, nil, time.Now())
	if !errors.Is(err, context.Canceled) || report.OrphanObjectsPruned != 0 {
		t.Fatalf("report=%+v error=%v", report, err)
	}
}
func TestLocalStoreRejectsSpoofAndLimit(t *testing.T) {
	s, _ := NewLocalStore(t.TempDir())
	if _, e := s.Save(context.Background(), bytes.NewBufferString("not a pdf"), 100); !errors.Is(e, ErrUnsupported) {
		t.Fatalf("got %v", e)
	}
	if _, e := s.Save(context.Background(), bytes.NewBufferString("%PDF-123456"), 6); !errors.Is(e, ErrTooLarge) {
		t.Fatalf("got %v", e)
	}
}
func TestLocalStoreRejectsUnsafeKey(t *testing.T) {
	s, _ := NewLocalStore(t.TempDir())
	if _, e := s.Open(context.Background(), "../../etc/passwd"); e == nil {
		t.Fatal("unsafe key accepted")
	}
}
