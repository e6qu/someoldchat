//go:build !race

package huddlesfu

import "time"

// Without the race detector the handshake completes in a second or two even on a
// loaded machine, so a tight bound keeps a genuine stall a fast failure. The
// race build (race_test.go) raises this because the detector's overhead starves
// the same handshake on shared CI.
const forwardDeadline = 60 * time.Second
