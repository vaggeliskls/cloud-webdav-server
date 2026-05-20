package storage

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/net/webdav"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GCSConfig holds the GCP Cloud Storage connection parameters.
type GCSConfig struct {
	Bucket      string
	Prefix      string // optional object key prefix
	Credentials string // path to service-account JSON; empty = ADC
}

// GCSFileSystem implements webdav.FileSystem backed by GCP Cloud Storage.
type GCSFileSystem struct {
	client *storage.Client
	bucket string
	prefix string
}

// NewGCS creates a new GCSFileSystem.
func NewGCS(ctx context.Context, cfg GCSConfig) (*GCSFileSystem, error) {
	var opts []option.ClientOption
	if cfg.Credentials != "" {
		data, err := os.ReadFile(cfg.Credentials)
		if err != nil {
			return nil, fmt.Errorf("gcs: read credentials file: %w", err)
		}
		creds, err := google.CredentialsFromJSONWithParams(ctx, data, google.CredentialsParams{ //nolint:staticcheck // credential file is operator-supplied and trusted
			Scopes: []string{storage.ScopeFullControl},
		})
		if err != nil {
			return nil, fmt.Errorf("gcs: parse credentials: %w", err)
		}
		opts = append(opts, option.WithTokenSource(creds.TokenSource))
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs: new client: %w", err)
	}
	prefix := cfg.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &GCSFileSystem{client: client, bucket: cfg.Bucket, prefix: prefix}, nil
}

// keyFor converts a WebDAV path to a GCS object name.
func (fs *GCSFileSystem) keyFor(name string) string {
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")
	if name == "." || name == "" {
		return fs.prefix
	}
	return fs.prefix + name
}

func (fs *GCSFileSystem) dirKeyFor(name string) string {
	k := fs.keyFor(name)
	if k == "" {
		return fs.prefix
	}
	if !strings.HasSuffix(k, "/") {
		k += "/"
	}
	return k
}

func (fs *GCSFileSystem) bkt() *storage.BucketHandle {
	return fs.client.Bucket(fs.bucket)
}

// Mkdir creates a directory marker object.
func (fs *GCSFileSystem) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	key := fs.dirKeyFor(name)
	w := fs.bkt().Object(key).NewWriter(ctx)
	w.ContentType = "application/x-directory"
	if _, err := w.Write(nil); err != nil {
		return err
	}
	return w.Close()
}

// OpenFile opens or creates a GCS object.
func (fs *GCSFileSystem) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	key := fs.keyFor(name)
	isRoot := key == fs.prefix

	if isRoot {
		return &gcsFile{fs: fs, name: name, key: key, isDir: true, ctx: ctx}, nil
	}

	// Check directory marker.
	dirKey := key
	if !strings.HasSuffix(dirKey, "/") {
		dirKey += "/"
	}
	_, err := fs.bkt().Object(dirKey).Attrs(ctx)
	if err == nil {
		return &gcsFile{fs: fs, name: name, key: dirKey, isDir: true, ctx: ctx}, nil
	}

	// Check for virtual directory (objects sharing this prefix, no marker needed).
	query := &storage.Query{Prefix: dirKey}
	it := fs.bkt().Objects(ctx, query)
	if _, err2 := it.Next(); err2 == nil {
		return &gcsFile{fs: fs, name: name, key: dirKey, isDir: true, ctx: ctx}, nil
	}

	// Write mode — stream directly to GCS via storage.Writer.
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 || flag&os.O_CREATE != 0 {
		return &gcsFile{
			fs:     fs,
			name:   name,
			key:    key,
			isDir:  false,
			write:  true,
			writer: fs.bkt().Object(key).NewWriter(ctx),
			ctx:    ctx,
			fi:     &s3FileInfo{name: path.Base(name), modTime: time.Now()},
		}, nil
	}

	// Read mode.
	r, err := fs.bkt().Object(key).NewReader(ctx)
	if err != nil {
		if err == storage.ErrObjectNotExist {
			return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
		}
		return nil, err
	}
	defer r.Close() //nolint:errcheck
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	attrs, _ := fs.bkt().Object(key).Attrs(ctx)
	var modTime time.Time
	if attrs != nil {
		modTime = attrs.Updated
	}

	return &gcsFile{
		fs:     fs,
		name:   name,
		key:    key,
		isDir:  false,
		reader: bytes.NewReader(data),
		ctx:    ctx,
		fi:     &s3FileInfo{name: path.Base(name), size: int64(len(data)), modTime: modTime},
	}, nil
}

// RemoveAll deletes a GCS object or all objects under a prefix.
func (fs *GCSFileSystem) RemoveAll(ctx context.Context, name string) error {
	key := fs.keyFor(name)
	dirKey := key
	if !strings.HasSuffix(dirKey, "/") {
		dirKey += "/"
	}

	// Delete file.
	fs.bkt().Object(key).Delete(ctx) //nolint:errcheck

	// Delete directory and children.
	query := &storage.Query{Prefix: dirKey}
	it := fs.bkt().Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		if delErr := fs.bkt().Object(attrs.Name).Delete(ctx); delErr != nil {
			return delErr
		}
	}
	return nil
}

