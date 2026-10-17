package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
)

// staticDir is the directory served under /static/, relative to the working
// directory.
const staticDir = "static"

// staticAssets serves /static/* so that a deploy's changes reach browsers that
// already hold an older copy. Pages link each file with its content hash in
// the URL, which skips anything cached under the old URL, and every response
// must be revalidated against an ETag of that hash rather than cached
// heuristically from Last-Modified.
type staticAssets struct {
	fsys  fs.FS
	files http.Handler

	mu       sync.Mutex
	versions map[string]staticVersion
}

// staticVersion is a file's content hash and the size and modification time
// it had when hashed.
type staticVersion struct {
	hash    string
	size    int64
	modTime time.Time
}

// newStaticAssets hashes every file in fsys. A missing directory serves
// nothing and leaves URLs unversioned.
func newStaticAssets(fsys fs.FS) (*staticAssets, error) {
	assets := &staticAssets{fsys: fsys, files: http.StripPrefix("/static/", http.FileServer(http.FS(fsys))), versions: map[string]staticVersion{}}
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		_, err = assets.version(name)
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return assets, nil
}

// version returns the content hash of the regular file name, rehashing it if
// its size or modification time changed since it was last hashed, such as
// when a stylesheet is edited under `make run`. It returns "" for anything
// else.
func (a *staticAssets) version(name string) (string, error) {
	info, err := fs.Stat(a.fsys, name)
	if err != nil || !info.Mode().IsRegular() {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if known, ok := a.versions[name]; ok && known.size == info.Size() && known.modTime.Equal(info.ModTime()) {
		return known.hash, nil
	}
	content, err := fs.ReadFile(a.fsys, name)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	current := staticVersion{hash: hex.EncodeToString(sum[:6]), size: info.Size(), modTime: info.ModTime()}
	a.versions[name] = current
	return current.hash, nil
}

// URL returns the path to a static file, such as "/static/styles.css?v=1a2b3c4d5e6f".
func (a *staticAssets) URL(name string) string {
	if version, _ := a.version(name); version != "" {
		return "/static/" + name + "?v=" + version
	}
	return "/static/" + name
}

func (a *staticAssets) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-cache")
	if version, _ := a.version(strings.TrimPrefix(request.URL.Path, "/static/")); version != "" {
		response.Header().Set("ETag", `"`+version+`"`)
	}
	a.files.ServeHTTP(response, request)
}
