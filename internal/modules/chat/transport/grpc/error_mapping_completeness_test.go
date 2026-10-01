package grpc

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A validation error describes a caller mistake. codes.Unavailable tells the
// caller to retry, which a malformed request will never survive, so every
// ErrInvalid* sentinel must map to codes.InvalidArgument.
//
// The sentinel is resolved through errorClasses, not through a second
// hand-written name→sentinel map. That map had 33 entries that had to be kept in
// step with the table by hand — the exact duplication the single table was
// introduced to delete — and a sentinel missing from it failed the test for
// bookkeeping reasons rather than for a defect. The scan covers every package
// in sentinelRoots: it used to read only internal/service, so a validation
// sentinel declared anywhere else escaped it.
func TestMapErrorClassifiesEveryValidationErrorAsInvalidArgument(t *testing.T) {
	table := tableSentinelKeys(t)
	checked := 0
	for qualified, name := range discoveredSentinels(t) {
		if !strings.HasPrefix(name, "ErrInvalid") {
			continue
		}
		if _, excluded := unclassifiedSentinels[qualified]; excluded {
			continue
		}
		if _, internal := corruptionSentinels[qualified]; internal {
			continue
		}
		checked++
		t.Run(qualified, func(t *testing.T) {
			class, classified := errorClassesByKey[table[qualified]]
			if !classified {
				t.Fatalf("%s has no class; TestEveryDomainSentinelIsClassified says which table entry is missing", qualified)
			}
			got := status.Code(mapError(fmt.Errorf("wrapped: %w", class.sentinel)))
			if got != codes.InvalidArgument {
				t.Fatalf("mapError(%s) = %s, want %s", qualified, got, codes.InvalidArgument)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no validation errors discovered; the source scan is broken")
	}
}

// corruptionSentinels are named ErrInvalid* but report state this system wrote
// and can no longer read, not anything the caller sent, so they are
// codes.Internal rather than codes.InvalidArgument.
var corruptionSentinels = map[string]string{
	"domain.ErrInvalidStoredTimestamp": "a stored timestamp that cannot be decoded",
}
