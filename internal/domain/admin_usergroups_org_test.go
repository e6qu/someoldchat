package domain

import (
	"errors"
	"slices"
	"testing"
)

func TestParseUserGroupCSV(t *testing.T) {
	rows, err := ParseUserGroupCSV("Member ID,Email\nU1, alice@example.com\n\n,bob@example.com\nU3\n")
	want := []UserGroupCSVRow{{UserID: "U1", Email: "alice@example.com"}, {Email: "bob@example.com"}, {UserID: "U3"}}
	if err != nil || !slices.Equal(rows, want) {
		t.Fatalf("rows %+v err=%v", rows, err)
	}
	for _, text := range []string{"U1,a,b", ",\n", "\"U1"} {
		if _, err := ParseUserGroupCSV(text); !errors.Is(err, ErrUnparseableUserGroupFile) {
			t.Fatalf("%q err=%v", text, err)
		}
	}
}
