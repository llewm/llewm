// Package privatestorage creates and verifies storage private to the current user.
// It never repairs permissions on an existing object.
package privatestorage

import "os"

// CheckFile verifies the opened object, not a second pathname lookup.
func CheckFile(f *os.File) error { return checkFile(f) }
