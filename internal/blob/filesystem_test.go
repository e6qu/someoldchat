package blob

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
)

func TestFilesystemBlobStorePublishesAndReadsBoundedObjects(t *testing.T) {
	store, err := NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("blob bytes")
	object, err := store.Put(context.Background(), "workspace/file-1", int64(len(want)), bytes.NewReader(want))
	if err != nil || object.Size != int64(len(want)) {
		t.Fatalf("object=%+v err=%v", object, err)
	}
	gotObject, reader, err := store.Open(context.Background(), "workspace/file-1")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, want) || gotObject.Size != object.Size {
		t.Fatalf("got=%q object=%+v err=%v", got, gotObject, err)
	}
	if err := store.Delete(context.Background(), "workspace/file-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), "workspace/file-1"); err != ErrNotFound {
		t.Fatalf("missing blob err=%v", err)
	}
}

func TestFilesystemBlobStoreRejectsSizeMismatchAndUnsafeKeys(t *testing.T) {
	store, err := NewFilesystem(filepath.Join(t.TempDir(), "objects"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "../escape", 1, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("unsafe key accepted")
	}
	if _, err := store.Put(context.Background(), "file", 3, bytes.NewReader([]byte("too long"))); err == nil {
		t.Fatal("oversized source accepted")
	}
}
