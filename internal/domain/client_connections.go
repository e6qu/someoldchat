package domain

import "time"

// A client connection is one open client of a member — a web client's event
// stream or an RTM socket — held as a lease its server renews while the
// client stays connected. A connection whose server vanished without closing
// it lapses when its lease does, so presence never outlives the client by more
// than one lease.

// ClientConnectionID names one open client connection.
type ClientConnectionID string

// ClientConnection is one open client of a member.
type ClientConnection struct {
	ID          ClientConnectionID
	WorkspaceID WorkspaceID
	UserID      UserID
	ExpiresAt   time.Time
}

// ClientConnectionLease is how long a connection counts without renewal; its
// server renews it every ClientConnectionRenewal.
const (
	ClientConnectionLease   = 60 * time.Second
	ClientConnectionRenewal = 20 * time.Second
)
