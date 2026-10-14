package server

import (
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"golang.org/x/net/webdav"
)

//go:embed templates/dirlist.html
var dirListTemplateSrc string

var dirListTemplate = template.Must(template.New("dirlist").Parse(dirListTemplateSrc))

// dirListHandler wraps a webdav.Handler and intercepts GET/HEAD requests on
// directories, serving an HTML directory listing instead of returning 405.
type dirListHandler struct {
	webdav http.Handler
	fs     webdav.FileSystem
}

type dirListData struct {
	Path        string
	Parent      string
	Breadcrumbs []breadcrumb
	Entries     []dirListEntry
	Count       int
}

type breadcrumb struct {
	Name string
	Href string
}

type dirListEntry struct {
	Name    string
	Href    string
	IsDir   bool
	Size    string
	ModTime string
}

func makeBreadcrumbs(p string) []breadcrumb {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	crumbs := make([]breadcrumb, 0, len(parts))
	var accum strings.Builder
	for _, part := range parts {
		accum.WriteByte('/')
		accum.WriteString(part)
		crumbs = append(crumbs, breadcrumb{Name: part, Href: accum.String() + "/"})
	}
	return crumbs
}

func (h *dirListHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.webdav.ServeHTTP(w, r)
		return
	}

	urlPath := r.URL.Path
	if !strings.HasSuffix(urlPath, "/") {
		fi, err := h.fs.Stat(r.Context(), urlPath)
		if err == nil && fi.IsDir() {
			// Relative target: http.Redirect resolves it against the request
			// path and cleans it, so "//host" can never become the Location.
			http.Redirect(w, r, "./"+url.PathEscape(path.Base(urlPath))+"/", http.StatusMovedPermanently)
			return
		}
	}

	fi, err := h.fs.Stat(r.Context(), urlPath)
	if err != nil || !fi.IsDir() {
		h.webdav.ServeHTTP(w, r)
		return
	}

	f, err := h.fs.OpenFile(r.Context(), urlPath, 0, 0)
	if err != nil {
		slog.Error("openfile failed", "path", urlPath, "err", err)
		renderError(w, r, http.StatusInternalServerError, "Could not open the directory.")
		return
	}
	defer f.Close()

	entries, err := f.Readdir(-1)
	if err != nil {
		slog.Error("readdir failed", "path", urlPath, "err", err)
		renderError(w, r, http.StatusInternalServerError, "Could not read the directory.")
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return entries[i].Name() < entries[j].Name()
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	data := dirListData{
		Path:        urlPath,
		Breadcrumbs: makeBreadcrumbs(urlPath),
		Entries:     make([]dirListEntry, 0, len(entries)),
		Count:       len(entries),
	}
	if urlPath != "/" {
		parent := path.Dir(strings.TrimSuffix(urlPath, "/"))
		if !strings.HasSuffix(parent, "/") {
			parent += "/"
		}
		data.Parent = parent
	}
	for _, e := range entries {
		entry := dirListEntry{
			Name:    e.Name(),
			Href:    e.Name(),
			IsDir:   e.IsDir(),
			ModTime: e.ModTime().Format("2006-01-02 15:04"),
		}
		if e.IsDir() {
			entry.Href += "/"
		} else {
			entry.Size = formatSize(e.Size())
		}
		data.Entries = append(data.Entries, entry)
	}

	if err := dirListTemplate.Execute(w, data); err != nil {
		slog.Error("template execute failed", "err", err)
	}
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
