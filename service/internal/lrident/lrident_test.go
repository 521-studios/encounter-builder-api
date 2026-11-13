package lrident

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const role = "arn:aws:iam::111122223333:role/ecs/lets-roll-staging-task"

var now = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

type stubSTS struct {
	arn  string
	got  *http.Request
	code int
}

func (s *stubSTS) Do(r *http.Request) (*http.Response, error) {
	s.got = r
	code := s.code
	if code == 0 {
		code = http.StatusOK
	}
	xml := `<GetCallerIdentityResponse><GetCallerIdentityResult><Arn>` + s.arn + `</Arn></GetCallerIdentityResult></GetCallerIdentityResponse>`
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(xml))}, nil
}

// sign builds an X-LR-Identity the way lets-roll would. mutate runs on the STS
// request before signing (signed) or after (unsigned) as the test needs.
func sign(t *testing.T, body []byte, before, after func(*http.Request)) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "https://sts.us-east-2.amazonaws.com/", strings.NewReader(stsBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sum := sha256.Sum256(body)
	req.Header.Set("X-LR-Audience", "encounter-builder-staging")
	req.Header.Set("X-LR-Body-SHA256", hex.EncodeToString(sum[:]))
	if before != nil {
		before(req)
	}
	bs := sha256.Sum256([]byte(stsBody))
	creds := aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret"}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, hex.EncodeToString(bs[:]), "sts", "us-east-2", now); err != nil {
		t.Fatal(err)
	}
	if after != nil {
		after(req)
	}
	h := req.Header.Clone()
	h.Set("Host", req.URL.Host)
	h.Set("Content-Length", strconv.Itoa(len(stsBody)))
	raw, _ := json.Marshal(identity{URL: req.URL.String(), Method: req.Method, Headers: h, Body: base64.StdEncoding.EncodeToString([]byte(stsBody))})
	return base64.StdEncoding.EncodeToString(raw)
}

func verifier(t *testing.T, sts *stubSTS) *Verifier {
	v, err := New(role, "staging", "us-east-2")
	if err != nil {
		t.Fatal(err)
	}
	v.http, v.now = sts, func() time.Time { return now.Add(time.Minute) }
	return v
}

func TestVerify(t *testing.T) {
	body := []byte(`{"kills":[]}`)
	good := "arn:aws:sts::111122223333:assumed-role/lets-roll-staging-task/abc123"
	sts := &stubSTS{arn: good}
	v := verifier(t, sts)
	if err := v.Verify(context.Background(), sign(t, body, nil, nil), body); err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	if sts.got.URL.String() != "https://sts.us-east-2.amazonaws.com/" || sts.got.Header.Get("Authorization") == "" {
		t.Fatalf("replayed %s %v", sts.got.URL, sts.got.Header)
	}

	cases := map[string]struct {
		header string
		body   []byte
		arn    string
	}{
		"other body":   {sign(t, body, nil, nil), []byte(`{"kills":[{}]}`), good},
		"audience":     {sign(t, body, func(r *http.Request) { r.Header.Set("X-LR-Audience", "lets-roll-staging") }, nil), body, good},
		"unsigned aud": {sign(t, body, func(r *http.Request) { r.Header.Del("X-LR-Audience") }, func(r *http.Request) { r.Header.Set("X-LR-Audience", "encounter-builder-staging") }), body, good},
		"other host":   {sign(t, body, nil, nil) /* rewritten below */, body, good},
		"other role":   {sign(t, body, nil, nil), body, "arn:aws:sts::111122223333:assumed-role/someone-else/abc"},
		"other acct":   {sign(t, body, nil, nil), body, "arn:aws:sts::999999999999:assumed-role/lets-roll-staging-task/abc"},
		"garbage":      {"not-base64!", body, good},
	}
	// Point the identity at another host: EB must refuse before replaying.
	raw, _ := base64.StdEncoding.DecodeString(cases["other host"].header)
	var id identity
	_ = json.Unmarshal(raw, &id)
	id.URL = "https://evil.example/"
	raw, _ = json.Marshal(id)
	c := cases["other host"]
	c.header = base64.StdEncoding.EncodeToString(raw)
	cases["other host"] = c

	for name, c := range cases {
		sts := &stubSTS{arn: c.arn}
		err := verifier(t, sts).Verify(context.Background(), c.header, c.body)
		if !errors.Is(err, ErrDenied) {
			t.Errorf("%s: err = %v, want denied", name, err)
		}
		if name == "other host" && sts.got != nil {
			t.Errorf("other host: replayed to %s", sts.got.URL)
		}
	}

	stale := verifier(t, &stubSTS{arn: good})
	stale.now = func() time.Time { return now.Add(10 * time.Minute) }
	if err := stale.Verify(context.Background(), sign(t, body, nil, nil), body); !errors.Is(err, ErrDenied) {
		t.Errorf("stale: err = %v, want denied", err)
	}
	if err := verifier(t, &stubSTS{arn: good, code: 403}).Verify(context.Background(), sign(t, body, nil, nil), body); !errors.Is(err, ErrDenied) {
		t.Errorf("sts 403: err = %v, want denied", err)
	}
	if err := verifier(t, &stubSTS{arn: good, code: 503}).Verify(context.Background(), sign(t, body, nil, nil), body); err == nil || errors.Is(err, ErrDenied) {
		t.Errorf("sts 503: err = %v, want a non-denied error", err)
	}
	// A duplicated audience is refused, and unsigned extras never reach STS.
	dup := sign(t, body, nil, func(r *http.Request) {
		r.Header.Add("X-LR-Audience", "lets-roll-staging")
	})
	if err := verifier(t, &stubSTS{arn: good}).Verify(context.Background(), dup, body); !errors.Is(err, ErrDenied) {
		t.Errorf("duplicate audience: err = %v, want denied", err)
	}
	sts = &stubSTS{arn: good}
	extra := sign(t, body, nil, func(r *http.Request) { r.Header.Set("Proxy-Authorization", "x") })
	if err := verifier(t, sts).Verify(context.Background(), extra, body); err != nil || sts.got.Header.Get("Proxy-Authorization") != "" {
		t.Errorf("unsigned extra header: err = %v, forwarded = %q", err, sts.got.Header.Get("Proxy-Authorization"))
	}
}

func TestNew_RejectsNonRoleARN(t *testing.T) {
	if _, err := New("arn:aws:iam::1:user/bob", "staging", "us-east-2"); err == nil {
		t.Fatal("user ARN accepted")
	}
}
