package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeDynamo is an in-memory DynamoAPI keyed by "pk\x00sk".
type fakeDynamo struct {
	items map[string]map[string]types.AttributeValue
}

func newFake() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]types.AttributeValue{}}
}

func s(av types.AttributeValue) string { return av.(*types.AttributeValueMemberS).Value }

func compositeKey(k map[string]types.AttributeValue) string {
	return s(k["pk"]) + "\x00" + s(k["sk"])
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.items[compositeKey(in.Item)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{Item: f.items[compositeKey(in.Key)]}, nil
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	delete(f.items, compositeKey(in.Key))
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	pk := s(in.ExpressionAttributeValues[":pk"])
	skPrefix := s(in.ExpressionAttributeValues[":sk"])
	var out []map[string]types.AttributeValue
	for _, item := range f.items {
		if s(item["pk"]) == pk && strings.HasPrefix(s(item["sk"]), skPrefix) {
			out = append(out, item)
		}
	}
	return &dynamodb.QueryOutput{Items: out}, nil
}

func sampleEncounter() model.Encounter {
	return model.Encounter{
		ID:         "enc-1",
		CampaignID: "game-42",
		Name:       "Goblin Ambush",
		Status:     model.StatusDraft,
		Monsters: []model.MonsterEntry{{
			Ref:        model.ContentRef{GameID: "abc123"},
			Count:      3,
			Adjustment: model.AdjustmentWeak,
		}},
		Treasure: []model.TreasureLine{{
			LineID:    "t1",
			Ref:       model.ContentRef{JSON: json.RawMessage(`{"name":"fireball scroll"}`)},
			Qty:       1,
			Masked:    true,
			MaskLabel: "unidentified scroll",
			SaleClass: model.SaleNormal,
			State:     model.TreasureIntact,
		}},
		Currency:  model.Currency{GP: 15},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func TestStore_PutGetRoundTrip(t *testing.T) {
	st := New(newFake(), "test-table")
	ctx := context.Background()
	want := sampleEncounter()

	if err := st.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := st.Get(ctx, want.CampaignID, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Full-fidelity round trip through the JSON payload, incl. contentRefs.
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("round trip mismatch:\n want %s\n got  %s", wantJSON, gotJSON)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	st := New(newFake(), "test-table")
	if _, err := st.Get(context.Background(), "game-42", "missing"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStore_ListScopedToCampaign(t *testing.T) {
	st := New(newFake(), "test-table")
	ctx := context.Background()
	a := sampleEncounter()
	b := sampleEncounter()
	b.ID = "enc-2"
	other := sampleEncounter()
	other.ID = "enc-3"
	other.CampaignID = "game-99" // different campaign
	for _, e := range []model.Encounter{a, b, other} {
		if err := st.Put(ctx, e); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	list, err := st.List(ctx, "game-42")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d, want 2 (must not leak the other campaign)", len(list))
	}
	for _, e := range list {
		if e.CampaignID != "game-42" {
			t.Fatalf("List leaked campaign %s", e.CampaignID)
		}
	}
}

// pagingDynamo returns items one-per-page with a LastEvaluatedKey until the
// last, to exercise List's pagination loop.
type pagingDynamo struct {
	pages []map[string]types.AttributeValue
	calls int
}

func (p *pagingDynamo) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (p *pagingDynamo) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (p *pagingDynamo) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (p *pagingDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	idx := 0
	if in.ExclusiveStartKey != nil {
		idx = int(in.ExclusiveStartKey["n"].(*types.AttributeValueMemberN).Value[0] - '0')
	}
	p.calls++
	out := &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{p.pages[idx]}}
	if idx+1 < len(p.pages) {
		out.LastEvaluatedKey = map[string]types.AttributeValue{
			"n": &types.AttributeValueMemberN{Value: string(rune('0' + idx + 1))},
		}
	}
	return out, nil
}

func TestStore_ListPaginates(t *testing.T) {
	mk := func(id string) map[string]types.AttributeValue {
		e := sampleEncounter()
		e.ID = id
		payload, _ := json.Marshal(e)
		return map[string]types.AttributeValue{
			"pk":      &types.AttributeValueMemberS{Value: campaignPK(e.CampaignID)},
			"sk":      &types.AttributeValueMemberS{Value: encounterSK(id)},
			"payload": &types.AttributeValueMemberS{Value: string(payload)},
		}
	}
	db := &pagingDynamo{pages: []map[string]types.AttributeValue{mk("a"), mk("b"), mk("c")}}
	got, err := New(db, "t").List(context.Background(), "game-42")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d across pages, want 3", len(got))
	}
	if db.calls != 3 {
		t.Fatalf("issued %d queries, want 3 (one per page)", db.calls)
	}
}

func TestStore_ListEmptyIsNonNil(t *testing.T) {
	got, err := New(newFake(), "t").List(context.Background(), "no-such-campaign")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got == nil {
		t.Fatal("List returned nil for an empty campaign; must be non-nil so it encodes as [] not null")
	}
	if len(got) != 0 {
		t.Fatalf("List len = %d, want 0", len(got))
	}
}

// TestStore_DecodesLegacyEncounter proves back-compat: a payload written before
// the v2 fields existed (no chapter_id / description) still decodes, with those
// fields as their zero values — so old encounters keep working after the upgrade.
func TestStore_DecodesLegacyEncounter(t *testing.T) {
	fake := newFake()
	st := New(fake, "test-table")
	ctx := context.Background()

	// A legacy item: full encounter JSON minus the v2 fields.
	legacy := `{"id":"enc-old","campaign_id":"game-42","name":"Old Fight","status":"draft","monsters":[],"treasure":[],"currency":{"cp":0,"sp":0,"gp":5,"pp":0},"created_at":"2020-01-01T00:00:00Z","updated_at":"2020-01-01T00:00:00Z"}`
	fake.items[campaignPK("game-42")+"\x00"+encounterSK("enc-old")] = map[string]types.AttributeValue{
		"pk":      &types.AttributeValueMemberS{Value: campaignPK("game-42")},
		"sk":      &types.AttributeValueMemberS{Value: encounterSK("enc-old")},
		"payload": &types.AttributeValueMemberS{Value: legacy},
	}

	got, err := st.Get(ctx, "game-42", "enc-old")
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	if got.Name != "Old Fight" || got.Currency.GP != 5 {
		t.Fatalf("legacy decode lost fields: %+v", got)
	}
	if got.ChapterID != "" || got.Description != "" {
		t.Fatalf("v2 fields should default to empty on a legacy row: chapter=%q desc=%q", got.ChapterID, got.Description)
	}
}

func TestStore_Delete(t *testing.T) {
	st := New(newFake(), "test-table")
	ctx := context.Background()
	e := sampleEncounter()
	_ = st.Put(ctx, e)
	if err := st.Delete(ctx, e.CampaignID, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, e.CampaignID, e.ID); err != ErrNotFound {
		t.Fatalf("after delete, Get err = %v, want ErrNotFound", err)
	}
}
