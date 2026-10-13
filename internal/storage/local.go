package storage

import (
	"fmt"
	"os"

	"golang.org/x/net/webdav"
)

// NewLocal returns a webdav.FileSystem backed by the local filesystem at root.
// It uses the built-in webdav.Dir implementation.
func NewLocal(root string) (webdav.FileSystem, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("local: create data dir %s: %w", root, err)
	}
	return webdav.Dir(root), nil
}
