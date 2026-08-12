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
	SaleClass  SaleClass     `json:"sale_class"`
	State      TreasureState `json:"state"`
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

var validAdjustments = map[Adjustment]bool{AdjustmentNone: true, AdjustmentElite: true, AdjustmentWeak: true}
var validSaleClasses = map[SaleClass]bool{SaleNormal: true, SalePureTreasure: true}
var validTreasureStates = map[TreasureState]bool{TreasureIntact: true, TreasureConsumed: true, TreasureDestroyed: true}

// Validate checks the client-supplied parts of an encounter. Server-owned
// fields (id, status, timestamps) are set by handlers and not checked here.
func (e Encounter) Validate() error {
	if e.Name == "" {
		return fmt.Errorf("name is required")
	}
	for i, m := range e.Monsters {
		if m.Count < 1 {
			return fmt.Errorf("monster[%d]: count must be >= 1", i)
		}
		if m.Adjustment != "" && !validAdjustments[m.Adjustment] {
			return fmt.Errorf("monster[%d]: invalid adjustment %q", i, m.Adjustment)
		}
	}
	for i, t := range e.Treasure {
		if t.Qty < 1 {
			return fmt.Errorf("treasure[%d]: qty must be >= 1", i)
		}
		if t.SaleClass != "" && !validSaleClasses[t.SaleClass] {
			return fmt.Errorf("treasure[%d]: invalid sale_class %q", i, t.SaleClass)
		}
		if t.State != "" && !validTreasureStates[t.State] {
			return fmt.Errorf("treasure[%d]: invalid state %q", i, t.State)
		}
	}
	for _, c := range []int{e.Currency.CP, e.Currency.SP, e.Currency.GP, e.Currency.PP} {
		if c < 0 {
			return fmt.Errorf("currency amounts must be >= 0")
		}
	}
	return nil
}
