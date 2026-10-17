package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"testing/fstest"
	"time"
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

func TestStaticAssetsServeFilesEditedAfterStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "styles.css")
	if err := os.WriteFile(path, []byte(".hero-actions>*{text-align:start}"), 0o644); err != nil {
		t.Fatal(err)
	}
	static, err := newStaticAssets(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	oldLink := static.URL("styles.css")
	response := httptest.NewRecorder()
	static.ServeHTTP(response, httptest.NewRequest("GET", oldLink, nil))
	oldETag := response.Header().Get("ETag")

	if err = os.WriteFile(path, []byte(".hero-actions>*{text-align:center}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Filesystems with coarse timestamps may not move the mtime of a
	// same-second edit, so move it explicitly.
	later := time.Now().Add(time.Minute)
	if err = os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	revalidate := httptest.NewRequest("GET", oldLink, nil)
	revalidate.Header.Set("If-None-Match", oldETag)
	response = httptest.NewRecorder()
	static.ServeHTTP(response, revalidate)
	if response.Code != http.StatusOK || response.Body.String() != ".hero-actions>*{text-align:center}" {
		t.Fatalf("revalidating an edited file = %d %q, want 200 with the new content", response.Code, response.Body.String())
	}
	newETag := response.Header().Get("ETag")
	if newETag == "" || newETag == oldETag {
		t.Fatalf("edited file ETag = %q, was %q", newETag, oldETag)
	}
	if newLink := static.URL("styles.css"); newLink == oldLink || newETag != `"`+newLink[len("/static/styles.css?v="):]+`"` {
		t.Fatalf("edited file URL = %q (was %q), ETag %q", newLink, oldLink, newETag)
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
