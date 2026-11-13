// Package combatpush pushes an encounter's combat into a lets-roll game
// (we5k.2.3): POST {lets-roll}/api/v1/games/{game}/eb_encounters, an idempotent
// upsert keyed on the EB encounter id (lets-roll-f6e).
//
// Auth is BOTH (Devon, 2026-11-13): the GM's lets-roll OIDC bearer, so lets-roll
// checks the caller is GM of the game, plus EB's service identity. lets-roll sits
// behind an ALB with no IAM authorizer, so it can't check SigV4 on the request
// itself; the client instead proves its IAM identity the Vault
// IAM-auth way: it presigns an sts:GetCallerIdentity request and sends it in
// X-EB-Identity. lets-roll replays that request to STS and checks the returned
// role ARN. The presigned request's SIGNED headers carry an audience and the
// SHA-256 of the push body, so the proof can't be reused for another service or
// another payload, and STS rejects it after ~15 minutes.
package combatpush

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Monster is one individual creature: EB expands a line's count, so each gets a
// stable key "<content item id>:<n>" that lets-roll syncs and reports kills by.
// Elite/weak travel inside Ref as template modifications. Nickname is used only
// when lets-roll creates the monster; a re-push never overwrites it.
type Monster struct {
	Key      string           `json:"eb_monster_key"`
	Ref      model.ContentRef `json:"content_ref"`
	Nickname string           `json:"nickname,omitempty"`
	// Killed: EB has this creature killed (we5k.2.4). lets-roll sets its health
	// to 0 and never deletes it on a re-push. Absent means no instruction, not
	// alive: un-killing in EB never revives a monster in lets-roll.
	Killed bool `json:"killed,omitempty"`
}

// Input is the push body (lets-roll-dur's ImportFromEb payload).
type Input struct {
	EncounterID string    `json:"eb_encounter_id"`
	Name        string    `json:"name"`
	Monsters    []Monster `json:"monsters"`
}

// Receipt is lets-roll's answer. Kept lists monsters a re-push would have
// removed but lets-roll kept because they were damaged or killed.
type Receipt struct {
	LetsRollEncounterID int           `json:"lets_roll_encounter_id"`
	Created             bool          `json:"created"`
	Kept                []KeptMonster `json:"kept,omitempty"`
}

type KeptMonster struct {
	Key    string `json:"eb_monster_key"`
	Reason string `json:"reason"`
}

// MonstersFrom expands an encounter's monster content items into individual
// creatures. Hazards and afflictions aren't initiative combatants in lets-roll
// and are left out. legacy lists the content items still using the pre-template
// `adjustment` field: their elite/weak can't travel in content_ref, so the caller
// refuses the push until the GM re-applies it as a template.
func MonstersFrom(enc model.Encounter) (out []Monster, legacy []string) {
	for _, c := range enc.Content {
		if c.Type != model.ContentMonster || c.Monster == nil {
			continue
		}
		if a := c.Monster.Adjustment; a != "" && a != "none" {
			legacy = append(legacy, c.ID)
			continue
		}
		n := c.Monster.Count
		if n < 1 {
			n = 1
		}
		for i := 1; i <= n; i++ {
			out = append(out, Monster{
				Key:      model.MonsterKey(c.ID, i),
				Ref:      c.Monster.Ref,
				Nickname: c.Monster.Nickname,
				Killed:   enc.Kills[model.MonsterKey(c.ID, i)].Killed,
			})
		}
	}
	return out, legacy
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type signer interface {
	SignHTTP(ctx context.Context, credentials aws.Credentials, r *http.Request,
		payloadHash, service, region string, signingTime time.Time,
		optFns ...func(*v4.SignerOptions)) error
}

// Client is the real pusher.
type Client struct {
	baseURL  string
	audience string // "lets-roll-<env>"; lets-roll requires its own
	http     httpDoer
	signer   signer
	creds    aws.CredentialsProvider
	region   string
	now      func() time.Time
}

// New builds a Client. baseURL is lets-roll (the OIDC issuer host).
func New(baseURL, env string, creds aws.CredentialsProvider, region string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		audience: "lets-roll-" + env,
		http:     &http.Client{Timeout: 10 * time.Second},
		signer:   v4.NewSigner(),
		creds:    creds,
		region:   region,
		now:      time.Now,
	}
}

const stsBody = "Action=GetCallerIdentity&Version=2011-06-15"

// identity is the presigned STS request lets-roll replays, sent base64'd JSON.
type identity struct {
	URL     string              `json:"url"`
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"` // base64
}

// identityHeader presigns sts:GetCallerIdentity bound to the audience and the
// push body's hash, and encodes it for X-EB-Identity.
func (c *Client) identityHeader(ctx context.Context, bodyHash string) (string, error) {
	stsURL := fmt.Sprintf("https://sts.%s.amazonaws.com/", c.region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stsURL, strings.NewReader(stsBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("X-EB-Audience", c.audience)
	req.Header.Set("X-EB-Body-SHA256", bodyHash)
	creds, err := c.creds.Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("combatpush: retrieve creds: %w", err)
	}
	sum := sha256.Sum256([]byte(stsBody))
	if err := c.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "sts", c.region, c.now()); err != nil {
		return "", fmt.Errorf("combatpush: sign: %w", err)
	}
	// SignHTTP signs host and content-length without setting them as headers; send
	// them too, so lets-roll's replay to STS reproduces the signed request exactly.
	headers := req.Header.Clone()
	headers.Set("Host", req.URL.Host)
	headers.Set("Content-Length", strconv.Itoa(len(stsBody)))
	raw, err := json.Marshal(identity{
		URL:     stsURL,
		Method:  http.MethodPost,
		Headers: headers,
		Body:    base64.StdEncoding.EncodeToString([]byte(stsBody)),
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// Push upserts the encounter's combat into lets-roll game gameID, as the GM
// (bearer) and as EB (the STS identity). Non-2xx is an error. 201 (created) and
// 200 (replay or sync) are both success.
func (c *Client) Push(ctx context.Context, bearer, gameID string, in Input) (Receipt, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Receipt{}, fmt.Errorf("combatpush: marshal: %w", err)
	}
	sum := sha256.Sum256(body)
	ident, err := c.identityHeader(ctx, hex.EncodeToString(sum[:]))
	if err != nil {
		return Receipt{}, err
	}
	target := fmt.Sprintf("%s/api/v1/games/%s/eb_encounters", c.baseURL, url.PathEscape(gameID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Receipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-EB-Identity", ident)
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := c.http.Do(req)
	if err != nil {
		return Receipt{}, fmt.Errorf("combatpush: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Receipt{}, fmt.Errorf("combatpush: lets-roll returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var out Receipt
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return Receipt{}, fmt.Errorf("combatpush: decode receipt: %w", err)
	}
	return out, nil
}
