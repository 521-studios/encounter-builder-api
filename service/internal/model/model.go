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

// Gate is an optional discovery check that hides a treasure pool until the party
// finds it (e.g. "DC 18 Perception to Search the altar"). Informational in the
// builder — prep budgets the full available treasure regardless of gates; Party
// Treasure applies the gate at play time.
type Gate struct {
	Skill string `json:"skill,omitempty"`
	DC    int    `json:"dc,omitempty"`
}

// ValueTiers is a treasure line whose realized gp value depends on a skill check's
// degree of success (AV B9's harvested gear: 40 gp on success, 20 on failure, 0 on
// critical failure). Values are in copper; a nil tier is unset (at least one must be
// set). The builder budgets the Success tier; Party Treasure records the actual
// rolled outcome.
type ValueTiers struct {
	CritSuccess *int `json:"crit_success,omitempty"`
	Success     *int `json:"success,omitempty"`
	Failure     *int `json:"failure,omitempty"`
	CritFailure *int `json:"crit_failure,omitempty"`
}

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
	// PoolID groups the line under a TreasurePool (where it's found). Empty or
	// dangling (pool deleted) renders under the default pool — like a dangling
	// chapter_id renders under "Unsorted" — so deleting a pool never orphans loot.
	PoolID string `json:"pool_id,omitempty"`
	// ValueTiers, when set, overrides the item's price with a degree-of-success
	// value (harvest/extraction checks).
	ValueTiers *ValueTiers `json:"value_tiers,omitempty"`
	// Variant is the chosen entry of an item's stat_block.variants, by NAME
	// (e.g. "Striking (Greater)") — stable across data changes, unlike an index.
	// Empty means the base item. Opaque to the API; the display library resolves it.
	Variant string `json:"variant,omitempty"`
}

