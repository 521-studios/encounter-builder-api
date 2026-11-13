package partytreasure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		Variant: "Striking (Greater)",
	}}
}

func TestReleasePayload_TreasureAndCoins(t *testing.T) {
	enc := model.Encounter{Content: []model.ContentItem{
		{ID: "md", Type: model.ContentMarkdown, Markdown: &model.TextBlock{Body: "prose"}},
		{ID: "mon", Type: model.ContentMonster, Monster: &model.MonsterEntry{Ref: model.ContentRef{GameID: "Monsters:1"}, Count: 2}},
		treasureItem("t1", "Weapons:1", 2, model.TreasureIntact),
		{ID: "coin1", Type: model.ContentCoin, Coin: &model.CoinDrop{Currency: model.Currency{GP: 12, SP: 5}}},
		{ID: "coin2", Type: model.ContentCoin, Coin: &model.CoinDrop{Currency: model.Currency{GP: 8, PP: 1}}},
		{ID: "xp", Type: model.ContentXPAward, XPAward: &model.XPAward{Amount: 30}},
	}}
	items, coins := ReleasePayloadFromEncounter(enc)

	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (monsters/prose/xp are not loot): %+v", len(items), items)
	}
	it := items[0]
	if it.Ref.GameID != "Weapons:1" || it.Qty != 2 || !it.Masked ||
		it.MaskLabel != "glowing rod" || it.IdentifyDC != 20 || it.SaleClass != model.SalePureTreasure ||
		it.Variant != "Striking (Greater)" {
		t.Fatalf("treasure not mapped: %+v", it)
	}
	if coins != (model.Currency{CP: 0, SP: 5, GP: 20, PP: 1}) {
		t.Fatalf("coins not summed: %+v", coins)
	}
}

// A custom/derived ref's json name is sent; a pristine ref (game_id only) sends "".
func TestReleasePayload_NameFromRefJSON(t *testing.T) {
	withJSON := func(id, j string) model.ContentItem {
		c := treasureItem(id, "", 1, model.TreasureIntact)
		c.Treasure.Ref = model.ContentRef{Base: &model.ContentRef{GameID: "Weapons:1"}, JSON: json.RawMessage(j)}
		return c
	}
	enc := model.Encounter{Content: []model.ContentItem{
		withJSON("t1", `{"name":" Sunblade "}`),
		treasureItem("t2", "Weapons:2", 1, model.TreasureIntact), // pristine
		withJSON("t3", `["not","an","object"]`),
		withJSON("t4", `{"name":"   "}`),
	}}
	items, _ := ReleasePayloadFromEncounter(enc)
	want := []string{"Sunblade", "", "", ""}
	if len(items) != len(want) {
		t.Fatalf("items = %+v, want %d", items, len(want))
	}
	for i, w := range want {
		if items[i].Name != w {
			t.Errorf("item %d name = %q, want %q", i, items[i].Name, w)
		}
	}
}

func TestReleasePayload_SkipsBlankAndPrunedTreasure(t *testing.T) {
	enc := model.Encounter{Content: []model.ContentItem{
		{ID: "blank", Type: model.ContentTreasure, Treasure: &model.TreasureLine{Qty: 1}},            // no ref
		{ID: "nilpay", Type: model.ContentTreasure, Treasure: nil},                                   // no payload
		treasureItem("gone", "Weapons:2", 1, model.TreasureDestroyed),                                // GM destroyed
		treasureItem("drunk", "Potions:1", 1, model.TreasureConsumed),                                // GM consumed
		treasureItem("zeroqty", "Weapons:4", 0, model.TreasureIntact),                                // qty 0 → skipped (would 400 the whole push)
		treasureItem("keep", "Weapons:3", 1, model.TreasureIntact),                                   // the only survivor
		{ID: "zerocoin", Type: model.ContentCoin, Coin: &model.CoinDrop{Currency: model.Currency{}}}, // zero coins, harmless
		{ID: "nilcoin", Type: model.ContentCoin, Coin: nil},                                          // nil coin payload → skipped
	}}
	items, coins := ReleasePayloadFromEncounter(enc)
	if len(items) != 1 || items[0].Ref.GameID != "Weapons:3" {
		t.Fatalf("expected only the intact item, got %+v", items)
	}
	if coins != (model.Currency{}) {
		t.Fatalf("coins = %+v, want zero", coins)
	}
}

