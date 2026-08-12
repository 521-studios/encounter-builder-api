// Package model holds the Encounter aggregate and the contentRef convention
// shared across the 521 cluster. The API stores and passes contentRefs
// opaquely — rendering and derivation (rune/template application) happen in
// pfsrd2-data-api / the display library, not here.
package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// ContentRef references a monster or item in one of three shapes (see
// 521-architect encounter-treasure-cluster.md §4):
//   - pristine:  {game_id, version?}
//   - derived:   {base:{game_id,version?}, modifications:[…], json:{…resolved}}
//   - custom:    {json:{…}}
//
// Derived keeps base+modifications as provenance so a future builder can
// re-open it rather than treating the snapshot as opaque.
type ContentRef struct {
	GameID        string            `json:"game_id,omitempty"`
	Version       string            `json:"version,omitempty"`
	Base          *ContentRef       `json:"base,omitempty"`
	Modifications []json.RawMessage `json:"modifications,omitempty"`
	JSON          json.RawMessage   `json:"json,omitempty"`
}

// isEmpty reports whether a ref names nothing — no pristine game_id, no custom
// json, and no derived base that itself references content. A non-nil but empty
// base ({"base":{}}) still points at nothing, so the check recurses.
func (r ContentRef) isEmpty() bool {
	baseEmpty := r.Base == nil || r.Base.isEmpty()
	return r.GameID == "" && baseEmpty && len(r.Modifications) == 0 && len(r.JSON) == 0
}

// Adjustment is the PF2e elite/weak template applied to a monster (±1 level).
type Adjustment string

const (
	AdjustmentNone  Adjustment = "none"
	AdjustmentElite Adjustment = "elite"
	AdjustmentWeak  Adjustment = "weak"
)

// MonsterEntry is one monster line in an encounter.
type MonsterEntry struct {
	Ref        ContentRef `json:"ref"`
	Count      int        `json:"count"`
	Adjustment Adjustment `json:"adjustment"`
	Nickname   string     `json:"nickname,omitempty"`
}

// SaleClass governs sale value: pure treasure sells at full price.
type SaleClass string

const (
	SaleNormal       SaleClass = "normal"
	SalePureTreasure SaleClass = "pure_treasure"
)

// TreasureState lets the GM prune loot post-encounter ("the ogre drank the
// potion", "the scrolls got fireballed").
type TreasureState string

const (
	TreasureIntact    TreasureState = "intact"
	TreasureConsumed  TreasureState = "consumed"
	TreasureDestroyed TreasureState = "destroyed"
)

// TreasureLine is one treasure item in an encounter. Masking hides an
// unidentified item's identity until the party identifies it.
type TreasureLine struct {
	LineID     string        `json:"line_id"`
	Ref        ContentRef    `json:"ref"`
	Qty        int           `json:"qty"`
	Masked     bool          `json:"masked"`
	MaskLabel  string        `json:"mask_label,omitempty"`
	IdentifyDC int           `json:"identify_dc,omitempty"`
	SaleClass  SaleClass     `json:"sale_class,omitempty"`
	State      TreasureState `json:"state,omitempty"`
	StateNote  string        `json:"state_note,omitempty"`
}

// Currency is the coin reward.
type Currency struct {
	CP int `json:"cp"`
	SP int `json:"sp"`
	GP int `json:"gp"`
	PP int `json:"pp"`
}

// Status is the encounter lifecycle: draft (being built) → run (used at the
// table) → released (loot handed to the party-treasure app).
type Status string

const (
	StatusDraft    Status = "draft"
	StatusRun      Status = "run"
	StatusReleased Status = "released"
)

