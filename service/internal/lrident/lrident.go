// Package lrident verifies lets-roll's service identity on its server-to-server
// calls into EB (kill reports, we5k.2.4; the .2.2 pull later). It is the mirror
// of internal/combatpush: lets-roll's ECS task role signs an
// sts:GetCallerIdentity request and sends it base64-JSON in X-LR-Identity. The
// signed headers bind it to EB's audience and to the SHA-256 of the request
// body. EB checks all of that, replays the request to STS, and requires the
// returned role to be LETS_ROLL_TASK_ROLE_ARN.
package lrident

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Header carries the signed STS request.
const Header = "X-LR-Identity"

const (
	stsBody  = "Action=GetCallerIdentity&Version=2011-06-15"
	maxSkew  = 5 * time.Minute // tighter than STS's own 15 minutes
	amzDate  = "20060102T150405Z"
	audHdr   = "X-Lr-Audience"
	bodyHdr  = "X-Lr-Body-Sha256"
	dateHdr  = "X-Amz-Date"
	authzHdr = "Authorization"
)

type identity struct {
	URL     string              `json:"url"`
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"` // base64
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Verifier checks X-LR-Identity.
type Verifier struct {
	stsURL   string
	audience string // "encounter-builder-<env>"
	account  string // from the expected role ARN
	roleName string
	http     httpDoer
	now      func() time.Time
}

// New builds a Verifier for the IAM role ARN lets-roll's task runs as
// (arn:aws:iam::<account>:role[/path]/<name>), in EB's env and region.
func New(roleARN, env, region string) (*Verifier, error) {
	parts := strings.SplitN(roleARN, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "iam" || !strings.HasPrefix(parts[5], "role/") {
		return nil, fmt.Errorf("lrident: %q is not an IAM role ARN", roleARN)
	}
	path := strings.Split(parts[5], "/")
	return &Verifier{
		stsURL:   fmt.Sprintf("https://sts.%s.amazonaws.com/", region),
		audience: "encounter-builder-" + env,
		account:  parts[4],
		roleName: path[len(path)-1],
		http:     &http.Client{Timeout: 5 * time.Second},
		now:      time.Now,
	}, nil
}

// ErrDenied is any identity failure; the caller answers 401 without detail.
var ErrDenied = errors.New("lrident: denied")

func deny(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrDenied}, a...)...)
}

// Verify checks the identity header against body (the raw request bytes).
func (v *Verifier) Verify(ctx context.Context, header string, body []byte) error {
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return deny("header not base64")
	}
	var id identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return deny("header not JSON")
	}
	stsReqBody, err := base64.StdEncoding.DecodeString(id.Body)
	if err != nil || string(stsReqBody) != stsBody {
		return deny("not a GetCallerIdentity request")
	}
	if id.URL != v.stsURL || id.Method != http.MethodPost {
		return deny("not the STS endpoint")
	}
	h := http.Header{}
	for k, vs := range id.Headers { // canonicalize keys from the JSON map
		h[http.CanonicalHeaderKey(k)] = vs
	}
	for _, k := range []string{authzHdr, audHdr, bodyHdr, dateHdr} {
		if len(h[k]) != 1 {
			return deny("%s must appear exactly once", k)
		}
	}
	signed, err := signedHeaders(h.Get(authzHdr))
	if err != nil {
		return err
	}
	for _, need := range []string{"host", "x-amz-date", "x-lr-audience", "x-lr-body-sha256"} {
		if !slices.Contains(signed, need) {
			return deny("%s is not signed", need)
		}
	}
	if h.Get(audHdr) != v.audience {
		return deny("wrong audience")
	}
	sum := sha256.Sum256(body)
	if !strings.EqualFold(h.Get(bodyHdr), hex.EncodeToString(sum[:])) {
		return deny("body hash mismatch")
	}
	at, err := time.Parse(amzDate, h.Get(dateHdr))
	if err != nil || v.now().Sub(at).Abs() > maxSkew {
		return deny("stale or missing X-Amz-Date")
	}
	arn, err := v.replay(ctx, h, signed)
	if err != nil {
		return err
	}
	return v.checkARN(arn)
}

// signedHeaders pulls the SignedHeaders list out of a SigV4 Authorization header.
func signedHeaders(authz string) ([]string, error) {
	if !strings.HasPrefix(authz, "AWS4-HMAC-SHA256 ") {
		return nil, deny("not SigV4")
	}
	for _, part := range strings.Split(strings.TrimPrefix(authz, "AWS4-HMAC-SHA256 "), ",") {
		if s, ok := strings.CutPrefix(strings.TrimSpace(part), "SignedHeaders="); ok {
			return strings.Split(s, ";"), nil
		}
	}
	return nil, deny("no SignedHeaders")
}

type getCallerIdentityResponse struct {
	Arn string `xml:"GetCallerIdentityResult>Arn"`
}

// replay sends the signed request to STS (never anywhere else: the URL was
// checked against our own regional endpoint) and returns the caller's ARN.
// Only the signed headers (plus Authorization) are forwarded.
func (v *Verifier) replay(ctx context.Context, h http.Header, signed []string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.stsURL, bytes.NewReader([]byte(stsBody)))
	if err != nil {
		return "", err
	}
	for _, k := range append(signed, authzHdr) {
		k = http.CanonicalHeaderKey(k)
		if k == "Host" || k == "Content-Length" {
			continue // Go sets both from the URL and body, which were checked above
		}
		req.Header[k] = h[k]
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("lrident: sts: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return "", fmt.Errorf("lrident: sts unavailable (%d)", resp.StatusCode) // 502, so lets-roll retries
	case resp.StatusCode != http.StatusOK:
		return "", deny("sts returned %d", resp.StatusCode)
	}
	var out getCallerIdentityResponse
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", fmt.Errorf("lrident: sts decode: %w", err)
	}
	return out.Arn, nil
}

// checkARN accepts arn:aws:sts::<account>:assumed-role/<name>/<session> for the
// expected role (ECS task roles always arrive as assumed-role sessions).
func (v *Verifier) checkARN(arn string) error {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[2] != "sts" || parts[4] != v.account {
		return deny("caller %q is not lets-roll", arn)
	}
	seg := strings.Split(parts[5], "/")
	if len(seg) != 3 || seg[0] != "assumed-role" || seg[1] != v.roleName {
		return deny("caller %q is not lets-roll", arn)
	}
	return nil
}

// Middleware admits only requests carrying a valid lets-roll identity. It reads
// the body (1 MiB cap) to hash it and hands the same bytes to the handler.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, `{"error":"body too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		if err := v.Verify(r.Context(), r.Header.Get(Header), body); err != nil {
			slog.WarnContext(r.Context(), "lets-roll identity rejected", "path", r.URL.Path, "error", err)
			if !errors.Is(err, ErrDenied) {
				http.Error(w, `{"error":"could not verify caller"}`, http.StatusBadGateway)
				return
			}
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
