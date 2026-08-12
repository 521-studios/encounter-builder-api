package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func sampleChapter() model.Chapter {
	return model.Chapter{
		ID:         "ch-1",
		CampaignID: "game-42",
		Name:       "Chapter 1: The Fall",
		Order:      1,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
		UpdatedAt:  time.Now().UTC().Truncate(time.Second),
	}
}

func TestStore_ChapterPutGetRoundTrip(t *testing.T) {
	st := New(newFake(), "test-table")
	ctx := context.Background()
	want := sampleChapter()

	if err := st.PutChapter(ctx, want); err != nil {
		t.Fatalf("PutChapter: %v", err)
	}
	got, err := st.GetChapter(ctx, want.CampaignID, want.ID)
	if err != nil {
		t.Fatalf("GetChapter: %v", err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("round trip mismatch:\n want %s\n got  %s", wantJSON, gotJSON)
	}
}

func TestStore_GetChapterNotFound(t *testing.T) {
	st := New(newFake(), "test-table")
	if _, err := st.GetChapter(context.Background(), "game-42", "missing"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ListChapters must be scoped to the campaign AND must not pick up encounters,
// which share the same pk but a different sk prefix (ENCOUNTER# vs CHAPTER#).
func TestStore_ListChaptersScopedAndSorted(t *testing.T) {
	st := New(newFake(), "test-table")
	ctx := context.Background()

	// Out-of-order chapters + a tie on Order to prove the (Order, Name) sort.
	// c1 is the base sampleChapter() (order 1, "Chapter 1: The Fall").
	c1 := sampleChapter()
	c2 := sampleChapter()
	c2.ID, c2.Name, c2.Order = "ch-2", "Chapter 2", 2
	c0b := sampleChapter()
	c0b.ID, c0b.Name, c0b.Order = "ch-0b", "Bravo", 0
	c0a := sampleChapter()
	c0a.ID, c0a.Name, c0a.Order = "ch-0a", "Alpha", 0
	other := sampleChapter()
	other.ID, other.CampaignID = "ch-x", "game-99"

	for _, c := range []model.Chapter{c2, c1, c0b, c0a, other} {
		if err := st.PutChapter(ctx, c); err != nil {
			t.Fatalf("PutChapter: %v", err)
		}
	}
	// An encounter in the same campaign must be invisible to ListChapters.
	if err := st.Put(ctx, sampleEncounter()); err != nil {
		t.Fatalf("Put encounter: %v", err)
	}

	list, err := st.ListChapters(ctx, "game-42")
	if err != nil {
		t.Fatalf("ListChapters: %v", err)
	}
	gotOrder := make([]string, len(list))
	for i, c := range list {
		if c.CampaignID != "game-42" {
			t.Fatalf("ListChapters leaked campaign %s", c.CampaignID)
		}
		gotOrder[i] = c.Name
	}
	// (Order, Name): order 0 → Alpha, Bravo; then order 1; then order 2.
	want := []string{"Alpha", "Bravo", "Chapter 1: The Fall", "Chapter 2"}
	if len(gotOrder) != len(want) {
		t.Fatalf("ListChapters returned %d chapters (%v), want %d", len(gotOrder), gotOrder, len(want))
	}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Fatalf("sort order = %v, want %v", gotOrder, want)
		}
	}
}

func TestStore_ListChaptersEmptyIsNonNil(t *testing.T) {
	got, err := New(newFake(), "t").ListChapters(context.Background(), "no-such-campaign")
	if err != nil {
		t.Fatalf("ListChapters: %v", err)
	}
	if got == nil {
		t.Fatal("ListChapters returned nil; must be non-nil so it encodes as [] not null")
	}
}

// TestStore_ListChaptersPaginates exercises ListChapters' LastEvaluatedKey
// continuation loop (its own copy of List's pagination), reusing pagingDynamo.
func TestStore_ListChaptersPaginates(t *testing.T) {
	mk := func(id string) map[string]types.AttributeValue {
		c := sampleChapter()
		c.ID = id
		payload, _ := json.Marshal(c)
		return map[string]types.AttributeValue{
			"pk":      &types.AttributeValueMemberS{Value: campaignPK(c.CampaignID)},
			"sk":      &types.AttributeValueMemberS{Value: chapterSK(id)},
			"payload": &types.AttributeValueMemberS{Value: string(payload)},
		}
	}
	db := &pagingDynamo{pages: []map[string]types.AttributeValue{mk("a"), mk("b"), mk("c")}}
	got, err := New(db, "t").ListChapters(context.Background(), "game-42")
	if err != nil {
		t.Fatalf("ListChapters: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListChapters returned %d across pages, want 3", len(got))
	}
	if db.calls != 3 {
		t.Fatalf("issued %d queries, want 3 (one per page)", db.calls)
	}
}

func TestStore_DeleteChapter(t *testing.T) {
	st := New(newFake(), "test-table")
	ctx := context.Background()
	c := sampleChapter()
	_ = st.PutChapter(ctx, c)
	if err := st.DeleteChapter(ctx, c.CampaignID, c.ID); err != nil {
		t.Fatalf("DeleteChapter: %v", err)
	}
	if _, err := st.GetChapter(ctx, c.CampaignID, c.ID); err != ErrNotFound {
		t.Fatalf("after delete, GetChapter err = %v, want ErrNotFound", err)
	}
}