// Rename copies src to dst then deletes src.
func (fs *GCSFileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldKey := fs.keyFor(oldName)
	newKey := fs.keyFor(newName)

	// Try single file copy.
	_, err := fs.bkt().Object(newKey).CopierFrom(fs.bkt().Object(oldKey)).Run(ctx)
	if err == nil {
		fs.bkt().Object(oldKey).Delete(ctx) //nolint:errcheck
		return nil
	}
	if err != storage.ErrObjectNotExist {
		return err
	}

	// Directory rename.
	oldDirKey := oldKey + "/"
	newDirKey := newKey + "/"
	query := &storage.Query{Prefix: oldDirKey}
	it := fs.bkt().Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		src := attrs.Name
		dst := newDirKey + strings.TrimPrefix(src, oldDirKey)
		if _, err := fs.bkt().Object(dst).CopierFrom(fs.bkt().Object(src)).Run(ctx); err != nil {
			return err
		}
		if err := fs.bkt().Object(src).Delete(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Stat returns file info for the given path.
func (fs *GCSFileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	key := fs.keyFor(name)
	if key == fs.prefix {
		return &s3FileInfo{name: "/", isDir: true, modTime: time.Now()}, nil
	}

	attrs, err := fs.bkt().Object(key).Attrs(ctx)
	if err == nil {
		return &s3FileInfo{
			name:    path.Base(name),
			size:    attrs.Size,
			modTime: attrs.Updated,
		}, nil
	}

	// Try directory marker.
	dirKey := key + "/"
	_, err2 := fs.bkt().Object(dirKey).Attrs(ctx)
	if err2 == nil {
		return &s3FileInfo{name: path.Base(name), isDir: true, modTime: time.Now()}, nil
	}

	// Check for virtual directory (objects with prefix).
	query := &storage.Query{Prefix: dirKey}
	it := fs.bkt().Objects(ctx, query)
	_, err3 := it.Next()
	if err3 == nil {
		return &s3FileInfo{name: path.Base(name), isDir: true, modTime: time.Now()}, nil
	}

	return nil, &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

// ---- gcsFile ---------------------------------------------------------------

type gcsFile struct {
	fs     *GCSFileSystem
	name   string
	key    string
	isDir  bool
	write  bool
	writer *storage.Writer
	fi     *s3FileInfo

	reader *bytes.Reader
	ctx    context.Context
}

func (f *gcsFile) Close() error {
	if f.write && f.writer != nil {
		err := f.writer.Close()
		f.writer = nil
		return err
	}
	return nil
}

func (f *gcsFile) Read(p []byte) (int, error) {
	if f.isDir {
		return 0, fmt.Errorf("is a directory")
	}
	if f.reader == nil {
		return 0, io.EOF
	}
	return f.reader.Read(p)
}

func (f *gcsFile) Seek(offset int64, whence int) (int64, error) {
	if f.reader != nil {
		return f.reader.Seek(offset, whence)
	}
	return 0, fmt.Errorf("seek not supported on write-only file")
}

func (f *gcsFile) Write(p []byte) (int, error) {
	if !f.write || f.writer == nil {
		return 0, fmt.Errorf("not opened for writing")
	}
	return f.writer.Write(p)
}

func (f *gcsFile) Readdir(count int) ([]os.FileInfo, error) {
	if !f.isDir {
		return nil, fmt.Errorf("not a directory")
	}
	prefix := f.key
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	var infos []os.FileInfo
	seen := map[string]bool{}

	query := &storage.Query{
		Prefix:    prefix,
		Delimiter: "/",
	}
	it := f.fs.bkt().Objects(f.ctx, query)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		if attrs.Prefix != "" {
			// Sub-directory.
			dirName := strings.TrimPrefix(attrs.Prefix, prefix)
			dirName = strings.TrimSuffix(dirName, "/")
			if dirName == "" || seen[dirName] {
				continue
			}
			seen[dirName] = true
			infos = append(infos, &s3FileInfo{name: dirName, isDir: true, modTime: time.Now()})
		} else {
			// File.
			fileName := strings.TrimPrefix(attrs.Name, prefix)
			if fileName == "" || seen[fileName] {
				continue
			}
			seen[fileName] = true
			infos = append(infos, &s3FileInfo{
				name:    fileName,
				size:    attrs.Size,
				modTime: attrs.Updated,
			})
		}
		if count > 0 && len(infos) >= count {
			break
		}
	}
	return infos, nil
}

func (f *gcsFile) Stat() (os.FileInfo, error) {
	if f.fi != nil {
		return f.fi, nil
	}
	return &s3FileInfo{name: path.Base(f.name), isDir: f.isDir, modTime: time.Now()}, nil
}

func (f *gcsFile) DeadProps() (map[xml.Name]webdav.Property, error) {
	return nil, nil
}

func (f *gcsFile) Patch(_ []webdav.Proppatch) ([]webdav.Propstat, error) {
	return nil, nil
}
