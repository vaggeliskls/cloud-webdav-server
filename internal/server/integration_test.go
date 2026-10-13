//go:build integration

// Integration tests run the full server against every storage backend.
// Cloud backends talk to local emulators (MinIO, fake-gcs-server, Azurite):
//
//	task emulators-up
//	go test -tags integration -count=1 ./internal/server/
//
// A cloud backend whose emulator is unreachable is skipped locally, but fails
// when CI=true so a broken CI setup can't pass silently.
package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cloud-webdav-server/internal/config"
)

const azuriteConnStr = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
	"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
	"BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;"

type backend struct {
	name     string
	endpoint string // emulator address probed before running; empty = always available
	// implicitDirs is true when objects can be written without creating
	// their parent folder first (object stores, not the local filesystem).
	implicitDirs bool
	configure    func(t *testing.T, cfg *config.Config, prefix string)
}

func backends() []backend {
	return []backend{
		{
			name: "local",
			configure: func(t *testing.T, cfg *config.Config, _ string) {
				cfg.StorageType = config.StorageLocal
				cfg.LocalDataPath = t.TempDir()
			},
		},
		{
			name:         "s3",
			endpoint:     envOr("IT_S3_ENDPOINT", "http://localhost:9000"),
			implicitDirs: true,
			configure: func(_ *testing.T, cfg *config.Config, prefix string) {
				cfg.StorageType = config.StorageS3
				cfg.S3Bucket = envOr("IT_S3_BUCKET", "webdav")
				cfg.S3Region = "us-east-1"
				cfg.S3Endpoint = envOr("IT_S3_ENDPOINT", "http://localhost:9000")
				cfg.S3AccessKey = envOr("IT_S3_ACCESS_KEY", "minioadmin")
				cfg.S3SecretKey = envOr("IT_S3_SECRET_KEY", "minioadmin")
				cfg.S3Prefix = prefix
			},
		},
		{
			name:         "gcs",
			endpoint:     envOr("IT_GCS_ENDPOINT", "http://localhost:4443"),
			implicitDirs: true,
			configure: func(t *testing.T, cfg *config.Config, prefix string) {
				t.Setenv("STORAGE_EMULATOR_HOST", envOr("IT_GCS_ENDPOINT", "http://localhost:4443"))
				cfg.StorageType = config.StorageGCS
				cfg.GCSBucket = envOr("IT_GCS_BUCKET", "webdav")
				cfg.GCSPrefix = prefix
			},
		},
		{
			name:         "azure",
			endpoint:     "http://127.0.0.1:10000",
			implicitDirs: true,
			configure: func(_ *testing.T, cfg *config.Config, prefix string) {
				cfg.StorageType = config.StorageAzure
				cfg.AzureContainer = envOr("IT_AZURE_CONTAINER", "webdav")
				cfg.AzureConnectionString = envOr("IT_AZURE_CONNECTION_STRING", azuriteConnStr)
				cfg.AzurePrefix = prefix
			},
		},
	}
}

func TestBackends(t *testing.T) {
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			if b.endpoint != "" && !reachable(b.endpoint) {
				if os.Getenv("CI") == "true" {
					t.Fatalf("%s emulator not reachable at %s", b.name, b.endpoint)
				}
				t.Skipf("%s emulator not reachable at %s (run `task emulators-up`)", b.name, b.endpoint)
			}
			// Unique prefix per run so repeated runs don't see each other's objects.
			prefix := fmt.Sprintf("it-%d", time.Now().UnixNano())
			ts := newTestServer(t, "/files:alice:rw,/public:public:ro,/nested/deep:alice:rw", func(cfg *config.Config) {
				b.configure(t, cfg, prefix)
			})
			defer ts.Close()
			runBackendSuite(t, &davClient{base: ts.URL}, b)
		})
	}
}

