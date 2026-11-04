//go:build darwin

package darwin

import (
	"errors"
	"os"
	"unsafe"
)

// ReplaceFile replaces target with -[NSFileManager
// replaceItemAtURL:withItemAtURL:…], NSDocument's safe save: the target
// keeps its permissions, creation date, extended attributes (Finder tags,
// Finder info) and ACLs, and its identity, so that aliases still find it.
// It runs on the calling thread, which saves a document off the main
// thread: the URLs and the error are autoreleased in a pool of this
// thread.
func (*Backend) ReplaceFile(target, replacement string) error {
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return os.Rename(replacement, target)
	}
	load()
	var err error
	withPool(func() {
		fm := sendHere(class("NSFileManager"), "defaultManager")
		var nsErr id
		ok := byte(sendHere(fm, "replaceItemAtURL:withItemAtURL:backupItemName:options:resultingItemURL:error:",
			uintptr(fileURL(target)), uintptr(fileURL(replacement)), 0, 0, 0, uintptr(unsafe.Pointer(&nsErr)))) != 0
		if !ok {
			msg := "cannot replace the file"
			if nsErr != 0 {
				// An NSError is a CFError.
				desc := cfErrorCopyDescription(nsErr)
				msg = goString(id(desc))
				cfRelease(desc)
			}
			err = &os.LinkError{Op: "replace", Old: replacement, New: target, Err: errors.New(msg)}
		}
	})
	return err
}
