// Package remote persists checkpoints to an object store so a sandbox's
// history outlives the machine it ran on, and so another machine can pull a
// checkpoint and fork from it. The store is content addressed, so pushing is
// incremental: a blob already in the bucket is never uploaded again.
//
// The ObjectStore interface is small on purpose. vitvm ships an S3 client that
// speaks to AWS S3 and any S3-compatible service (MinIO, Cloudflare R2,
// Backblaze B2, Ceph), and a plain directory store for a shared filesystem.
package remote

import "errors"

// ObjectStore is a flat key to bytes store.
type ObjectStore interface {
	// Put writes bytes at key. Overwriting is fine; blobs are content
	// addressed so the bytes at a key never change.
	Put(key string, data []byte) error
	// Get reads the bytes at key, or ErrNotFound.
	Get(key string) ([]byte, error)
	// Has reports whether key exists, cheaply where the backend allows.
	Has(key string) (bool, error)
	// List returns keys under prefix.
	List(prefix string) ([]string, error)
}

// ErrNotFound is returned when a key is absent.
var ErrNotFound = errors.New("object not found")
