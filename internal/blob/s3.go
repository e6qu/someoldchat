package blob

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type s3Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// ListStore extends Store with bounded object listing for manifest retention
// and provider-backed lifecycle metadata.
type ListStore interface {
	Store
	Walk(context.Context, string, func(Object) error) error
}

// S3 stores objects in an explicitly selected Amazon Simple Storage Service
// bucket. It never substitutes a filesystem store when the service is
// unavailable.
type S3 struct {
	client  s3Client
	bucket  string
	prefix  string
	maxSize int64
}

var errExactSize = errors.New("blob source size does not match the declared size")

var _ ListStore = S3{}

func NewS3(client *s3.Client, bucket, prefix string, maxSize int64) (S3, error) {
	if client == nil {
		return S3{}, errors.New("Amazon Simple Storage Service requires a client")
	}
	return newS3(client, bucket, prefix, maxSize)
}

func newS3(client s3Client, bucket, prefix string, maxSize int64) (S3, error) {
	if client == nil || strings.TrimSpace(bucket) == "" || maxSize <= 0 {
		return S3{}, errors.New("Amazon Simple Storage Service requires a client, bucket, and positive size limit")
	}
	prefix = strings.Trim(prefix, "/")
	return S3{client: client, bucket: bucket, prefix: prefix, maxSize: maxSize}, nil
}

func (s S3) Put(ctx context.Context, key string, size int64, source io.Reader) (Object, error) {
	if source == nil || size < 0 || size > s.maxSize {
		return Object{}, errors.New("blob source and bounded size are required")
	}
	objectKey, err := s.objectKey(key)
	if err != nil {
		return Object{}, err
	}
	reader := &exactSizeReader{source: source, remaining: size}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey), Body: reader})
	if err != nil {
		return Object{}, err
	}
	return Object{Key: key, Size: size}, nil
}

func (s S3) Open(ctx context.Context, key string) (Object, io.ReadCloser, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return Object{}, nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey)})
	if err != nil {
		if isS3NotFound(err) {
			return Object{}, nil, ErrNotFound
		}
		return Object{}, nil, err
	}
	if output == nil || output.Body == nil || output.ContentLength == nil || *output.ContentLength < 0 || *output.ContentLength > s.maxSize {
		invalidErr := errors.New("stored object has an invalid size")
		if output != nil && output.Body != nil {
			return Object{}, nil, errors.Join(invalidErr, output.Body.Close())
		}
		return Object{}, nil, invalidErr
	}
	return Object{Key: key, Size: *output.ContentLength}, output.Body, nil
}

func (s S3) Delete(ctx context.Context, key string) error {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey)})
	return err
}

// Walk visits listed objects page by page so callers can process a large
// provider listing without retaining the complete result set.
func (s S3) Walk(ctx context.Context, prefix string, visit func(Object) error) error {
	if visit == nil {
		return errors.New("object listing visitor is required")
	}
	objectPrefix := s.prefixWithSlash()
	if prefix != "" {
		var err error
		objectPrefix, err = s.objectKey(prefix)
		if err != nil {
			return err
		}
	}
	var token *string
	for {
		output, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(objectPrefix), ContinuationToken: token})
		if err != nil {
			return err
		}
		if output == nil {
			return errors.New("object listing returned an empty response")
		}
		for _, item := range output.Contents {
			if item.Key == nil || item.Size == nil || *item.Size < 0 || *item.Size > s.maxSize {
				return errors.New("object listing contains an invalid object")
			}
			key := strings.TrimPrefix(*item.Key, s.prefixWithSlash())
			if err := visit(Object{Key: key, Size: *item.Size}); err != nil {
				return err
			}
		}
		if !aws.ToBool(output.IsTruncated) {
			return nil
		}
		if output.NextContinuationToken == nil || *output.NextContinuationToken == "" {
			return errors.New("object listing is truncated without a continuation token")
		}
		token = output.NextContinuationToken
	}
}

func (s S3) objectKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return "", errors.New("unsafe blob key")
	}
	for _, part := range strings.Split(key, "/") {
		if part == ".." {
			return "", errors.New("unsafe blob key")
		}
	}
	if s.prefix == "" {
		return key, nil
	}
	return s.prefixWithSlash() + key, nil
}

func (s S3) prefixWithSlash() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

func isS3NotFound(err error) bool {
	var apiError smithy.APIError
	return errors.As(err, &apiError) && (apiError.ErrorCode() == "NoSuchKey" || apiError.ErrorCode() == "NotFound")
}

type exactSizeReader struct {
	source    io.Reader
	remaining int64
	checked   bool
}

func (r *exactSizeReader) Read(p []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(p)) > r.remaining {
			p = p[:r.remaining]
		}
		count, err := r.source.Read(p)
		r.remaining -= int64(count)
		if err == io.EOF && r.remaining > 0 {
			return count, errExactSize
		}
		return count, err
	}
	if r.checked {
		return 0, io.EOF
	}
	r.checked = true
	var extra [1]byte
	count, err := r.source.Read(extra[:])
	if count > 0 {
		return 0, errExactSize
	}
	return 0, err
}
