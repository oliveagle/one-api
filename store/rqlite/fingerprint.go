//go:build cgo

package rqlite

// The clean_snapshot marker must match what rqlite's own
// createSnapshotFingerprint writes (store.FileFingerprint):
// ModTime as a time.Time JSON value, Size, CRC32, file name <dir>/clean_snapshot.
// A wrong fingerprint (or wrong file name) makes rqlite fall back to
// restoring the latest snapshot on boot - which for our incremental-only
// snapshots can be an EMPTY db. See TestCleanMarkerPreservesDB.
