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
	Variant    string           `json:"variant,omitempty"` // stat_block.variants name; "" = base item
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

// Receipt is what party-treasure recorded for a release: its ledger entry id
// (which is the client_ulid) and how many items landed in the pool.
type Receipt struct {
	LedgerID  string
	ItemCount int
}

// Releaser pushes a released encounter's loot into the party pool. The handler
// depends on this interface so tests can stub it; it is nil when the push is
// disabled (no PARTY_TREASURE_URL).
type Releaser interface {
	PushRelease(ctx context.Context, gameID string, in ReleaseInput) (Receipt, error)
}

// ReleasePayloadFromEncounter walks an encounter's content and extracts its loot:
// each treasure line becomes an ItemInput, each coin drop sums into coins. Non-loot
// content (monsters, skill checks, prose, pool headers, xp, rewards) is skipped, as
// are blank treasure lines (no ref) and lines the GM pruned post-encounter
// (consumed/destroyed). Pure.
func ReleasePayloadFromEncounter(enc model.Encounter) (items []ItemInput, coins model.Currency) {
	unfound := false // inside a pool the GM marked not found at close-out
	for _, c := range enc.Content {
		switch c.Type {
		case model.ContentPool:
			unfound = c.Pool != nil && c.Pool.NotFound
		case model.ContentTreasure:
			t := c.Treasure
			if t == nil || t.Ref.IsEmpty() {
				continue // a destroyed/blank treasure line points at nothing
			}
			qty := t.Moving() // the GM's close-out exclusions, whole or partial
			if unfound || qty < 1 {
				continue // nothing moves; a qty-0 item would also fail
				// party-treasure's qty>=1 validation and 400 the WHOLE push.
			}
			items = append(items, ItemInput{
				Ref:        t.Ref,
				Variant:    t.Variant,
				Name:       refName(t.Ref), // "" for pristine refs; party-treasure-web falls back to the pfsrd2 name
				Qty:        qty,
				Masked:     t.Masked,
				MaskLabel:  t.MaskLabel,
				IdentifyDC: t.IdentifyDC,
				SaleClass:  t.SaleClass,
			})
		case model.ContentCoin:
			if c.Coin == nil || unfound || c.Coin.Excluded() {
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

// refName is the item name a custom or derived ref carries in its json snapshot
// (ref.json.name). Pristine refs have only a game_id, so it's "" for them.
func refName(r model.ContentRef) string {
	var j struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(r.JSON, &j) // absent or non-object json → ""
	return strings.TrimSpace(j.Name)
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
// SigV4-signed as service "lambda". Non-2xx is an error. A replay of the same
// client_ulid answers 200 with the original release, so a retry is safe.
func (c *Client) PushRelease(ctx context.Context, gameID string, in ReleaseInput) (Receipt, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Receipt{}, fmt.Errorf("partytreasure: marshal: %w", err)
	}
	url := fmt.Sprintf("%s/api/party/parties/%s/releases", c.baseURL, gameID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Receipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	creds, err := c.creds.Retrieve(ctx)
	if err != nil {
		return Receipt{}, fmt.Errorf("partytreasure: retrieve creds: %w", err)
	}
	// A bodied request to an IAM Function URL must carry x-amz-content-sha256 AS A
	// SIGNED HEADER — the v4 SignHTTP does NOT add it (it only uses the payload hash
	// for the canonical request), so set it ourselves BEFORE signing so it's both
	// signed and sent. Without it the Function URL's SigV4 check 403s a bodied POST.
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", hash)
	if err := c.signer.SignHTTP(ctx, creds, req, hash, "lambda", c.region, time.Now()); err != nil {
		return Receipt{}, fmt.Errorf("partytreasure: sign: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Receipt{}, fmt.Errorf("partytreasure: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Receipt{}, fmt.Errorf("partytreasure: release returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	// party-treasure's releaseResult: {items: [...], ledger: {id, ...}}.
	var out struct {
		Items  []json.RawMessage `json:"items"`
		Ledger struct {
			ID string `json:"id"`
		} `json:"ledger"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return Receipt{}, fmt.Errorf("partytreasure: decode receipt: %w", err)
	}
	return Receipt{LedgerID: out.Ledger.ID, ItemCount: len(out.Items)}, nil
}
