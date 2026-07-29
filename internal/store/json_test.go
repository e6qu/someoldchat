package store

import (
	"errors"
	"testing"
)

func TestMergeJSONObjectsRejectsTrailingValues(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		existing string
		patch    string
		invalid  bool
	}{
		"existing": {existing: `{"id":"1"} {"id":"2"}`, patch: `{"name":"Ada"}`},
		"patch":    {existing: `{"id":"1"}`, patch: `{"name":"Ada"} null`, invalid: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := MergeJSONObjects(test.existing, test.patch)
			if err == nil {
				t.Fatal("MergeJSONObjects accepted more than one JSON value")
			}
			if test.invalid && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestMergeJSONObjectsPreservesNumbersAndOverlaysFields(t *testing.T) {
	t.Parallel()

	got, err := MergeJSONObjects(`{"id":"1","count":9007199254740993}`, `{"name":"Ada"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"count":9007199254740993,"id":"1","name":"Ada"}` {
		t.Fatalf("merged = %s", got)
	}
}
