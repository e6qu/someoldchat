package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type fakeS3Client struct {
	objects  map[string][]byte
	getError error
}

func (f *fakeS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	f.objects[aws.ToString(input.Key)] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.getError != nil {
		return nil, f.getError
	}
	body, ok := f.objects[aws.ToString(input.Key)]
	if !ok {
		return nil, io.EOF
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: aws.Int64(int64(len(body)))}, nil
}

func (f *fakeS3Client) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3Client) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	contents := make([]types.Object, 0)
	for key, body := range f.objects {
		if len(aws.ToString(input.Prefix)) <= len(key) && key[:len(aws.ToString(input.Prefix))] == aws.ToString(input.Prefix) {
			contents = append(contents, types.Object{Key: aws.String(key), Size: aws.Int64(int64(len(body)))})
		}
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}

func TestS3StoresBoundedObjectsAndListsByPrefix(t *testing.T) {
	client := &fakeS3Client{objects: make(map[string][]byte)}
	store, err := newS3(client, "bucket", "snapshots", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("snapshot")
	object, err := store.Put(context.Background(), "artifacts/1", int64(len(want)), bytes.NewReader(want))
	if err != nil || object.Key != "artifacts/1" || object.Size != int64(len(want)) {
		t.Fatalf("put object=%+v err=%v", object, err)
	}
	gotObject, reader, err := store.Open(context.Background(), "artifacts/1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || gotObject.Size != int64(len(want)) || !bytes.Equal(got, want) {
		t.Fatalf("got=%q object=%+v err=%v close=%v", got, gotObject, err, closeErr)
	}
	objects, err := store.List(context.Background(), "artifacts")
	if err != nil || len(objects) != 1 || objects[0].Key != "artifacts/1" {
		t.Fatalf("objects=%+v err=%v", objects, err)
	}
	if err := store.Delete(context.Background(), "artifacts/1"); err != nil {
		t.Fatal(err)
	}
}

func TestS3RejectsUnsafeKeysAndOversizedObjects(t *testing.T) {
	client := &fakeS3Client{objects: make(map[string][]byte)}
	store, err := newS3(client, "bucket", "snapshots", 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "/absolute", "../escape", "nested\\escape"} {
		if _, err := store.Put(context.Background(), key, 1, bytes.NewReader([]byte("x"))); err == nil {
			t.Fatalf("unsafe key %q was accepted", key)
		}
	}
	if _, err := store.Put(context.Background(), "large", 4, bytes.NewReader([]byte("four"))); err == nil {
		t.Fatal("oversized object was accepted")
	}
	if _, err := store.Put(context.Background(), "too-long", 3, bytes.NewReader([]byte("four"))); err == nil {
		t.Fatal("object with extra source bytes was accepted")
	}
}

func TestS3MapsProviderNotFoundToBlobError(t *testing.T) {
	client := &fakeS3Client{objects: make(map[string][]byte), getError: &smithy.GenericAPIError{Code: "NoSuchKey"}}
	store, err := newS3(client, "bucket", "snapshots", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open error = %v, want ErrNotFound", err)
	}
}
