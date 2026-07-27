package domain

import (
	"sort"
	"testing"
	"time"
)

// orderingInstants deliberately straddles the edges of the representable range.
// The previous coverage of this invariant used eight instants inside a single
// year, which is why it could not see that year 10000 encoded to 31 bytes and
// sorted before 1969, or that -4000 sorted after -0001.
func orderingInstants() []time.Time {
	return []time.Time{
		time.Date(-4000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC),
		time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 50000000, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 100000000, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 120000000, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 123456000, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 200000000, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999998, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(36812, 2, 20, 0, 0, 0, 0, time.UTC),
	}
}

// TestStoredTimeIsAlwaysOrdered is the type's whole reason to exist: whatever it
// is handed, the value it returns is fixed width, single zone, and comparable
// byte-for-byte against every other value it returns.
func TestStoredTimeIsAlwaysOrdered(t *testing.T) {
	for _, instant := range orderingInstants() {
		encoded := NewStoredTime(instant)
		if !encoded.Ordered() {
			t.Fatalf("NewStoredTime(%s) = %q, which is not order preserving", instant, string(encoded))
		}
		if len(encoded) != StoredTimeWidth {
			t.Fatalf("NewStoredTime(%s) = %q has width %d, want %d", instant, string(encoded), len(encoded), StoredTimeWidth)
		}
	}
}

// TestStoredTimeByteOrderMatchesTimeOrderAcrossTheRange sorts by encoding and by
// instant and requires the two orders to agree. Saturation makes out-of-range
// instants compare EQUAL to the bound they saturate at, never reversed, so the
// comparison is on the saturated instants rather than the originals.
func TestStoredTimeByteOrderMatchesTimeOrderAcrossTheRange(t *testing.T) {
	instants := orderingInstants()
	for left := range instants {
		for right := range instants {
			leftText := string(NewStoredTime(instants[left]))
			rightText := string(NewStoredTime(instants[right]))
			leftSaturated := saturateForTest(instants[left])
			rightSaturated := saturateForTest(instants[right])
			if (leftText < rightText) != leftSaturated.Before(rightSaturated) {
				t.Fatalf("byte order of %q vs %q disagrees with time order of %s vs %s", leftText, rightText, leftSaturated, rightSaturated)
			}
			if (leftText == rightText) != leftSaturated.Equal(rightSaturated) {
				t.Fatalf("byte equality of %q vs %q disagrees with time equality of %s vs %s", leftText, rightText, leftSaturated, rightSaturated)
			}
		}
	}

	type pair struct {
		encoded   string
		saturated time.Time
	}
	pairs := make([]pair, 0, len(instants))
	for _, instant := range instants {
		pairs = append(pairs, pair{encoded: string(NewStoredTime(instant)), saturated: saturateForTest(instant)})
	}
	byEncoding := append([]pair(nil), pairs...)
	sort.SliceStable(byEncoding, func(i, j int) bool { return byEncoding[i].encoded < byEncoding[j].encoded })
	byInstant := append([]pair(nil), pairs...)
	sort.SliceStable(byInstant, func(i, j int) bool { return byInstant[i].saturated.Before(byInstant[j].saturated) })
	for index := range byEncoding {
		if byEncoding[index].encoded != byInstant[index].encoded {
			t.Fatalf("position %d sorts to %q by encoding and to %q by instant", index, byEncoding[index].encoded, byInstant[index].encoded)
		}
	}
}

// TestStoredTimeSaturatesOutsideTheRepresentableRange pins the enforcement, not
// just its consequence.
func TestStoredTimeSaturatesOutsideTheRepresentableRange(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		instant time.Time
		want    string
	}{
		{"below the floor", time.Date(-4000, 1, 1, 0, 0, 0, 0, time.UTC), "0001-01-01T00:00:00.000000000Z"},
		{"year zero", time.Date(0, 6, 1, 0, 0, 0, 0, time.UTC), "0001-01-01T00:00:00.000000000Z"},
		{"above the ceiling", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "9999-12-31T23:59:59.999999999Z"},
		{"first representable", time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC), "0001-01-01T00:00:00.000000001Z"},
		{"last representable", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), "9999-12-31T23:59:59.999999999Z"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := string(NewStoredTime(testCase.instant)); got != testCase.want {
				t.Fatalf("NewStoredTime(%s) = %q, want %q", testCase.instant, got, testCase.want)
			}
		})
	}
	// The zero time keeps its own meaning: the "unset" marker, not the floor.
	if got := NewStoredTime(time.Time{}); got != "" {
		t.Fatalf("NewStoredTime(zero) = %q, want the unset marker", string(got))
	}
}