func runBackendSuite(t *testing.T, c *davClient, b backend) {
	small := []byte("hello webdav")
	// Larger than the S3 transfer manager's multipart threshold.
	big := make([]byte, 20<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}

	step := func(name string, fn func(t *testing.T)) {
		if !t.Run(name, fn) {
			t.FailNow() // later steps depend on earlier ones
		}
	}

	step("MKCOL", func(t *testing.T) {
		c.expect(t, http.StatusCreated, "MKCOL", "/files/it/", nil, nil)
	})
	step("auto-created nested folder", func(t *testing.T) {
		c.expect(t, http.StatusMultiStatus, "PROPFIND", "/nested/deep/", nil, map[string]string{"Depth": "0"})
	})
	// MKCOL preconditions (RFC 4918 §9.3), found failing on object stores by litmus.
	step("MKCOL on existing folder is 405", func(t *testing.T) {
		c.expect(t, http.StatusMethodNotAllowed, "MKCOL", "/files/it/", nil, nil)
	})
	step("MKCOL with missing parent is 409", func(t *testing.T) {
		c.expect(t, http.StatusConflict, "MKCOL", "/files/it/missing/child/", nil, nil)
	})
	step("PUT small", func(t *testing.T) {
		c.expect(t, http.StatusCreated, "PUT", "/files/it/small.txt", small, nil)
	})
	step("MKCOL over a file is 405", func(t *testing.T) {
		c.expect(t, http.StatusMethodNotAllowed, "MKCOL", "/files/it/small.txt", nil, nil)
	})
	// Regression: cloud backends opened O_RDWR as a new upload, so PROPPATCH
	// (sent by Windows Explorer after every upload) emptied the file.
	step("PROPPATCH keeps content", func(t *testing.T) {
		c.expect(t, http.StatusMultiStatus, "PROPPATCH", "/files/it/small.txt",
			[]byte(`<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:" xmlns:Z="urn:test">`+
				`<D:set><D:prop><Z:color>red</Z:color></D:prop></D:set></D:propertyupdate>`),
			map[string]string{"Content-Type": "application/xml"})
		if got := c.expect(t, http.StatusOK, "GET", "/files/it/small.txt", nil, nil).body; !bytes.Equal(got, small) {
			t.Fatalf("content after PROPPATCH: got %q, want %q", got, small)
		}
	})
	step("PUT big (multipart)", func(t *testing.T) {
		c.expect(t, http.StatusCreated, "PUT", "/files/it/big.bin", big, nil)
	})
	step("unauthenticated GET is 401", func(t *testing.T) {
		resp := c.doAs(t, "", "GET", "/files/it/small.txt", nil, nil)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", resp.status)
		}
	})
	step("public folder is read-only", func(t *testing.T) {
		resp := c.doAs(t, "", "PUT", "/public/x.txt", small, nil)
		if resp.status != http.StatusForbidden {
			t.Fatalf("want 403, got %d", resp.status)
		}
	})
	step("GET big round-trips", func(t *testing.T) {
		got := c.expect(t, http.StatusOK, "GET", "/files/it/big.bin", nil, nil).body
		if sha256.Sum256(got) != sha256.Sum256(big) {
			t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(big))
		}
	})
	step("GET range", func(t *testing.T) {
		got := c.expect(t, http.StatusPartialContent, "GET", "/files/it/small.txt", nil,
			map[string]string{"Range": "bytes=0-4"}).body
		if string(got) != "hello" {
			t.Fatalf("want %q, got %q", "hello", got)
		}
	})
	step("HEAD", func(t *testing.T) {
		resp := c.expect(t, http.StatusOK, "HEAD", "/files/it/small.txt", nil, nil)
		if cl := resp.header.Get("Content-Length"); cl != fmt.Sprint(len(small)) {
			t.Fatalf("Content-Length: want %d, got %q", len(small), cl)
		}
	})
	step("PUT into subfolder", func(t *testing.T) {
		c.expect(t, http.StatusCreated, "MKCOL", "/files/it/sub/", nil, nil)
		c.expect(t, http.StatusCreated, "PUT", "/files/it/sub/a.txt", small, nil)
	})
	step("PROPFIND depth 1", func(t *testing.T) {
		got := c.propfindHrefs(t, "/files/it/")
		// self + small.txt + big.bin + sub/
		if len(got) != 4 {
			t.Fatalf("want 4 entries, got %d: %v", len(got), got)
		}
	})
	step("HTML directory listing", func(t *testing.T) {
		body := c.expect(t, http.StatusOK, "GET", "/files/it/", nil,
			map[string]string{"Accept": "text/html"}).body
		if !bytes.Contains(body, []byte("small.txt")) {
			t.Fatal("listing does not mention small.txt")
		}
	})
	if b.implicitDirs {
		// Regression: S3 OpenFile ignored folders that have no marker object.
		step("implicit folder is listable", func(t *testing.T) {
			c.expect(t, http.StatusCreated, "PUT", "/files/it/implicit/deep/f.txt", small, nil)
			if got := c.propfindHrefs(t, "/files/it/implicit/deep/"); len(got) != 2 {
				t.Fatalf("want 2 entries, got %d: %v", len(got), got)
			}
			c.expect(t, http.StatusOK, "GET", "/files/it/implicit/deep/", nil,
				map[string]string{"Accept": "text/html"})
		})
	}
	step("COPY file", func(t *testing.T) {
		c.expect(t, http.StatusCreated, "COPY", "/files/it/small.txt", nil,
			map[string]string{"Destination": c.base + "/files/it/copy.txt"})
		c.expect(t, http.StatusOK, "GET", "/files/it/small.txt", nil, nil)
	})
	step("MOVE file", func(t *testing.T) {
		c.expect(t, http.StatusCreated, "MOVE", "/files/it/copy.txt", nil,
			map[string]string{"Destination": c.base + "/files/it/moved.txt"})
		c.expect(t, http.StatusNotFound, "GET", "/files/it/copy.txt", nil, nil)
		if got := c.expect(t, http.StatusOK, "GET", "/files/it/moved.txt", nil, nil).body; !bytes.Equal(got, small) {
			t.Fatalf("moved content: got %q", got)
		}
	})
	// Regression: GCS Rename treated the copy's googleapi 404 as fatal.
	step("MOVE folder", func(t *testing.T) {
		c.expect(t, http.StatusCreated, "MOVE", "/files/it/sub/", nil,
			map[string]string{"Destination": c.base + "/files/it/sub2/"})
		c.expect(t, http.StatusNotFound, "GET", "/files/it/sub/a.txt", nil, nil)
		if got := c.expect(t, http.StatusOK, "GET", "/files/it/sub2/a.txt", nil, nil).body; !bytes.Equal(got, small) {
			t.Fatalf("moved content: got %q", got)
		}
	})
	step("DELETE folder", func(t *testing.T) {
		c.expect(t, http.StatusNoContent, "DELETE", "/files/it/", nil, nil)
		c.expect(t, http.StatusNotFound, "GET", "/files/it/small.txt", nil, nil)
		c.expect(t, http.StatusNotFound, "GET", "/files/it/sub2/a.txt", nil, nil)
	})
}

