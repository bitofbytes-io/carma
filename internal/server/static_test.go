package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"testing/fstest"
)

func TestStaticAssetsVersionURLsAndRequireRevalidation(t *testing.T) {
	files := fstest.MapFS{"styles.css": {Data: []byte(".hero-actions>*{text-align:center}")}}
	static, err := newStaticAssets(files)
	if err != nil {
		t.Fatal(err)
	}
	link := static.URL("styles.css")
	if !regexp.MustCompile(`^/static/styles\.css\?v=[0-9a-f]{12}$`).MatchString(link) {
		t.Fatalf("styles.css URL = %q, want a content-hash version", link)
	}
	if got := static.URL("missing.css"); got != "/static/missing.css" {
		t.Fatalf("missing file URL = %q", got)
	}

	response := httptest.NewRecorder()
	static.ServeHTTP(response, httptest.NewRequest("GET", link, nil))
	if response.Code != http.StatusOK || response.Body.String() != ".hero-actions>*{text-align:center}" {
		t.Fatalf("GET %s = %d %q", link, response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	etag := response.Header().Get("ETag")
	if etag != `"`+link[len("/static/styles.css?v="):]+`"` {
		t.Fatalf("ETag = %q, want the URL's version", etag)
	}

	revalidate := httptest.NewRequest("GET", "/static/styles.css", nil)
	revalidate.Header.Set("If-None-Match", etag)
	response = httptest.NewRecorder()
	static.ServeHTTP(response, revalidate)
	if response.Code != http.StatusNotModified {
		t.Fatalf("revalidation = %d, want 304", response.Code)
	}

	changed, err := newStaticAssets(fstest.MapFS{"styles.css": {Data: []byte(".hero-actions{gap:12px}")}})
	if err != nil {
		t.Fatal(err)
	}
	if changed.URL("styles.css") == link {
		t.Fatal("changed stylesheet kept the same URL")
	}
}

func TestStaticAssetsWithoutDirectoryLeaveURLsUnversioned(t *testing.T) {
	static, err := newStaticAssets(os.DirFS(filepath.Join(t.TempDir(), "missing")))
	if err != nil {
		t.Fatal(err)
	}
	if got := static.URL("styles.css"); got != "/static/styles.css" {
		t.Fatalf("URL = %q", got)
	}
}