// we5k.2.5 close-out: partial qty, left-behind lines, excluded coins, and a gated
// pool the party never found (its loot + coins skipped until the next pool).
func TestReleasePayload_CloseOutExclusions(t *testing.T) {
	ptr := func(n int) *int { return &n }
	darts := treasureItem("darts", "Weapons:5", 30, model.TreasureDestroyed)
	darts.Treasure.QtyMoving = ptr(18) // 12 broke
	over := treasureItem("over", "Weapons:6", 2, model.TreasureIntact)
	over.Treasure.QtyMoving = ptr(9) // stale after qty was lowered → capped at qty
	coin := func(id string, gp int, st model.TreasureState) model.ContentItem {
		return model.ContentItem{ID: id, Type: model.ContentCoin, Coin: &model.CoinDrop{Currency: model.Currency{GP: gp}, State: st}}
	}
	enc := model.Encounter{Content: []model.ContentItem{
		darts,
		over,
		treasureItem("left", "Weapons:7", 1, model.TreasureLeftBehind),
		coin("spent", 100, model.TreasureConsumed),
		coin("kept", 5, ""),
		{ID: "altar", Type: model.ContentPool, Pool: &model.PoolHeader{Name: "Altar", Gate: &model.Gate{Skill: "Perception", DC: 18}, NotFound: true}},
		treasureItem("hidden", "Weapons:8", 1, model.TreasureIntact),
		coin("hiddencoin", 50, model.TreasureIntact),
		{ID: "chest", Type: model.ContentPool, Pool: &model.PoolHeader{Name: "Chest"}},
		treasureItem("found", "Weapons:9", 1, model.TreasureIntact),
		coin("chestcoin", 7, ""),
	}}
	items, coins := ReleasePayloadFromEncounter(enc)
	got := map[string]int{}
	for _, it := range items {
		got[it.Ref.GameID] = it.Qty
	}
	want := map[string]int{"Weapons:5": 18, "Weapons:6": 2, "Weapons:9": 1}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("items = %v, want %v", got, want)
		}
	}
	if coins != (model.Currency{GP: 12}) {
		t.Fatalf("coins = %+v, want 12 gp (kept 5 + chest 7)", coins)
	}
}

func TestReleasePayload_EmptyContent(t *testing.T) {
	items, coins := ReleasePayloadFromEncounter(model.Encounter{})
	if len(items) != 0 || coins != (model.Currency{}) {
		t.Fatalf("empty content should yield no loot: items=%+v coins=%+v", items, coins)
	}
}

// --- light signing test: the Client builds a signed POST to the right URL ---

type fakeSigner struct {
	called      bool
	payloadHash string
}

func (f *fakeSigner) SignHTTP(_ context.Context, _ aws.Credentials, r *http.Request,
	payloadHash, service, region string, _ time.Time, _ ...func(*v4.SignerOptions)) error {
	f.called = true
	f.payloadHash = payloadHash
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 ...")
	// NOTE: the real aws-sdk-go-v2 v4 signer does NOT set X-Amz-Content-Sha256 — the
	// CLIENT must set it before signing. Don't set it here, or we'd mask that bug.
	return nil
}

type fakeDoer struct {
	req    *http.Request
	status int
	body   string
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	f.req = r
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader(f.body))}, nil
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
	dr.body = `{"items":[{"id":"i1"},{"id":"i2"}],"ledger":{"id":"enc1","kind":"release"}}`
	rcpt, err := c.PushRelease(context.Background(), "g1", ReleaseInput{ClientULID: "enc1", Coins: model.Currency{GP: 5}})
	if err != nil {
		t.Fatalf("PushRelease: %v", err)
	}
	if rcpt != (Receipt{LedgerID: "enc1", ItemCount: 2}) {
		t.Fatalf("receipt = %+v, want ledger enc1 with 2 items", rcpt)
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
	// The client (not the signer) must set X-Amz-Content-Sha256 to the body hash,
	// both as a sent header and as the payloadHash it signs with — otherwise the IAM
	// Function URL 403s a bodied POST.
	wantBody, _ := json.Marshal(ReleaseInput{ClientULID: "enc1", Coins: model.Currency{GP: 5}})
	sum := sha256.Sum256(wantBody)
	wantHash := hex.EncodeToString(sum[:])
	if got := dr.req.Header.Get("X-Amz-Content-Sha256"); got != wantHash {
		t.Fatalf("x-amz-content-sha256 header = %q, want %q (client must set it)", got, wantHash)
	}
	if sg.payloadHash != wantHash {
		t.Fatalf("signed payloadHash = %q, want %q", sg.payloadHash, wantHash)
	}
}

// A 2xx whose body isn't a releaseResult is an error, not an empty receipt.
func TestClient_PushRelease_UndecodableReceiptIsError(t *testing.T) {
	c, _, dr := newTestClient(http.StatusCreated)
	dr.body = "not json"
	if _, err := c.PushRelease(context.Background(), "g1", ReleaseInput{ClientULID: "enc1"}); err == nil {
		t.Fatal("expected an error for an undecodable receipt")
	}
}

func TestClient_PushRelease_Non2xxIsError(t *testing.T) {
	c, _, _ := newTestClient(http.StatusForbidden)
	if _, err := c.PushRelease(context.Background(), "g1", ReleaseInput{ClientULID: "enc1"}); err == nil {
		t.Fatal("expected an error on 403")
	}
}

// variant rides as a sibling of ref on the wire; the base item omits it.
func TestItemInput_VariantWire(t *testing.T) {
	b, _ := json.Marshal(ItemInput{Ref: model.ContentRef{GameID: "g"}, Variant: "Striking (Greater)", Qty: 1})
	if !strings.Contains(string(b), `"variant":"Striking (Greater)"`) {
		t.Fatalf("variant missing: %s", b)
	}
	b, _ = json.Marshal(ItemInput{Ref: model.ContentRef{GameID: "g"}, Qty: 1})
	if strings.Contains(string(b), "variant") {
		t.Fatalf("base item must omit variant: %s", b)
	}
}
