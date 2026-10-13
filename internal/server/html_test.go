package server

import (
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// htmlDo sends a request as alice (unless anonymous) and returns status, headers and body.
func htmlDo(t *testing.T, ts *httptest.Server, method, path, body string, anonymous bool, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if !anonymous {
		req.SetBasicAuth("alice", "alice123")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(data)
}

var acceptHTML = map[string]string{"Accept": "text/html"}

func TestDirListing_RendersEntries(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	htmlDo(t, ts, "MKCOL", "/files/sub/", "", false, nil)
	htmlDo(t, ts, "PUT", "/files/b.txt", "bbb", false, nil)
	htmlDo(t, ts, "PUT", "/files/a.txt", "a", false, nil)

	status, hdr, body := htmlDo(t, ts, "GET", "/files/", "", false, acceptHTML)
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type: want text/html, got %q", ct)
	}
	if cd := hdr.Get("Content-Disposition"); cd != "" {
		t.Errorf("listing must not be a download, got Content-Disposition %q", cd)
	}
	for _, want := range []string{
		"<title>Index of /files/</title>",
		`<a href="/files/">files</a>`, // breadcrumb
		`href="/" aria-label="Parent directory"`,
		`<a class="row" href="sub/">`,
		"3 items",
		"3 B", // size of b.txt
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listing missing %q", want)
		}
	}
	// Folders first, then files alphabetically.
	iSub, iA, iB := strings.Index(body, ">sub<"), strings.Index(body, ">a.txt<"), strings.Index(body, ">b.txt<")
	if iSub < 0 || iSub > iA || iA > iB {
		t.Errorf("want order sub/, a.txt, b.txt; got positions %d, %d, %d", iSub, iA, iB)
	}
}

func TestDirListing_EscapesNames(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	name := `<img src=x onerror=alert(1)>.txt`
	if status, _, _ := htmlDo(t, ts, "PUT", "/files/"+url.PathEscape(name), "x", false, nil); status != http.StatusCreated {
		t.Fatalf("PUT: want 201, got %d", status)
	}
	_, _, body := htmlDo(t, ts, "GET", "/files/", "", false, acceptHTML)
	if strings.Contains(body, "<img") {
		t.Fatal("file name rendered as raw HTML")
	}
	if !strings.Contains(body, "&lt;img src=x onerror=alert(1)&gt;.txt") {
		t.Error("escaped file name not found in listing")
	}
}

func TestDirListing_EmptyFolder(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	_, _, body := htmlDo(t, ts, "GET", "/files/", "", false, acceptHTML)
	if !strings.Contains(body, "0 items") {
		t.Error(`empty listing should say "0 items"`)
	}
}

func TestDirListing_RedirectsWithoutTrailingSlash(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	htmlDo(t, ts, "MKCOL", "/files/sub/", "", false, nil)
	status, hdr, _ := htmlDo(t, ts, "GET", "/files/sub", "", false, acceptHTML)
	if status != http.StatusMovedPermanently {
		t.Fatalf("want 301, got %d", status)
	}
	if loc := hdr.Get("Location"); loc != "/files/sub/" {
		t.Errorf("Location: want /files/sub/, got %q", loc)
	}
}

func TestDirListing_HeadHasNoBody(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	status, hdr, body := htmlDo(t, ts, "HEAD", "/files/", "", false, acceptHTML)
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type: want text/html, got %q", hdr.Get("Content-Type"))
	}
	if body != "" {
		t.Errorf("HEAD must not return a body, got %d bytes", len(body))
	}
}

func TestErrorPage_HTMLForBrowsers(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	status, hdr, body := htmlDo(t, ts, "GET", "/files/", "", true, acceptHTML)
	if status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", status)
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type: want text/html, got %q", hdr.Get("Content-Type"))
	}
	if hdr.Get("WWW-Authenticate") == "" {
		t.Error("401 must keep the WWW-Authenticate challenge")
	}
	for _, want := range []string{"<title>401 Unauthorized</title>", "Authentication is required"} {
		if !strings.Contains(body, want) {
			t.Errorf("error page missing %q", want)
		}
	}
}

func TestErrorPage_PlainTextForClients(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	status, hdr, body := htmlDo(t, ts, "GET", "/files/", "", true, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", status)
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") {
		t.Errorf("Content-Type: want text/plain, got %q", hdr.Get("Content-Type"))
	}
	if strings.Contains(body, "<html") {
		t.Error("non-browser clients should not get HTML")
	}
}

func TestDownload_ContentDispositionRoundTrips(t *testing.T) {
	ts := newTestServer(t, "/files:alice:rw")
	defer ts.Close()

	for _, name := range []string{"report.pdf", "my file.txt", `quote"d.txt`, "résumé.pdf"} {
		t.Run(name, func(t *testing.T) {
			p := "/files/" + url.PathEscape(name)
			if status, _, _ := htmlDo(t, ts, "PUT", p, "data", false, nil); status != http.StatusCreated {
				t.Fatalf("PUT: want 201, got %d", status)
			}
			_, hdr, _ := htmlDo(t, ts, "GET", p, "", false, nil)
			disp, params, err := mime.ParseMediaType(hdr.Get("Content-Disposition"))
			if err != nil {
				t.Fatalf("unparseable Content-Disposition %q: %v", hdr.Get("Content-Disposition"), err)
			}
			if disp != "attachment" || params["filename"] != name {
				t.Errorf("want attachment with filename %q, got %q %v", name, disp, params)
			}
		})
	}
}
