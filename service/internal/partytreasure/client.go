// Package partytreasure is a thin client for party-treasure-api's release
// endpoint. When a GM releases an encounter, encounter-builder-api best-effort
// pushes its loot (treasure + coins) into the party pool. party-treasure
// authorizes the call by the Lambda's SigV4 IAM identity (slice 5a) — no bearer
// is forwarded. A push failure never fails the release.
package partytreasure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// ItemInput mirrors party-treasure's model.ItemInput JSON. Ref reuses the
// encounter ContentRef so a released item ref serializes identically across the
// two services.
type ItemInput struct {
	Ref        model.ContentRef `json:"ref"`
	Name       string           `json:"name"`
	Qty        int              `json:"qty"`
	Masked     bool             `json:"masked"`
	MaskLabel  string           `json:"mask_label,omitempty"`
	IdentifyDC int              `json:"identify_dc,omitempty"`
	SaleClass  model.SaleClass  `json:"sale_class,omitempty"`
}

// ReleaseInput mirrors party-treasure's model.ReleaseInput JSON. ClientULID is
// the encounter id, so a re-release is idempotent on party-treasure's side.
type ReleaseInput struct {
	ClientULID string         `json:"client_ulid"`
	Items      []ItemInput    `json:"items,omitempty"`
	Coins      model.Currency `json:"coins"`
}

// Releaser pushes a released encounter's loot into the party pool. The handler
// depends on this interface so tests can stub it; it is nil when the push is
// disabled (no PARTY_TREASURE_URL).
type Releaser interface {
	PushRelease(ctx context.Context, gameID string, in ReleaseInput) error
}

// ReleasePayloadFromEncounter walks an encounter's content and extracts its loot:
// each treasure line becomes an ItemInput, each coin drop sums into coins. Non-loot
// content (monsters, skill checks, prose, pool headers, xp, rewards) is skipped, as
// are blank treasure lines (no ref) and lines the GM pruned post-encounter
// (consumed/destroyed). Pure.
func ReleasePayloadFromEncounter(enc model.Encounter) (items []ItemInput, coins model.Currency) {
	for _, c := range enc.Content {
		switch c.Type {
		case model.ContentTreasure:
			t := c.Treasure
			if t == nil || t.Ref.IsEmpty() {
				continue // a destroyed/blank treasure line points at nothing
			}
			if t.State == model.TreasureConsumed || t.State == model.TreasureDestroyed {
				continue // the GM pruned it; it never reaches the party
			}
			items = append(items, ItemInput{
				Ref:        t.Ref,
				Name:       "", // the API stores refs opaquely; party-treasure resolves the display name
				Qty:        t.Qty,
				Masked:     t.Masked,
				MaskLabel:  t.MaskLabel,
				IdentifyDC: t.IdentifyDC,
				SaleClass:  t.SaleClass,
			})
		case model.ContentCoin:
			if c.Coin == nil {
				continue
			}
			coins.CP += c.Coin.CP
			coins.SP += c.Coin.SP
			coins.GP += c.Coin.GP
			coins.PP += c.Coin.PP
		}
	}
	return items, coins
}

// httpDoer is the subset of *http.Client the client needs (stubbed in tests).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// signer is the subset of the SigV4 signer the client needs (stubbed in tests).
type signer interface {
	SignHTTP(ctx context.Context, credentials aws.Credentials, r *http.Request,
		payloadHash, service, region string, signingTime time.Time,
		optFns ...func(*v4.SignerOptions)) error
}

// Client is the real Releaser: it POSTs a SigV4-signed JSON release to
// party-treasure's AWS_IAM Function URL.
type Client struct {
	baseURL string
	http    httpDoer
	signer  signer
	creds   aws.CredentialsProvider
	region  string
}

// New builds a Client. baseURL is party-treasure's Function URL (trailing slash
// trimmed); creds/region come from the Lambda's default AWS config.
func New(baseURL string, creds aws.CredentialsProvider, region string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
		signer:  v4.NewSigner(),
		creds:   creds,
		region:  region,
	}
}

// PushRelease POSTs the release to {baseURL}/api/party/parties/{gameID}/releases,
// SigV4-signed as service "lambda". Non-2xx is an error. The required
// x-amz-content-sha256 header is set by the v4 signer from the payload hash.
func (c *Client) PushRelease(ctx context.Context, gameID string, in ReleaseInput) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("partytreasure: marshal: %w", err)
	}
	url := fmt.Sprintf("%s/api/party/parties/%s/releases", c.baseURL, gameID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	creds, err := c.creds.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("partytreasure: retrieve creds: %w", err)
	}
	// A bodied request to an IAM Function URL (and behind CloudFront OAC) must carry
	// x-amz-content-sha256; the v4 signer sets it from the payload hash we pass.
	sum := sha256.Sum256(body)
	if err := c.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "lambda", c.region, time.Now()); err != nil {
		return fmt.Errorf("partytreasure: sign: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("partytreasure: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("partytreasure: release returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
