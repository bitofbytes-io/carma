package assets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
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
