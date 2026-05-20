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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/net/webdav"
)

// S3Config holds the AWS S3 connection parameters.
type S3Config struct {
	Bucket           string
	Region           string
	Prefix           string // optional key prefix inside the bucket
	Endpoint         string // custom endpoint (MinIO, LocalStack, etc.)
	AccessKey        string
	SecretKey        string
	VirtualHosted bool // use virtual-hosted style (bucket.endpoint) instead of path style
}

// S3FileSystem implements webdav.FileSystem backed by Amazon S3.
type S3FileSystem struct {
	client   *s3.Client
	uploader *transfermanager.Client // streaming/multipart uploader
	bucket   string
	prefix   string // always ends with "/" if non-empty, or is ""
}

// NewS3 creates a new S3FileSystem.
func NewS3(ctx context.Context, cfg S3Config) (*S3FileSystem, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
	}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load config: %w", err)
	}

	clientOpts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		virtualHosted := cfg.VirtualHosted
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = !virtualHosted
		})
	}
	client := s3.NewFromConfig(awsCfg, clientOpts...)

	prefix := cfg.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	return &S3FileSystem{
		client:   client,
		uploader: transfermanager.New(client),
		bucket:   cfg.Bucket,
		prefix:   prefix,
	}, nil
}

// keyFor converts a WebDAV path to an S3 object key.
func (fs *S3FileSystem) keyFor(name string) string {
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")
	if name == "." || name == "" {
		return fs.prefix
	}
	return fs.prefix + name
}

// dirKeyFor returns the directory marker key for the given path.
func (fs *S3FileSystem) dirKeyFor(name string) string {
	k := fs.keyFor(name)
	if k == "" {
		return fs.prefix
	}
	if !strings.HasSuffix(k, "/") {
		k += "/"
	}
	return k
}

// Mkdir creates a directory marker object.
func (fs *S3FileSystem) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	key := fs.dirKeyFor(name)
	_, err := fs.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(fs.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(nil),
		ContentType: aws.String("application/x-directory"),
	})
	return err
}

// OpenFile opens or creates a file in S3.
func (fs *S3FileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	key := fs.keyFor(name)
	isRoot := key == fs.prefix

	// Check if it's a directory (either root or has a "/" marker).
	if isRoot {
		return &s3File{fs: fs, name: name, key: key, isDir: true}, nil
	}
	// Check for directory marker.
	dirKey := key
	if !strings.HasSuffix(dirKey, "/") {
		dirKey += "/"
	}
	_, err := fs.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(fs.bucket),
		Key:    aws.String(dirKey),
	})
	if err == nil {
		return &s3File{fs: fs, name: name, key: dirKey, isDir: true}, nil
	}

	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 || flag&os.O_CREATE != 0 {
		// Write mode: stream directly to S3 via the transfer manager, which
		// transparently switches to multipart upload past the default threshold.
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			_, err := fs.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
				Bucket: aws.String(fs.bucket),
				Key:    aws.String(key),
				Body:   pr,
			})
			_ = pr.CloseWithError(err)
			done <- err
		}()
		return &s3File{
			fs:         fs,
			name:       name,
			key:        key,
			isDir:      false,
			write:      true,
			pw:         pw,
			uploadDone: done,
			ctx:        ctx,
			fi:         &s3FileInfo{name: path.Base(name), size: 0, modTime: time.Now()},
		}, nil
	}

	// Read mode: download the object.
	resp, err := fs.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(fs.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, mapS3Error(err, name)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	size := int64(len(data))
	modTime := aws.ToTime(resp.LastModified)

	return &s3File{
		fs:     fs,
		name:   name,
		key:    key,
		isDir:  false,
		reader: bytes.NewReader(data),
		fi:     &s3FileInfo{name: path.Base(name), size: size, modTime: modTime},
	}, nil
}

// RemoveAll deletes a file or all objects under a prefix.
func (fs *S3FileSystem) RemoveAll(ctx context.Context, name string) error {
	key := fs.keyFor(name)
	dirKey := key
	if !strings.HasSuffix(dirKey, "/") {
		dirKey += "/"
	}

	// Try to delete as file first.
	_, errFile := fs.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(fs.bucket),
		Key:    aws.String(key),
	})

	// Delete directory marker and all children.
	var toDelete []types.ObjectIdentifier
	paginator := s3.NewListObjectsV2Paginator(fs.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(fs.bucket),
		Prefix: aws.String(dirKey),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			toDelete = append(toDelete, types.ObjectIdentifier{Key: obj.Key})
		}
	}

	if len(toDelete) > 0 {
		_, err := fs.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(fs.bucket),
			Delete: &types.Delete{Objects: toDelete},
		})
		if err != nil {
			return err
		}
	}

	if errFile != nil && len(toDelete) == 0 {
		return mapS3Error(errFile, name)
	}
	return nil
}

// Rename copies src to dst then deletes src (S3 has no native rename).
func (fs *S3FileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldKey := fs.keyFor(oldName)
	newKey := fs.keyFor(newName)

	// Try file copy first.
	_, err := fs.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(fs.bucket),
		CopySource: aws.String(fs.bucket + "/" + oldKey),
		Key:        aws.String(newKey),
	})
	if err == nil {
		fs.client.DeleteObject(ctx, &s3.DeleteObjectInput{ //nolint:errcheck
			Bucket: aws.String(fs.bucket),
			Key:    aws.String(oldKey),
		})
		return nil
	}

	// Try directory rename: copy all objects with old prefix to new prefix.
	oldDirKey := oldKey + "/"
	newDirKey := newKey + "/"
	paginator := s3.NewListObjectsV2Paginator(fs.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(fs.bucket),
		Prefix: aws.String(oldDirKey),
	})
	var toDelete []types.ObjectIdentifier
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			src := aws.ToString(obj.Key)
			dst := newDirKey + strings.TrimPrefix(src, oldDirKey)
			_, err := fs.client.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket:     aws.String(fs.bucket),
				CopySource: aws.String(fs.bucket + "/" + src),
				Key:        aws.String(dst),
			})
			if err != nil {
				return err
			}
			toDelete = append(toDelete, types.ObjectIdentifier{Key: obj.Key})
		}
	}
	if len(toDelete) > 0 {
		_, err := fs.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(fs.bucket),
			Delete: &types.Delete{Objects: toDelete},
		})
		return err
	}
	return mapS3Error(err, oldName)
}

