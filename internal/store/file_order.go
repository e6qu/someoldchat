package store

import (
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A workspace's file collection is read newest first — created_at DESC, id
// DESC — which is the order Slack's files.list returns and the only one a
// person browsing files expects. It used to be ascending by id, and ids are
// random, so the "first page" was an arbitrary sample of the collection.
//
// Every repository shares this order and this cursor so a page boundary means
// the same thing on every storage profile.

// FileCursor is the keyset position after which a newest-first file read
// resumes. The zero value is the start of the collection.
type FileCursor struct {
	CreatedAt time.Time
	ID        domain.FileID
}

// IsZero reports the start of the collection.
func (c FileCursor) IsZero() bool {
	return c.ID == ""
}

// Precedes reports whether file comes after this position in newest-first
// order, and so belongs to the page this cursor opens.
func (c FileCursor) Precedes(file domain.File) bool {
	if c.IsZero() {
		return true
	}
	created := file.CreatedAt.UTC()
	if created.Equal(c.CreatedAt) {
		return file.ID < c.ID
	}
	return created.Before(c.CreatedAt)
}

// NewerFileFirst is the newest-first order as a less function.
func NewerFileFirst(left, right domain.File) bool {
	if left.CreatedAt.Equal(right.CreatedAt) {
		return left.ID > right.ID
	}
	return left.CreatedAt.After(right.CreatedAt)
}

// NewFileCursor is the position just after file.
func NewFileCursor(file domain.File) (domain.Cursor, error) {
	if file.CreatedAt.IsZero() {
		return "", domain.ErrInvalidCursor
	}
	return domain.NewPairCursor(string(domain.NewStoredTime(file.CreatedAt)), string(file.ID))
}

// DecodeFileCursor reads a cursor NewFileCursor minted.
func DecodeFileCursor(cursor domain.Cursor) (FileCursor, error) {
	pair, err := domain.DecodePairCursor(cursor)
	if err != nil || cursor == "" {
		return FileCursor{}, err
	}
	created, err := domain.StoredTime(pair.First).Time()
	if err != nil || created.IsZero() {
		return FileCursor{}, domain.ErrInvalidCursor
	}
	return FileCursor{CreatedAt: created, ID: domain.FileID(pair.Second)}, nil
}
