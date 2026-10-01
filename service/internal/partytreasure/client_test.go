package partytreasure

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func treasureItem(id, gameID string, qty int, state model.TreasureState) model.ContentItem {
	return model.ContentItem{ID: id, Type: model.ContentTreasure, Treasure: &model.TreasureLine{
		Ref: model.ContentRef{GameID: gameID}, Qty: qty, State: state,
		Masked: true, MaskLabel: "glowing rod", IdentifyDC: 20, SaleClass: model.SalePureTreasure,
	}}
}

func TestReleasePayload_TreasureAndCoins(t *testing.T) {
	enc := model.Encounter{Content: []model.ContentItem{
		{ID: "md", Type: model.ContentMarkdown, Markdown: &model.TextBlock{Body: "prose"}},
		{ID: "mon", Type: model.ContentMonster, Monster: &model.MonsterEntry{Ref: model.ContentRef{GameID: "Monsters:1"}, Count: 2}},
		treasureItem("t1", "Weapons:1", 2, model.TreasureIntact),
		{ID: "coin1", Type: model.ContentCoin, Coin: &model.Currency{GP: 12, SP: 5}},
		{ID: "coin2", Type: model.ContentCoin, Coin: &model.Currency{GP: 8, PP: 1}},
		{ID: "xp", Type: model.ContentXPAward, XPAward: &model.XPAward{Amount: 30}},
	}}
	items, coins := ReleasePayloadFromEncounter(enc)

	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (monsters/prose/xp are not loot): %+v", len(items), items)
	}
	it := items[0]
	if it.Ref.GameID != "Weapons:1" || it.Qty != 2 || !it.Masked ||
		it.MaskLabel != "glowing rod" || it.IdentifyDC != 20 || it.SaleClass != model.SalePureTreasure {
		t.Fatalf("treasure not mapped: %+v", it)
	}
	if coins != (model.Currency{CP: 0, SP: 5, GP: 20, PP: 1}) {
		t.Fatalf("coins not summed: %+v", coins)
	}
}

func TestReleasePayload_SkipsBlankAndPrunedTreasure(t *testing.T) {
	enc := model.Encounter{Content: []model.ContentItem{
		{ID: "blank", Type: model.ContentTreasure, Treasure: &model.TreasureLine{Qty: 1}}, // no ref
		{ID: "nilpay", Type: model.ContentTreasure, Treasure: nil},                        // no payload
		treasureItem("gone", "Weapons:2", 1, model.TreasureDestroyed),                     // GM destroyed
		treasureItem("drunk", "Potions:1", 1, model.TreasureConsumed),                     // GM consumed
		treasureItem("zeroqty", "Weapons:4", 0, model.TreasureIntact),                     // qty 0 → skipped (would 400 the whole push)
		treasureItem("keep", "Weapons:3", 1, model.TreasureIntact),                        // the only survivor
		{ID: "zerocoin", Type: model.ContentCoin, Coin: &model.Currency{}},                // zero coins, harmless
		{ID: "nilcoin", Type: model.ContentCoin, Coin: nil},                               // nil coin payload → skipped
	}}
	items, coins := ReleasePayloadFromEncounter(enc)
	if len(items) != 1 || items[0].Ref.GameID != "Weapons:3" {
		t.Fatalf("expected only the intact item, got %+v", items)
	}
	if coins != (model.Currency{}) {
		t.Fatalf("coins = %+v, want zero", coins)
	}
}

func TestReleasePayload_EmptyContent(t *testing.T) {
	items, coins := ReleasePayloadFromEncounter(model.Encounter{})
	if len(items) != 0 || coins != (model.Currency{}) {
		t.Fatalf("empty content should yield no loot: items=%+v coins=%+v", items, coins)
	}
}

// --- light signing test: the Client builds a signed POST to the right URL ---

type fakeSigner struct{ called bool }

func (f *fakeSigner) SignHTTP(_ context.Context, _ aws.Credentials, r *http.Request,
	payloadHash, service, region string, _ time.Time, _ ...func(*v4.SignerOptions)) error {
	f.called = true
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 ...")
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	return nil
}

type fakeDoer struct {
	req    *http.Request
	status int
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	f.req = r
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader(""))}, nil
}

type fakeCreds struct{}

func (fakeCreds) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret"}, nil
}

func newTestClient(status int) (*Client, *fakeSigner, *fakeDoer) {
	sg := &fakeSigner{}
	dr := &fakeDoer{status: status}
	c := &Client{
		baseURL: "https://pt.example.com",
		http:    dr,
		signer:  sg,
		creds:   fakeCreds{},
		region:  "us-east-2",
	}
	return c, sg, dr
}

func TestClient_PushRelease_SignsAndPosts(t *testing.T) {
	c, sg, dr := newTestClient(http.StatusCreated)
	err := c.PushRelease(context.Background(), "g1", ReleaseInput{ClientULID: "enc1", Coins: model.Currency{GP: 5}})
	if err != nil {
		t.Fatalf("PushRelease: %v", err)
	}
	if !sg.called {
		t.Fatal("signer was not called")
	}
	if dr.req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", dr.req.Method)
	}
	if got := dr.req.URL.String(); got != "https://pt.example.com/api/party/parties/g1/releases" {
		t.Fatalf("url = %s", got)
	}
	if dr.req.Header.Get("X-Amz-Content-Sha256") == "" {
		t.Fatal("x-amz-content-sha256 not set by the signer")
	}
}

func TestClient_PushRelease_Non2xxIsError(t *testing.T) {
	c, _, _ := newTestClient(http.StatusForbidden)
	if err := c.PushRelease(context.Background(), "g1", ReleaseInput{ClientULID: "enc1"}); err == nil {
		t.Fatal("expected an error on 403")
	}
}