// ---- helpers ---------------------------------------------------------------

type davClient struct {
	base string
}

type davResponse struct {
	status int
	header http.Header
	body   []byte
}

// doAs sends a request as user ("" = anonymous, otherwise password user+"123").
func (c *davClient) doAs(t *testing.T, user, method, path string, body []byte, headers map[string]string) davResponse {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.SetBasicAuth(user, user+"123")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return davResponse{status: resp.StatusCode, header: resp.Header, body: data}
}

func (c *davClient) expect(t *testing.T, want int, method, path string, body []byte, headers map[string]string) davResponse {
	t.Helper()
	resp := c.doAs(t, "alice", method, path, body, headers)
	if resp.status != want {
		t.Fatalf("%s %s: want %d, got %d: %s", method, path, want, resp.status, truncate(resp.body))
	}
	return resp
}

func (c *davClient) propfindHrefs(t *testing.T, path string) []string {
	t.Helper()
	body := c.expect(t, http.StatusMultiStatus, "PROPFIND", path, nil, map[string]string{"Depth": "1"}).body
	var hrefs []string
	for _, part := range strings.Split(string(body), "<D:href>")[1:] {
		hrefs = append(hrefs, part[:strings.Index(part, "</D:href>")])
	}
	return hrefs
}

func reachable(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}
