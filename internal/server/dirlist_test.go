package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"golang.org/x/net/webdav"
)

func TestDirList_RedirectAddsTrailingSlash(t *testing.T) {
	fs := webdav.NewMemFS()
	for _, dir := range []string{"/files", "/files/sub dir", "/evil.com"} {
		if err := fs.Mkdir(context.Background(), dir, os.ModePerm); err != nil {
			t.Fatal(err)
		}
	}
	h := &dirListHandler{webdav: &webdav.Handler{FileSystem: fs, LockSystem: webdav.NewMemLS()}, fs: fs}

	tests := []struct {
		path string
		want string
	}{
		{"/files", "/files/"},
		{"/files/sub dir", "/files/sub%20dir/"},
		// Protocol-relative path must not redirect off-site.
		{"//evil.com", "/evil.com/"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = tt.path
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("%q: status = %d, want %d", tt.path, rec.Code, http.StatusMovedPermanently)
		}
		if got := rec.Header().Get("Location"); got != tt.want {
			t.Errorf("%q: Location = %q, want %q", tt.path, got, tt.want)
		}
	}
}
