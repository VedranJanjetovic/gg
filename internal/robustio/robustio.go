// Package robustio provides file operations that tolerate the transient
// sharing failures Windows reports while another process — typically an
// antivirus scanner or the search indexer — briefly holds a handle to a file
// gg has just written.
package robustio

import "os"

// Rename replaces newpath with oldpath. On Windows it retries for a short
// bounded window when the operation fails only because a handle to either path
// is still open; on every other platform it is exactly os.Rename.
func Rename(oldpath, newpath string) error {
	return retry(func() error { return os.Rename(oldpath, newpath) })
}
