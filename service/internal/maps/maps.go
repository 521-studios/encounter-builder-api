// Package maps stores chapter battle-map images in S3 (muz0.1). The SPA uploads
// directly with a presigned POST. Each upload is tagged state=pending, and a
// bucket lifecycle rule expires pending objects, so abandoned uploads clean
// themselves up. Saving the chapter clears the tag.
package maps

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// MaxBytes caps one upload (enforced by the POST policy's content-length-range).
const MaxBytes = 15 << 20

// ErrNotFound is returned by ContentType when no object exists at the key.
var ErrNotFound = errors.New("maps: object not found")

const (
	pendingTagging = "<Tagging><TagSet><Tag><Key>state</Key><Value>pending</Value></Tag></TagSet></Tagging>"
	// Keys are unguessable and never rewritten, so the object can be cached forever.
	cacheControl = "public, max-age=31536000, immutable"
	uploadTTL    = 15 * time.Minute
)

// Store is the S3-backed map image store.
type Store struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func New(client *s3.Client, bucket string) *Store {
	return &Store{client: client, presign: s3.NewPresignClient(client), bucket: bucket}
}

// PresignUpload returns the URL and form fields for a browser POST of one image
// to key. The policy pins the content type, size cap, pending tag and cache header.
func (s *Store) PresignUpload(ctx context.Context, key, contentType string) (string, map[string]string, error) {
	req, err := s.presign.PresignPostObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignPostOptions) {
		o.Expires = uploadTTL
		o.Conditions = []any{
			[]any{"content-length-range", 1, MaxBytes},
			map[string]string{"Content-Type": contentType},
			map[string]string{"tagging": pendingTagging},
			map[string]string{"Cache-Control": cacheControl},
		}
	})
	if err != nil {
		return "", nil, err
	}
	fields := req.Values
	fields["Content-Type"] = contentType
	fields["tagging"] = pendingTagging
	fields["Cache-Control"] = cacheControl
	return req.URL, fields, nil
}

// ContentType reports the stored object's content type, or ErrNotFound.
func (s *Store) ContentType(ctx context.Context, key string) (string, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return aws.ToString(out.ContentType), nil
}

// ClearPending drops the state=pending tag so lifecycle keeps the object.
func (s *Store) ClearPending(ctx context.Context, key string) error {
	_, err := s.client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket:  aws.String(s.bucket),
		Key:     aws.String(key),
		Tagging: &types.Tagging{TagSet: []types.Tag{}},
	})
	return err
}
