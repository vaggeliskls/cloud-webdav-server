package storage

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"golang.org/x/net/webdav"
)

// AzureConfig holds the Azure Blob Storage connection parameters.
type AzureConfig struct {
	Account          string // storage account name (ignored when ConnectionString is set)
	Key              string // shared access key (ignored when ConnectionString is set)
	Container        string // blob container name
	Prefix           string // optional blob name prefix
	Endpoint         string // override service URL (e.g. http://127.0.0.1:10000/devstoreaccount1 for Azurite)
	ConnectionString string // full connection string; takes precedence over Account/Key
}

// AzureFileSystem implements webdav.FileSystem backed by Azure Blob Storage.
type AzureFileSystem struct {
	client    *azblob.Client
	container string
	prefix    string
}

// NewAzure creates a new AzureFileSystem.
func NewAzure(_ context.Context, cfg AzureConfig) (*AzureFileSystem, error) {
	var (
		client *azblob.Client
		err    error
	)

	switch {
	case cfg.ConnectionString != "":
		client, err = azblob.NewClientFromConnectionString(cfg.ConnectionString, nil)
	case cfg.Account != "" && cfg.Key != "":
		cred, credErr := azblob.NewSharedKeyCredential(cfg.Account, cfg.Key)
		if credErr != nil {
			return nil, fmt.Errorf("azure: shared key credential: %w", credErr)
		}
		serviceURL := cfg.Endpoint
		if serviceURL == "" {
			serviceURL = fmt.Sprintf("https://%s.blob.core.windows.net/", cfg.Account)
		}
		client, err = azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
	default:
		return nil, errors.New("azure: provide AZURE_STORAGE_CONNECTION_STRING or AZURE_STORAGE_ACCOUNT + AZURE_STORAGE_KEY")
	}
	if err != nil {
		return nil, fmt.Errorf("azure: new client: %w", err)
	}

	prefix := cfg.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &AzureFileSystem{client: client, container: cfg.Container, prefix: prefix}, nil
}

// keyFor converts a WebDAV path to an Azure blob name.
func (fs *AzureFileSystem) keyFor(name string) string {
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")
	if name == "." || name == "" {
		return fs.prefix
	}
	return fs.prefix + name
}

func (fs *AzureFileSystem) dirKeyFor(name string) string {
	k := fs.keyFor(name)
	if k == "" {
		return fs.prefix
	}
	if !strings.HasSuffix(k, "/") {
		k += "/"
	}
	return k
}

func (fs *AzureFileSystem) containerClient() *container.Client {
	return fs.client.ServiceClient().NewContainerClient(fs.container)
}

// Mkdir creates an empty directory marker blob.
func (fs *AzureFileSystem) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	key := fs.dirKeyFor(name)
	_, err := fs.client.UploadBuffer(ctx, fs.container, key, nil, nil)
	return err
}

// OpenFile opens or creates a blob.
func (fs *AzureFileSystem) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	key := fs.keyFor(name)
	isRoot := key == fs.prefix

	if isRoot {
		return &azureFile{fs: fs, name: name, key: key, isDir: true, ctx: ctx}, nil
	}

	// Directory marker check.
	dirKey := key
	if !strings.HasSuffix(dirKey, "/") {
		dirKey += "/"
	}
	if _, err := fs.containerClient().NewBlobClient(dirKey).GetProperties(ctx, nil); err == nil {
		return &azureFile{fs: fs, name: name, key: dirKey, isDir: true, ctx: ctx}, nil
	}

	// Virtual directory check (any blob shares this prefix).
	if hasPrefix, err := fs.anyBlobWithPrefix(ctx, dirKey); err == nil && hasPrefix {
		return &azureFile{fs: fs, name: name, key: dirKey, isDir: true, ctx: ctx}, nil
	}

	// Write mode — stream directly to Azure via UploadStream + io.Pipe.
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 || flag&os.O_CREATE != 0 {
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			_, err := fs.client.UploadStream(ctx, fs.container, key, pr, nil)
			// Surface any upload error back to pending Write() callers.
			_ = pr.CloseWithError(err)
			done <- err
		}()
		return &azureFile{
			fs:         fs,
			name:       name,
			key:        key,
			isDir:      false,
			write:      true,
			pw:         pw,
			uploadDone: done,
			ctx:        ctx,
			fi:         &s3FileInfo{name: path.Base(name), modTime: time.Now()},
		}, nil
	}

	// Read mode.
	resp, err := fs.client.DownloadStream(ctx, fs.container, key, nil)
	if err != nil {
		return nil, mapAzureError(err, name)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var size int64
	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}
	var modTime time.Time
	if resp.LastModified != nil {
		modTime = *resp.LastModified
	}
	return &azureFile{
		fs:     fs,
		name:   name,
		key:    key,
		isDir:  false,
		reader: bytes.NewReader(data),
		ctx:    ctx,
		fi:     &s3FileInfo{name: path.Base(name), size: size, modTime: modTime},
	}, nil
}

