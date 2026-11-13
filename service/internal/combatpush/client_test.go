package combatpush

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

func TestMonstersFrom_ExpandsCountAndSkipsNonMonsters(t *testing.T) {
	enc := model.Encounter{Content: []model.ContentItem{
		{ID: "a", Type: model.ContentMonster, Monster: &model.MonsterEntry{Ref: model.ContentRef{GameID: "gob"}, Count: 2, Adjustment: "none", Nickname: "Boss"}},
		{ID: "old", Type: model.ContentMonster, Monster: &model.MonsterEntry{Ref: model.ContentRef{GameID: "ogre"}, Count: 1, Adjustment: "elite"}},
		{ID: "h", Type: model.ContentHazard, Monster: &model.MonsterEntry{Ref: model.ContentRef{GameID: "trap"}, Count: 1}},
		{ID: "b", Type: model.ContentMonster, Monster: &model.MonsterEntry{Ref: model.ContentRef{GameID: "wolf"}}}, // count 0 → 1
		{ID: "m", Type: model.ContentMarkdown},
	}, Kills: map[string]model.Kill{"a:2": {Killed: true}, "b:1": {Killed: false}}}
	got, legacy := MonstersFrom(enc)
	if strings.Join(legacy, ",") != "old" {
		t.Fatalf("legacy = %v, want [old] (elite via the old adjustment field)", legacy)
	}
	keys := []string{}
	for _, m := range got {
		keys = append(keys, m.Key)
	}
	if strings.Join(keys, ",") != "a:1,a:2,b:1" {
		t.Fatalf("keys = %v", keys)
	}
	if got[0].Killed || !got[1].Killed || got[2].Killed {
		t.Fatalf("killed flags: %+v", got)
	}
	if got[0].Nickname != "Boss" || got[0].Ref.GameID != "gob" {
		t.Fatalf("fields: %+v", got[0])
	}
}

// The push carries a decodable presigned STS GetCallerIdentity whose SIGNED
// headers bind the audience and the exact push body hash.
func TestPush_SendsBoundSTSIdentity(t *testing.T) {
	var gotBody []byte
	var ident identity
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/games/42/eb_encounters" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer gm-token" {
			t.Errorf("GM bearer not forwarded: %q", r.Header.Get("Authorization"))
		}
		raw, err := base64.StdEncoding.DecodeString(r.Header.Get("X-EB-Identity"))
		if err != nil || json.Unmarshal(raw, &ident) != nil {
			t.Errorf("X-EB-Identity not base64 JSON: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"lets_roll_encounter_id":7,"created":true,"kept":[{"eb_monster_key":"a:2","reason":"damaged"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "staging", credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""), "us-east-2")
	c.now = func() time.Time { return time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC) }
	rcpt, err := c.Push(context.Background(), "gm-token", "42", Input{EncounterID: "e1", Name: "Hall"})
	if err != nil {
		t.Fatal(err)
	}
	if rcpt.LetsRollEncounterID != 7 || !rcpt.Created || len(rcpt.Kept) != 1 || rcpt.Kept[0].Reason != "damaged" {
		t.Fatalf("receipt = %+v", rcpt)
	}

	if ident.URL != "https://sts.us-east-2.amazonaws.com/" || ident.Method != http.MethodPost {
		t.Fatalf("sts target = %s %s", ident.Method, ident.URL)
	}
	if b, _ := base64.StdEncoding.DecodeString(ident.Body); string(b) != stsBody {
		t.Fatalf("sts body = %q", b)
	}
	h := http.Header(ident.Headers)
	sum := sha256.Sum256(gotBody)
	if h.Get("X-Eb-Audience") != "lets-roll-staging" || h.Get("X-Eb-Body-Sha256") != hex.EncodeToString(sum[:]) {
		t.Fatalf("binding headers = %v", h)
	}
	if h.Get("Host") != "sts.us-east-2.amazonaws.com" || h.Get("Content-Length") != "43" {
		t.Fatalf("host/content-length must be sent for an exact replay: %v", h)
	}
	authz := h.Get("Authorization")
	if !strings.Contains(authz, "/us-east-2/sts/aws4_request") ||
		!strings.Contains(authz, "x-eb-audience") || !strings.Contains(authz, "x-eb-body-sha256") {
		t.Fatalf("audience/body hash must be SIGNED headers: %s", authz)
	}
}

func TestPush_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad identity", http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(srv.URL, "staging", aws.AnonymousCredentials{}, "us-east-2")
	c.creds = credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")
	if _, err := c.Push(context.Background(), "gm-token", "42", Input{}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401", err)
	}
}
