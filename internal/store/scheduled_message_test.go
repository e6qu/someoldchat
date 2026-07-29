package store_test

import (
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestScheduledMessageLimitUsesARollingWindow(t *testing.T) {
	base := time.Unix(1_800_000_000, 0).UTC()
	existing := make([]time.Time, 0, 30)
	for index := 0; index < 30; index++ {
		// Straddle a wall-clock 5-minute boundary. A fixed-bucket count would
		// split this burst and incorrectly admit the 31st message.
		existing = append(existing, base.Add(-time.Minute+time.Duration(index)*time.Second))
	}
	if !store.ScheduledMessageLimitExceeded(existing, base.Add(time.Minute), 5*time.Minute, 30) {
		t.Fatal("31 messages spanning a clock-bucket boundary were accepted")
	}
	if store.ScheduledMessageLimitExceeded(existing, base.Add(5*time.Minute), 5*time.Minute, 30) {
		t.Fatal("messages outside any common rolling window were rejected")
	}
}