// RemoveAll deletes a blob or all blobs under a prefix.
func (fs *AzureFileSystem) RemoveAll(ctx context.Context, name string) error {
	key := fs.keyFor(name)
	dirKey := key
	if !strings.HasSuffix(dirKey, "/") {
		dirKey += "/"
	}

	// Best-effort delete of the file blob.
	_, errFile := fs.client.DeleteBlob(ctx, fs.container, key, nil)

	// Delete every blob sharing the directory prefix.
	pager := fs.client.NewListBlobsFlatPager(fs.container, &azblob.ListBlobsFlatOptions{
		Prefix: to.Ptr(dirKey),
	})
	deletedAny := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, b := range page.Segment.BlobItems {
			if b.Name == nil {
				continue
			}
			if _, err := fs.client.DeleteBlob(ctx, fs.container, *b.Name, nil); err != nil {
				return err
			}
			deletedAny = true
		}
	}

	if errFile != nil && !deletedAny {
		return mapAzureError(errFile, name)
	}
	return nil
}

// Rename copies src to dst then deletes src (Azure has no native rename).
func (fs *AzureFileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldKey := fs.keyFor(oldName)
	newKey := fs.keyFor(newName)

	// Single-file rename.
	if err := fs.copyBlob(ctx, oldKey, newKey); err == nil {
		_, _ = fs.client.DeleteBlob(ctx, fs.container, oldKey, nil) //nolint:errcheck
		return nil
	} else if !isNotFound(err) {
		return err
	}

	// Directory rename.
	oldDirKey := oldKey + "/"
	newDirKey := newKey + "/"
	pager := fs.client.NewListBlobsFlatPager(fs.container, &azblob.ListBlobsFlatOptions{
		Prefix: to.Ptr(oldDirKey),
	})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, b := range page.Segment.BlobItems {
			if b.Name == nil {
				continue
			}
			src := *b.Name
			dst := newDirKey + strings.TrimPrefix(src, oldDirKey)
			if err := fs.copyBlob(ctx, src, dst); err != nil {
				return err
			}
			if _, err := fs.client.DeleteBlob(ctx, fs.container, src, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Stat returns file info for the given path.
func (fs *AzureFileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	key := fs.keyFor(name)
	if key == fs.prefix {
		return &s3FileInfo{name: "/", isDir: true, modTime: time.Now()}, nil
	}

	if props, err := fs.containerClient().NewBlobClient(key).GetProperties(ctx, nil); err == nil {
		var size int64
		if props.ContentLength != nil {
			size = *props.ContentLength
		}
		var modTime time.Time
		if props.LastModified != nil {
			modTime = *props.LastModified
		}
		return &s3FileInfo{name: path.Base(name), size: size, modTime: modTime}, nil
	}

	dirKey := key + "/"
	if _, err := fs.containerClient().NewBlobClient(dirKey).GetProperties(ctx, nil); err == nil {
		return &s3FileInfo{name: path.Base(name), isDir: true, modTime: time.Now()}, nil
	}

	if hasPrefix, err := fs.anyBlobWithPrefix(ctx, dirKey); err == nil && hasPrefix {
		return &s3FileInfo{name: path.Base(name), isDir: true, modTime: time.Now()}, nil
	}

	return nil, &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

// anyBlobWithPrefix returns true if at least one blob exists under the given prefix.
func (fs *AzureFileSystem) anyBlobWithPrefix(ctx context.Context, prefix string) (bool, error) {
	pager := fs.client.NewListBlobsFlatPager(fs.container, &azblob.ListBlobsFlatOptions{
		Prefix:     to.Ptr(prefix),
		MaxResults: to.Ptr(int32(1)),
	})
	if !pager.More() {
		return false, nil
	}
	page, err := pager.NextPage(ctx)
	if err != nil {
		return false, err
	}
	return len(page.Segment.BlobItems) > 0, nil
}

// copyBlob performs a same-container server-side copy.
func (fs *AzureFileSystem) copyBlob(ctx context.Context, srcKey, dstKey string) error {
	srcURL := fs.containerClient().NewBlobClient(srcKey).URL()
	_, err := fs.containerClient().NewBlobClient(dstKey).StartCopyFromURL(ctx, srcURL, nil)
	return err
}

// ---- azureFile -------------------------------------------------------------

type azureFile struct {
	fs         *AzureFileSystem
	name       string
	key        string
	isDir      bool
	write      bool
	pw         *io.PipeWriter
	uploadDone chan error
	fi         *s3FileInfo

	reader *bytes.Reader
	ctx    context.Context
}

func (f *azureFile) Close() error {
	if f.write && f.pw != nil {
		// Closing the writer signals EOF to the UploadStream goroutine;
		// then we wait for it to finish and surface any error.
		_ = f.pw.Close()
		f.pw = nil
		return <-f.uploadDone
	}
	return nil
}

func (f *azureFile) Read(p []byte) (int, error) {
	if f.isDir {
		return 0, fmt.Errorf("is a directory")
	}
	if f.reader == nil {
		return 0, io.EOF
	}
	return f.reader.Read(p)
}

func (f *azureFile) Seek(offset int64, whence int) (int64, error) {
	if f.reader != nil {
		return f.reader.Seek(offset, whence)
	}
	return 0, fmt.Errorf("seek not supported on write-only file")
}

func (f *azureFile) Write(p []byte) (int, error) {
	if !f.write || f.pw == nil {
		return 0, fmt.Errorf("not opened for writing")
	}
	return f.pw.Write(p)
}

func (f *azureFile) Readdir(count int) ([]os.FileInfo, error) {
	if !f.isDir {
		return nil, fmt.Errorf("not a directory")
	}
	prefix := f.key
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	var infos []os.FileInfo
	seen := map[string]bool{}

	pager := f.fs.containerClient().NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{
		Prefix: to.Ptr(prefix),
	})
	for pager.More() {
		page, err := pager.NextPage(f.ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range page.Segment.BlobPrefixes {
			if p.Name == nil {
				continue
			}
			dirName := strings.TrimPrefix(*p.Name, prefix)
			dirName = strings.TrimSuffix(dirName, "/")
			if dirName == "" || seen[dirName] {
				continue
			}
			seen[dirName] = true
			infos = append(infos, &s3FileInfo{name: dirName, isDir: true, modTime: time.Now()})
		}
		for _, b := range page.Segment.BlobItems {
			if b.Name == nil {
				continue
			}
			if *b.Name == prefix {
				continue // directory marker
			}
			fileName := strings.TrimPrefix(*b.Name, prefix)
			if fileName == "" || seen[fileName] {
				continue
			}
			seen[fileName] = true
			var size int64
			var modTime time.Time
			if b.Properties != nil {
				if b.Properties.ContentLength != nil {
					size = *b.Properties.ContentLength
				}
				if b.Properties.LastModified != nil {
					modTime = *b.Properties.LastModified
				}
			}
			infos = append(infos, &s3FileInfo{name: fileName, size: size, modTime: modTime})
		}
		if count > 0 && len(infos) >= count {
			break
		}
	}
	if count > 0 && len(infos) > count {
		infos = infos[:count]
	}
	return infos, nil
}

func (f *azureFile) Stat() (os.FileInfo, error) {
	if f.fi != nil {
		return f.fi, nil
	}
	return &s3FileInfo{name: path.Base(f.name), isDir: f.isDir, modTime: time.Now()}, nil
}

func (f *azureFile) DeadProps() (map[xml.Name]webdav.Property, error) {
	return nil, nil
}

func (f *azureFile) Patch(_ []webdav.Proppatch) ([]webdav.Propstat, error) {
	return nil, nil
}

// ---- helpers ---------------------------------------------------------------

func mapAzureError(err error, name string) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}
	return err
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return bloberror.HasCode(err, bloberror.BlobNotFound) ||
		bloberror.HasCode(err, bloberror.ContainerNotFound) ||
		strings.Contains(err.Error(), "404")
}
