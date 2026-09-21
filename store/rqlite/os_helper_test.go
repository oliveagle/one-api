//go:build cgo

package rqlite

import "os"

// osReadFile is a thin indirection so the recovery tests stay in one file.
func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