// TreasurePool groups treasure by where it's found (a module splits loot into
// distinct finds). First-class so each pool carries its own GM markdown
// Description and an optional discovery Gate. An encounter's treasure lines
// reference a pool by PoolID; the client keeps a default pool that ungated,
// unassigned loot falls into.
type TreasurePool struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"` // GM-authored markdown
	Gate        *Gate  `json:"gate,omitempty"`
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
//
// ChapterID (v2) groups the encounter under a Chapter in the GM sidebar; empty
// or dangling (chapter deleted) means it renders under the synthetic "Unsorted"
// group — deliberately not validated against an existing chapter, so deleting a
// chapter never orphans an encounter. Description is GM-authored markdown,
// rendered by the frontend (the API stores it verbatim, no sanitization here).
// Monster templates need no new field: a templated monster is just a derived
// MonsterEntry.Ref ({base, modifications:[{template_game_id, selections}…], json}).
type Encounter struct {
	ID            string         `json:"id"`
	CampaignID    string         `json:"campaign_id"`
	Name          string         `json:"name"`
	Status        Status         `json:"status"`
	ChapterID     string         `json:"chapter_id,omitempty"`
	Description   string         `json:"description,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	Monsters      []MonsterEntry `json:"monsters"`
	Treasure      []TreasureLine `json:"treasure"`
	TreasurePools []TreasurePool `json:"treasure_pools,omitempty"`
	Currency      Currency       `json:"currency"`
	// PartyLevel/PartySize are the encounter's expected party level and PC count
	// used for treasure/difficulty budgeting. nil means "inherit" — the client
	// resolves encounter -> chapter -> campaign settings -> app default. The API
	// stores the raw override; it does not resolve inheritance.
	PartyLevel *int       `json:"party_level,omitempty"`
	PartySize  *int       `json:"party_size,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Chapter is a subdivision of a campaign that groups encounters (like an
// adventure's chapters/parts). Stored per (campaign, chapter). Order drives the
// sidebar sort. Encounters reference a chapter via Encounter.ChapterID; an empty
// or dangling reference falls under a synthetic "Unsorted" group in the UI (the
// link is intentionally soft — see Encounter.ChapterID).
type Chapter struct {
	ID         string `json:"id"`
	CampaignID string `json:"campaign_id"`
	Name       string `json:"name"`
	Order      int    `json:"order"`
	// PartyLevel/PartySize are the chapter's default expected party level and PC
	// count; encounters inherit them unless overridden. nil means the chapter
	// itself inherits from campaign settings. Raw override only — no resolution.
	// Full-replaced on update (like Encounter/CampaignSettings): a nil field
	// clears the override back to inherit. Partial-update callers (the sidebar
	// rename) must round-trip these or a rename would wipe them.
	PartyLevel *int      `json:"party_level,omitempty"`
	PartySize  *int      `json:"party_size,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ChapterInput is the client-writable shape for create/update (server owns
// id/campaign/timestamps). Order is optional — omit to leave it unchanged.
type ChapterInput struct {
	Name       string `json:"name"`
	Order      *int   `json:"order,omitempty"`
	PartyLevel *int   `json:"party_level,omitempty"`
	PartySize  *int   `json:"party_size,omitempty"`
}

func (in ChapterInput) Validate() error {
	if in.Name == "" {
		return fmt.Errorf("name is required")
	}
	return validateParty(in.PartyLevel, in.PartySize)
}

// CampaignSettings holds per-campaign defaults that encounters/chapters inherit.
// It's a singleton per campaign (one item under CAMPAIGN#<id>). PartyLevel/
// PartySize are the base of the inheritance chain (campaign -> chapter ->
// encounter); nil means "unset", and the client falls back to an app default.
type CampaignSettings struct {
	CampaignID string     `json:"campaign_id"`
	PartyLevel *int       `json:"party_level,omitempty"`
	PartySize  *int       `json:"party_size,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// CampaignSettingsInput is the client-writable shape (PUT replaces the settings;
// server owns campaign id + timestamp).
type CampaignSettingsInput struct {
	PartyLevel *int `json:"party_level,omitempty"`
	PartySize  *int `json:"party_size,omitempty"`
}

func (in CampaignSettingsInput) Validate() error {
	return validateParty(in.PartyLevel, in.PartySize)
}

var validAdjustments = map[Adjustment]bool{AdjustmentNone: true, AdjustmentElite: true, AdjustmentWeak: true}
var validSaleClasses = map[SaleClass]bool{SaleNormal: true, SalePureTreasure: true}
var validTreasureStates = map[TreasureState]bool{TreasureIntact: true, TreasureConsumed: true, TreasureDestroyed: true}

// EncounterInput is the client-writable shape for create/update. Keeping it
// separate from Encounter is the invariant: server-owned fields (id, campaign,
// status lifecycle, timestamps) simply aren't in this struct, so a client can't
// set them — the handler maps the validated input onto a server-owned Encounter.
type EncounterInput struct {
	Name          string         `json:"name"`
	ChapterID     string         `json:"chapter_id,omitempty"`
	Description   string         `json:"description,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	Monsters      []MonsterEntry `json:"monsters,omitempty"`
	Treasure      []TreasureLine `json:"treasure,omitempty"`
	TreasurePools []TreasurePool `json:"treasure_pools,omitempty"`
	Currency      Currency       `json:"currency"`
	// PartyLevel/PartySize override the inherited expected-party values; nil
	// leaves the encounter inheriting from its chapter/campaign.
	PartyLevel *int `json:"party_level,omitempty"`
	PartySize  *int `json:"party_size,omitempty"`
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
		if t.IdentifyDC < 0 {
			return fmt.Errorf("treasure[%d]: identify_dc must be >= 0", i)
		}
		if v := t.ValueTiers; v != nil {
			anySet := false
			for _, tier := range []*int{v.CritSuccess, v.Success, v.Failure, v.CritFailure} {
				if tier != nil {
					anySet = true
					if *tier < 0 {
						return fmt.Errorf("treasure[%d]: value_tiers amounts must be >= 0", i)
					}
				}
			}
			if !anySet {
				return fmt.Errorf("treasure[%d]: value_tiers must set at least one tier", i)
			}
		}
		// PoolID is intentionally not validated against TreasurePools — a dangling
		// pool_id renders under the default pool, so deleting a pool never orphans loot.
	}
	for i := range in.TreasurePools {
		p := &in.TreasurePools[i]
		if p.ID == "" {
			return fmt.Errorf("treasure_pool[%d]: id is required", i)
		}
		// A present gate is a real discovery check — reject an empty {} that would
		// round-trip as a gate on no skill at DC 0 (indistinguishable from ungated).
		if g := p.Gate; g != nil {
			if g.Skill == "" {
				return fmt.Errorf("treasure_pool[%d]: gate requires a skill", i)
			}
			if g.DC < 1 {
				return fmt.Errorf("treasure_pool[%d]: gate dc must be >= 1", i)
			}
		}
	}
	for _, c := range []int{in.Currency.CP, in.Currency.SP, in.Currency.GP, in.Currency.PP} {
		if c < 0 {
			return fmt.Errorf("currency amounts must be >= 0")
		}
	}
	return validateParty(in.PartyLevel, in.PartySize)
}

// validateParty checks the optional expected-party fields shared by encounters,
// chapters, and campaign settings. nil means "inherit from the level above" and
// is always allowed; when set, the values must be sane — PF2e levels run 1–20
// (the span of the treasure/XP tables) and a party has at least one PC.
func validateParty(level, size *int) error {
	if level != nil && (*level < 1 || *level > 20) {
		return fmt.Errorf("party_level must be between 1 and 20")
	}
	if size != nil && *size < 1 {
		return fmt.Errorf("party_size must be >= 1")
	}
	return nil
}
