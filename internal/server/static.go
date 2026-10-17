package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"strings"
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
	files    http.Handler
	versions map[string]string
}

// newStaticAssets hashes every file in fsys once. A missing directory serves
// nothing and leaves URLs unversioned.
func newStaticAssets(fsys fs.FS) (*staticAssets, error) {
	versions := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		versions[name] = hex.EncodeToString(sum[:6])
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return &staticAssets{files: http.StripPrefix("/static/", http.FileServer(http.FS(fsys))), versions: versions}, nil
}

// URL returns the path to a static file, such as "/static/styles.css?v=1a2b3c4d5e6f".
func (a *staticAssets) URL(name string) string {
	if version := a.versions[name]; version != "" {
		return "/static/" + name + "?v=" + version
	}
	return "/static/" + name
}

func (a *staticAssets) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-cache")
	if version := a.versions[strings.TrimPrefix(request.URL.Path, "/static/")]; version != "" {
		response.Header().Set("ETag", `"`+version+`"`)
	}
	a.files.ServeHTTP(response, request)
}