// TestStoredTimeOrderedRejectsAZonedValueOfTheRightWidth pins the half of the
// check that was missing: a legacy value with a numeric offset is exactly
// StoredTimeWidth bytes and is not comparable with the 'Z' values around it.
func TestStoredTimeOrderedRejectsAZonedValueOfTheRightWidth(t *testing.T) {
	zoned := StoredTime("2026-07-27T10:00:00.1234+01:00")
	if len(zoned) != StoredTimeWidth {
		t.Fatalf("fixture width %d, want %d so the width check alone cannot reject it", len(zoned), StoredTimeWidth)
	}
	if zoned.Ordered() {
		t.Fatalf("%q reports ordered; a numeric offset is not comparable against the Z form", string(zoned))
	}
	// It still decodes, because databases in the field contain it.
	if _, err := zoned.Time(); err != nil {
		t.Fatalf("a legacy zoned value must still decode: %v", err)
	}
}

func saturateForTest(value time.Time) time.Time {
	value = value.UTC()
	switch {
	case value.Before(time.Date(StoredTimeMinYear, time.January, 1, 0, 0, 0, 0, time.UTC)):
		return time.Date(StoredTimeMinYear, time.January, 1, 0, 0, 0, 0, time.UTC)
	case value.After(time.Date(StoredTimeMaxYear, time.December, 31, 23, 59, 59, 999999999, time.UTC)):
		return time.Date(StoredTimeMaxYear, time.December, 31, 23, 59, 59, 999999999, time.UTC)
	default:
		return value
	}
}

// FuzzStoredTimeOrderIsChronological is the general form of the invariant: for
// any two instants a driver can name, the byte order of their encodings equals
// the chronological order of the instants those encodings denote. Round-tripping
// the encoding before comparing is what makes saturation and truncation
// participate rather than being special-cased.
func FuzzStoredTimeOrderIsChronological(f *testing.F) {
	f.Add(int64(0), int64(0), int64(0), int64(0))
	f.Add(int64(1700000000), int64(120000000), int64(1700000000), int64(123456000))
	f.Add(int64(-62135596800), int64(0), int64(253402300799), int64(999999999))
	f.Add(int64(-3000000000000), int64(0), int64(3000000000000), int64(0))
	f.Fuzz(func(t *testing.T, leftSeconds, leftNanos, rightSeconds, rightNanos int64) {
		left := time.Unix(leftSeconds, leftNanos).UTC()
		right := time.Unix(rightSeconds, rightNanos).UTC()
		leftText, rightText := NewStoredTime(left), NewStoredTime(right)
		if !leftText.Ordered() || !rightText.Ordered() {
			t.Fatalf("NewStoredTime produced a non-ordered encoding: %q, %q", string(leftText), string(rightText))
		}
		if leftText == "" || rightText == "" {
			return
		}
		leftDecoded, err := leftText.Time()
		if err != nil {
			t.Fatalf("decode %q: %v", string(leftText), err)
		}
		rightDecoded, err := rightText.Time()
		if err != nil {
			t.Fatalf("decode %q: %v", string(rightText), err)
		}
		if (leftText < rightText) != leftDecoded.Before(rightDecoded) {
			t.Fatalf("byte order of %q vs %q disagrees with the order of the instants they denote (%s vs %s)", string(leftText), string(rightText), leftDecoded, rightDecoded)
		}
		if (leftText == rightText) != leftDecoded.Equal(rightDecoded) {
			t.Fatalf("byte equality of %q vs %q disagrees with instant equality", string(leftText), string(rightText))
		}
	})
}