// Stat returns file info for the given path.
func (fs *S3FileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	key := fs.keyFor(name)

	// Root is always a directory.
	if key == fs.prefix {
		return &s3FileInfo{name: "/", isDir: true, modTime: time.Now()}, nil
	}

	// Try exact file key.
	resp, err := fs.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(fs.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return &s3FileInfo{
			name:    path.Base(name),
			size:    aws.ToInt64(resp.ContentLength),
			modTime: aws.ToTime(resp.LastModified),
		}, nil
	}

	// Try directory marker.
	dirKey := key + "/"
	_, err2 := fs.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(fs.bucket),
		Key:    aws.String(dirKey),
	})
	if err2 == nil {
		return &s3FileInfo{name: path.Base(name), isDir: true, modTime: time.Now()}, nil
	}

	// Check if there are any objects with this prefix (virtual directory).
	resp2, err3 := fs.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(fs.bucket),
		Prefix:  aws.String(dirKey),
		MaxKeys: aws.Int32(1),
	})
	if err3 == nil && len(resp2.Contents) > 0 {
		return &s3FileInfo{name: path.Base(name), isDir: true, modTime: time.Now()}, nil
	}

	return nil, &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

// ---- s3File ----------------------------------------------------------------

type s3File struct {
	fs         *S3FileSystem
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

func (f *s3File) Close() error {
	if f.write && f.pw != nil {
		// Signal EOF to the upload goroutine, wait for completion, and
		// surface its error (which propagates multipart-abort failures).
		_ = f.pw.Close()
		f.pw = nil
		return <-f.uploadDone
	}
	return nil
}

func (f *s3File) Read(p []byte) (int, error) {
	if f.isDir {
		return 0, fmt.Errorf("is a directory")
	}
	if f.reader == nil {
		return 0, io.EOF
	}
	return f.reader.Read(p)
}

func (f *s3File) Seek(offset int64, whence int) (int64, error) {
	if f.reader != nil {
		return f.reader.Seek(offset, whence)
	}
	return 0, fmt.Errorf("seek not supported on write-only file")
}

func (f *s3File) Write(p []byte) (int, error) {
	if !f.write || f.pw == nil {
		return 0, fmt.Errorf("not opened for writing")
	}
	return f.pw.Write(p)
}

func (f *s3File) Readdir(count int) ([]os.FileInfo, error) {
	if !f.isDir {
		return nil, fmt.Errorf("not a directory")
	}
	ctx := context.Background()
	prefix := f.key
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	var infos []os.FileInfo
	seen := map[string]bool{}

	paginator := s3.NewListObjectsV2Paginator(f.fs.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(f.fs.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		// Common prefixes are sub-directories.
		for _, cp := range page.CommonPrefixes {
			dirName := strings.TrimPrefix(aws.ToString(cp.Prefix), prefix)
			dirName = strings.TrimSuffix(dirName, "/")
			if dirName == "" || seen[dirName] {
				continue
			}
			seen[dirName] = true
			infos = append(infos, &s3FileInfo{name: dirName, isDir: true, modTime: time.Now()})
		}
		// Objects are files (skip the directory marker itself).
		for _, obj := range page.Contents {
			objKey := aws.ToString(obj.Key)
			if objKey == prefix {
				continue // directory marker
			}
			fileName := strings.TrimPrefix(objKey, prefix)
			if fileName == "" || seen[fileName] {
				continue
			}
			seen[fileName] = true
			infos = append(infos, &s3FileInfo{
				name:    fileName,
				size:    aws.ToInt64(obj.Size),
				modTime: aws.ToTime(obj.LastModified),
			})
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

func (f *s3File) Stat() (os.FileInfo, error) {
	if f.fi != nil {
		return f.fi, nil
	}
	if f.isDir {
		return &s3FileInfo{name: path.Base(f.name), isDir: true, modTime: time.Now()}, nil
	}
	return nil, fmt.Errorf("no file info available")
}

func (f *s3File) DeadProps() (map[xml.Name]webdav.Property, error) {
	return nil, nil
}

func (f *s3File) Patch(_ []webdav.Proppatch) ([]webdav.Propstat, error) {
	return nil, nil
}

// ---- s3FileInfo ------------------------------------------------------------

type s3FileInfo struct {
	name    string
	size    int64
	modTime time.Time
	isDir   bool
}

func (fi *s3FileInfo) Name() string      { return fi.name }
func (fi *s3FileInfo) Size() int64       { return fi.size }
func (fi *s3FileInfo) Mode() os.FileMode {
	if fi.isDir {
		return os.ModeDir | 0755
	}
	return 0644
}
func (fi *s3FileInfo) ModTime() time.Time { return fi.modTime }
func (fi *s3FileInfo) IsDir() bool        { return fi.isDir }
func (fi *s3FileInfo) Sys() interface{}   { return nil }

// ---- helpers ---------------------------------------------------------------

func mapS3Error(err error, name string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "NoSuchKey") || strings.Contains(msg, "NotFound") || strings.Contains(msg, "404") {
		return &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}
	return err
}