// Encounter is the aggregate stored per (campaign, encounter). CampaignID is
// the lets-roll game_id; monsters/items are contentRefs, never copied content.
type Encounter struct {
	ID         string         `json:"id"`
	CampaignID string         `json:"campaign_id"`
	Name       string         `json:"name"`
	Status     Status         `json:"status"`
	Notes      string         `json:"notes,omitempty"`
	Monsters   []MonsterEntry `json:"monsters"`
	Treasure   []TreasureLine `json:"treasure"`
	Currency   Currency       `json:"currency"`
	ReleasedAt *time.Time     `json:"released_at,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// Chapter is a subdivision of a campaign that groups encounters (like an
// adventure's chapters/parts). Stored per (campaign, chapter). Order drives the
// sidebar sort.
//
// The encounter->chapter link (an Encounter.chapter_id field, with chapterless
// encounters falling under a synthetic "Unsorted" group in the UI) is NOT wired
// yet — it lands with the encounter-v2 slice (bd_521Studios-sdmq). Until then a
// Chapter is a standalone grouping entity nothing references.
type Chapter struct {
	ID         string    `json:"id"`
	CampaignID string    `json:"campaign_id"`
	Name       string    `json:"name"`
	Order      int       `json:"order"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ChapterInput is the client-writable shape for create/update (server owns
// id/campaign/timestamps). Order is optional — omit to leave it unchanged.
type ChapterInput struct {
	Name  string `json:"name"`
	Order *int   `json:"order,omitempty"`
}

func (in ChapterInput) Validate() error {
	if in.Name == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

var validAdjustments = map[Adjustment]bool{AdjustmentNone: true, AdjustmentElite: true, AdjustmentWeak: true}
var validSaleClasses = map[SaleClass]bool{SaleNormal: true, SalePureTreasure: true}
var validTreasureStates = map[TreasureState]bool{TreasureIntact: true, TreasureConsumed: true, TreasureDestroyed: true}

// EncounterInput is the client-writable shape for create/update. Keeping it
// separate from Encounter is the invariant: server-owned fields (id, campaign,
// status lifecycle, timestamps) simply aren't in this struct, so a client can't
// set them — the handler maps the validated input onto a server-owned Encounter.
type EncounterInput struct {
	Name     string         `json:"name"`
	Notes    string         `json:"notes,omitempty"`
	Monsters []MonsterEntry `json:"monsters,omitempty"`
	Treasure []TreasureLine `json:"treasure,omitempty"`
	Currency Currency       `json:"currency"`
	// Status is honored only by update, and only for draft<->run (release has
	// its own endpoint). Empty means "leave unchanged".
	Status Status `json:"status,omitempty"`
}

// Validate checks and normalizes the input in place: it fills default enum
// values (so unset never persists as "") and rejects illegal states —
// including a monster/treasure line whose ref points at no content.
func (in *EncounterInput) Validate() error {
	if in.Name == "" {
		return fmt.Errorf("name is required")
	}
	for i := range in.Monsters {
		m := &in.Monsters[i]
		if m.Count < 1 {
			return fmt.Errorf("monster[%d]: count must be >= 1", i)
		}
		if m.Ref.isEmpty() {
			return fmt.Errorf("monster[%d]: ref must reference content (game_id, base, or json)", i)
		}
		if m.Adjustment == "" {
			m.Adjustment = AdjustmentNone
		} else if !validAdjustments[m.Adjustment] {
			return fmt.Errorf("monster[%d]: invalid adjustment %q", i, m.Adjustment)
		}
	}
	for i := range in.Treasure {
		t := &in.Treasure[i]
		if t.Qty < 1 {
			return fmt.Errorf("treasure[%d]: qty must be >= 1", i)
		}
		if t.Ref.isEmpty() {
			return fmt.Errorf("treasure[%d]: ref must reference content (game_id, base, or json)", i)
		}
		if t.SaleClass == "" {
			t.SaleClass = SaleNormal
		} else if !validSaleClasses[t.SaleClass] {
			return fmt.Errorf("treasure[%d]: invalid sale_class %q", i, t.SaleClass)
		}
		if t.State == "" {
			t.State = TreasureIntact
		} else if !validTreasureStates[t.State] {
			return fmt.Errorf("treasure[%d]: invalid state %q", i, t.State)
		}
	}
	for _, c := range []int{in.Currency.CP, in.Currency.SP, in.Currency.GP, in.Currency.PP} {
		if c < 0 {
			return fmt.Errorf("currency amounts must be >= 0")
		}
	}
	return nil
}
