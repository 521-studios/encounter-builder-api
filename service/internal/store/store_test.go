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
