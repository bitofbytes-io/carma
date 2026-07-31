package assets

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

type Object struct {
	Key, ContentType string
	Size             int64
	Checksum         string
}
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}
type Store interface {
	Save(context.Context, io.Reader, int64) (Object, error)
	Open(context.Context, string) (ReadSeekCloser, error)
	Delete(context.Context, string) error
}
type LocalStore struct{ root string }

var keyPattern = regexp.MustCompile(`^[0-9a-f]{2}/[0-9a-f-]{36}\.(pdf|jpg|png|webp|heic)$`)
var ErrTooLarge = errors.New("file exceeds upload limit")
var ErrUnsupported = errors.New("unsupported file type")

func NewLocalStore(root string) (*LocalStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("asset root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"objects", "temporary"} {
		if err := os.MkdirAll(filepath.Join(absolute, d), 0750); err != nil {
			return nil, fmt.Errorf("create asset directory: %w", err)
		}
	}
	return &LocalStore{root: absolute}, nil
}

func (s *LocalStore) Save(_ context.Context, source io.Reader, max int64) (Object, error) {
	if max <= 0 {
		return Object{}, fmt.Errorf("invalid upload limit")
	}
	br := bufio.NewReader(io.LimitReader(source, max+1))
	header, err := br.Peek(16)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return Object{}, err
	}
	mime, ext, ok := detect(header)
	if !ok {
		return Object{}, ErrUnsupported
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "temporary"), "upload-*")
	if err != nil {
		return Object{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), br)
	if syncErr := tmp.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	closeErr := tmp.Close()
	if copyErr != nil {
		return Object{}, copyErr
	}
	if closeErr != nil {
		return Object{}, closeErr
	}
	if n > max {
		return Object{}, ErrTooLarge
	}
	id := uuid.NewString()
	key := id[:2] + "/" + id + "." + ext
	dest := filepath.Join(s.root, "objects", filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
		return Object{}, err
	}
	if err := os.Rename(name, dest); err != nil {
		return Object{}, err
	}
	return Object{Key: key, ContentType: mime, Size: n, Checksum: hex.EncodeToString(h.Sum(nil))}, nil
}
func detect(b []byte) (string, string, bool) {
	if len(b) >= 5 && string(b[:5]) == "%PDF-" {
		return "application/pdf", "pdf", true
	}
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return "image/jpeg", "jpg", true
	}
	if len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png", "png", true
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp", "webp", true
	}
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		brand := string(b[8:12])
		if brand == "heic" || brand == "heix" || brand == "hevc" || brand == "hevx" || brand == "mif1" {
			return "image/heic", "heic", true
		}
	}
	return "", "", false
}
func (s *LocalStore) Open(_ context.Context, key string) (ReadSeekCloser, error) {
	p, e := s.path(key)
	if e != nil {
		return nil, e
	}
	return os.Open(p)
}
func (s *LocalStore) Delete(_ context.Context, key string) error {
	p, e := s.path(key)
	if e != nil {
		return e
	}
	e = os.Remove(p)
	if os.IsNotExist(e) {
		return nil
	}
	return e
}
func (s *LocalStore) path(key string) (string, error) {
	if !keyPattern.MatchString(key) {
		return "", fmt.Errorf("invalid storage key")
	}
	p := filepath.Join(s.root, "objects", filepath.FromSlash(key))
	clean := filepath.Clean(p)
	base := filepath.Join(s.root, "objects") + string(os.PathSeparator)
	if !strings.HasPrefix(clean, base) {
		return "", fmt.Errorf("invalid storage key")
	}
	return clean, nil
}
