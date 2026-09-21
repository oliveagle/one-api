//go:build cgo

package rqlite

import (
	"database/sql/driver"
	"errors"
)

// driverImpl is the database/sql driver backing the embedded RQLite
// store. Open ignores the DSN (the store is process-global, installed
// by OpenStore) and returns one connection bound to that store.
type driverImpl struct{}

// ErrNoStore is returned when the driver is used before a store is open.
var ErrNoStore = errors.New("rqlite: no embedded store is open in this process")

func (driverImpl) Open(name string) (driver.Conn, error) {
	s := currentStore()
	if s == nil {
		return nil, ErrNoStore
	}
	return s.newConn(), nil
}

var _ driver.Driver = driverImpl{}
