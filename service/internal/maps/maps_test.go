package maps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Presigning is local (no network), so this checks the real policy document.
func TestPresignUpload_PolicyPinsTypeSizeTagAndCache(t *testing.T) {
	client := s3.New(s3.Options{
		Region:      "us-east-2",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
	})
	url, fields, err := New(client, "521studios-staging-encounter-builder-maps").PresignUpload(context.Background(), "maps/c/ch/x.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "521studios-staging-encounter-builder-maps.s3.us-east-2.amazonaws.com") {
		t.Fatalf("url = %q", url)
	}
	if fields["key"] != "maps/c/ch/x.png" || fields["Content-Type"] != "image/png" || fields["tagging"] != pendingTagging || fields["Cache-Control"] != cacheControl {
		t.Fatalf("form fields missing: %v", fields)
	}
	raw, err := base64.StdEncoding.DecodeString(fields["policy"])
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	tagged := false
	for _, c := range policy.Conditions {
		var m map[string]string
		if json.Unmarshal(c, &m) == nil && m["tagging"] == pendingTagging {
			tagged = true
		}
	}
	if !tagged {
		t.Errorf("policy does not pin the pending tag: %s", raw)
	}
	doc := string(raw)
	for _, want := range []string{
		`["content-length-range",1,15728640]`,
		`{"Content-Type":"image/png"}`,
		`{"Cache-Control":"public, max-age=31536000, immutable"}`,
		`{"key":"maps/c/ch/x.png"}`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("policy missing %s\n%s", want, doc)
		}
	}
}

// fakeS3 answers HEAD with 404 for "missing" and image/png otherwise, and records
// the tagging PUT body.
func fakeS3(t *testing.T) (*Store, *string) {
	t.Helper()
	var tagging string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/missing"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Type", "image/png")
		case r.Method == http.MethodPut && r.URL.Query().Has("tagging"):
			b := new(strings.Builder)
			_, _ = io.Copy(b, r.Body)
			tagging = b.String()
		default:
			t.Errorf("unexpected S3 call %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	client := s3.New(s3.Options{
		Region:       "us-east-2",
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
	})
	return New(client, "maps-bucket"), &tagging
}

func TestContentType_MapsMissingToErrNotFound(t *testing.T) {
	st, _ := fakeS3(t)
	if ct, err := st.ContentType(context.Background(), "maps/c/ch/x.png"); err != nil || ct != "image/png" {
		t.Fatalf("present = %q, %v", ct, err)
	}
	if _, err := st.ContentType(context.Background(), "maps/c/ch/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
}

func TestClearPending_SendsAnEmptyTagSet(t *testing.T) {
	st, tagging := fakeS3(t)
	if err := st.ClearPending(context.Background(), "maps/c/ch/x.png"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*tagging, "<TagSet></TagSet>") || strings.Contains(*tagging, "pending") {
		t.Fatalf("tagging body = %q, want an empty TagSet", *tagging)
	}
}
