//go:build race

package huddlesfu

import "time"

// Under the race detector the forwarding tests get a large budget: the detector
// multiplies every memory access by 5-20x, and `go test -race ./...` runs many
// package binaries concurrently, so a real ICE/DTLS/RTP handshake over loopback
// can be starved for far longer than an unraced run ever shows. Five minutes is
// well inside the package's own 30m test timeout, so a true hang still fails in
// bounded time; it only stops CPU starvation from reading as a lost track.
const forwardDeadline = 5 * time.Minute
