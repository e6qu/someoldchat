package blob

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDisabledFailsExplicitly(t *testing.T) {
	ctx := context.Background()
	store := Disabled{}

	if _, err := store.Put(ctx, "T1/file", 4, strings.NewReader("data")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Put error = %v, want ErrUnavailable", err)
	}
	if _, _, err := store.Open(ctx, "T1/file"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open error = %v, want ErrUnavailable", err)
	}
	if err := store.Delete(ctx, "T1/file"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Delete error = %v, want ErrUnavailable", err)
	}
}
