//go:build !cgo

package rqlite

import (
	"database/sql/driver"
	"errors"
)

// ErrNoStore is returned when the driver is used without an open store.
var ErrNoStore = errors.New("rqlite: no embedded store is open (this binary was built without CGO, which RQLite requires: rebuild with CGO_ENABLED=1)")

// driverImpl is a stub for CGO_ENABLED=0 builds: the RQLite store links
// mattn/go-sqlite3 (CGO), so a pure-Go binary cannot run it. The driver
// still compiles (ADR-0004 D5) but fails fast with a clear error.
type driverImpl struct{}

func (driverImpl) Open(name string) (driver.Conn, error) {
	return nil, ErrNoStore
}

var _ driver.Driver = driverImpl{}
