// Package store persists Encounter aggregates in a DynamoDB single table,
// keyed pk=CAMPAIGN#<game_id> / sk=ENCOUNTER#<id>. The whole aggregate is a
// JSON payload on the item — the service stores and passes contentRefs, it
// doesn't index into them. (DynamoDB's 400KB item ceiling is ample for an
// encounter; spill to S3 only if that's ever hit — noted, not built.)
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ErrNotFound is returned when an encounter doesn't exist.
var ErrNotFound = errors.New("encounter not found")

// DynamoAPI is the subset of the DynamoDB client the store uses, so tests can
// inject a fake.
type DynamoAPI interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

// Store is the encounter persistence layer.
type Store struct {
	db    DynamoAPI
	table string
}

// New wraps a DynamoDB API. The real *dynamodb.Client satisfies DynamoAPI.
func New(db DynamoAPI, table string) *Store {
	return &Store{db: db, table: table}
}

const skPrefix = "ENCOUNTER#"
const chapterSKPrefix = "CHAPTER#"

func campaignPK(campaignID string) string { return "CAMPAIGN#" + campaignID }
func encounterSK(id string) string        { return skPrefix + id }
func chapterSK(id string) string          { return chapterSKPrefix + id }

func (s *Store) key(campaignID, id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: campaignPK(campaignID)},
		"sk": &types.AttributeValueMemberS{Value: encounterSK(id)},
	}
}

func (s *Store) chapterKey(campaignID, id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: campaignPK(campaignID)},
		"sk": &types.AttributeValueMemberS{Value: chapterSK(id)},
	}
}

// putPayload marshals v to JSON and writes it as the item's payload attribute.
// Shared by every aggregate on this table (encounters, chapters) — they differ
// only in their key, not in how the body is stored.
func (s *Store) putPayload(ctx context.Context, key map[string]types.AttributeValue, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: marshal payload: %w", err)
	}
	key["payload"] = &types.AttributeValueMemberS{Value: string(payload)}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table),
		Item:      key,
	})
	return err
}

// decodePayload unmarshals an item's payload attribute into T.
func decodePayload[T any](item map[string]types.AttributeValue) (T, error) {
	var v T
	payload, ok := item["payload"].(*types.AttributeValueMemberS)
	if !ok {
		return v, fmt.Errorf("store: item missing payload attribute")
	}
	if err := json.Unmarshal([]byte(payload.Value), &v); err != nil {
		return v, fmt.Errorf("store: unmarshal payload: %w", err)
	}
	return v, nil
}

// Put creates or replaces an encounter.
func (s *Store) Put(ctx context.Context, enc model.Encounter) error {
	return s.putPayload(ctx, s.key(enc.CampaignID, enc.ID), enc)
}

// Get returns one encounter, or ErrNotFound.
func (s *Store) Get(ctx context.Context, campaignID, id string) (model.Encounter, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table),
		Key:       s.key(campaignID, id),
	})
	if err != nil {
		return model.Encounter{}, err
	}
	if out.Item == nil {
		return model.Encounter{}, ErrNotFound
	}
	return decode(out.Item)
}

// List returns all encounters for a campaign, following DynamoDB pagination —
// a single Query returns at most 1MB, so a campaign with many encounters spans
// pages. Looping on LastEvaluatedKey is required to avoid silently truncating.
func (s *Store) List(ctx context.Context, campaignID string) ([]model.Encounter, error) {
	encounters := make([]model.Encounter, 0) // non-nil so an empty campaign encodes as [] not null
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: campaignPK(campaignID)},
				":sk": &types.AttributeValueMemberS{Value: skPrefix},
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, err
		}
		for _, it := range out.Items {
			enc, err := decode(it)
			if err != nil {
				return nil, err
			}
			encounters = append(encounters, enc)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return encounters, nil
		}
		startKey = out.LastEvaluatedKey
	}
}

// Delete removes an encounter (idempotent — no error if it's already gone).
func (s *Store) Delete(ctx context.Context, campaignID, id string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.table),
		Key:       s.key(campaignID, id),
	})
	return err
}

func decode(item map[string]types.AttributeValue) (model.Encounter, error) {
	return decodePayload[model.Encounter](item)
}

// PutChapter creates or replaces a chapter.
func (s *Store) PutChapter(ctx context.Context, ch model.Chapter) error {
	return s.putPayload(ctx, s.chapterKey(ch.CampaignID, ch.ID), ch)
}

// GetChapter returns one chapter, or ErrNotFound.
func (s *Store) GetChapter(ctx context.Context, campaignID, id string) (model.Chapter, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table),
		Key:       s.chapterKey(campaignID, id),
	})
	if err != nil {
		return model.Chapter{}, err
	}
	if out.Item == nil {
		return model.Chapter{}, ErrNotFound
	}
	return decodePayload[model.Chapter](out.Item)
}

// ListChapters returns a campaign's chapters, sorted by (Order, Name) so the
// sidebar order is deterministic. Follows DynamoDB pagination like List.
func (s *Store) ListChapters(ctx context.Context, campaignID string) ([]model.Chapter, error) {
	chapters := make([]model.Chapter, 0) // non-nil so empty encodes as [] not null
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: campaignPK(campaignID)},
				":sk": &types.AttributeValueMemberS{Value: chapterSKPrefix},
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, err
		}
		for _, it := range out.Items {
			ch, err := decodePayload[model.Chapter](it)
			if err != nil {
				return nil, err
			}
			chapters = append(chapters, ch)
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].Order != chapters[j].Order {
			return chapters[i].Order < chapters[j].Order
		}
		return chapters[i].Name < chapters[j].Name
	})
	return chapters, nil
}

// DeleteChapter removes a chapter (idempotent). No cascade: encounters that
// reference it keep their now-dangling ChapterID and fall back to the "Unsorted"
// group in the UI (the encounter->chapter link is soft by design), rather than
// being deleted along with the chapter.
func (s *Store) DeleteChapter(ctx context.Context, campaignID, id string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.table),
		Key:       s.chapterKey(campaignID, id),
	})
	return err
}
