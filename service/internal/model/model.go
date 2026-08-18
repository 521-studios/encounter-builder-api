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
	// PriceCp is the composed copper price of a derived item (base + runes), computed
	// by the builder and carried here so the treasure budget reads it directly. Opaque
	// to the API — stored and round-tripped, not interpreted. Declared so the strict
	// (DisallowUnknownFields) decoder accepts a priced composed ref.
	PriceCp *int `json:"price_cp,omitempty"`
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
	// Loadout is the creature's carried equipment (0o77) — catalog or composed
	// (runed) item refs, same shape as treasure/composed items, that the builder
	// can send into the encounter loot. Opaque round-trip like treasure refs.
	Loadout []LoadoutItem `json:"loadout,omitempty"`
}

// LoadoutItem is one piece of a creature's equipment: a catalog or composed item
// ref with a quantity (and optional variant). Opaque to the API — the builder and
// display library interpret the ref; the API stores + validates qty.
type LoadoutItem struct {
	Ref     ContentRef `json:"ref"`
	Qty     int        `json:"qty"`
	Variant string     `json:"variant,omitempty"`
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

// XPAward is a flat, non-combat XP grant — a story/exploration/quest milestone
// or an ally recruited (AV's "30 XP for gaining Augrael as an ally") — that
// advances the party with no creature to fight. Amount is the XP; Reason is the
// GM's label. Awards add to the encounter's XP total (and chapter rollups) but
// NOT to its combat difficulty band or treasure budget, which stay creature-derived.
type XPAward struct {
	Amount int    `json:"amount"`
	Reason string `json:"reason,omitempty"`
}

// RoomType classifies an area beyond combat. Modules are full of rooms that are
// hazards, haunts, exploration/skill challenges, social scenes, pure-knowledge
// rooms, or empty — for which the combat difficulty band is meaningless. Default
// is combat (the builder's original unit).
type RoomType string

const (
	RoomCombat      RoomType = "combat"
	RoomHazard      RoomType = "hazard"
	RoomHaunt       RoomType = "haunt"
	RoomExploration RoomType = "exploration"
	RoomSocial      RoomType = "social"
	RoomKnowledge   RoomType = "knowledge"
	RoomEmpty       RoomType = "empty"
)

// RewardKind types a non-treasure reward slot: information/lore unlocked, a ritual
// granted, an ally recruited, or a unique narrative item. Distinct from valued
// treasure (which feeds the budget) — these are informational GM records with no
// gp/XP effect.
type RewardKind string

const (
	RewardInformation RewardKind = "information"
	RewardRitual      RewardKind = "ritual"
	RewardAlly        RewardKind = "ally"
	RewardItem        RewardKind = "item"
)

// Reward is one non-treasure reward a room grants. Label is a short name;
// Description is GM-authored markdown.
type Reward struct {
	Kind        RewardKind `json:"kind"`
	Label       string     `json:"label"`
	Description string     `json:"description,omitempty"`
}

// SkillCheck is a structured discovery/skill entry a room carries (e.g. "DC 12
// Perception to spot the loose planks"). Skill + DC make it surfaceable at the
// table instead of buried in prose; Description is what it reveals/does (markdown).
//
// Richer structure (xhwl), all optional/back-compat:
//   - Successes: required successes to fully resolve (e.g. "4 successful DC 25
//     Thievery checks"); 0/omitted = 1.
//   - Alternatives: other skill+DC that ALSO satisfy the check ("DC 22 Thievery
//     OR DC 20 Religion").
//   - Outcomes: per-degree-of-success effect text (crit reveals extra, etc.).
type SkillCheck struct {
	Skill        string          `json:"skill"`
	DC           int             `json:"dc"`
	Description  string          `json:"description,omitempty"`
	Successes    int             `json:"successes,omitempty"`
	Alternatives []SkillOption   `json:"alternatives,omitempty"`
	Outcomes     *DegreeOutcomes `json:"outcomes,omitempty"`
}

// SkillOption is one alternative skill+DC that satisfies a SkillCheck.
type SkillOption struct {
	Skill string `json:"skill"`
	DC    int    `json:"dc"`
}

// DegreeOutcomes holds per-degree-of-success effect text; any field may be empty.
type DegreeOutcomes struct {
	CritSuccess string `json:"crit_success,omitempty"`
	Success     string `json:"success,omitempty"`
	Failure     string `json:"failure,omitempty"`
	CritFailure string `json:"crit_failure,omitempty"`
}

// Exit is one edge of the dungeon connectivity graph — a passage from this room to
// another encounter (ToEncounterID, a SOFT reference like ChapterID: not validated
// against existing encounters, so deleting a room never breaks a link) or to an
// external/unlisted destination named only by Label ("Exterior", "stairs up").
// Directed as modules list them per-room; the map view renders the graph.
type Exit struct {
	ToEncounterID string `json:"to_encounter_id,omitempty"`
	Label         string `json:"label,omitempty"`
	// Secret is per-direction: a passage secret from the hallway but obvious from the
	// room has secret:true on the hall→room exit and secret:false on the room→hall one.
	Secret bool `json:"secret,omitempty"`
	// Skill/DC are an optional skill check to find or traverse this exit (e.g. Perception
	// DC 18 to spot a secret door, Athletics DC 15 to climb). Also per-direction.
	Skill string `json:"skill,omitempty"`
	DC    int    `json:"dc,omitempty"`
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
// TextBlock is one titled markdown section of an encounter's body. Title is
// optional (an untitled block — e.g. the pre-blocks Description migrated on save).
// Body is GM-authored markdown, stored verbatim (no sanitization here; the frontend
// renders it). TextBlocks replaces the single Description: on the first save of an
// encounter that still carries a Description, the client moves it into an untitled
// block and clears Description (migrate-on-save). Opaque to the API otherwise.
type TextBlock struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body"`
}

// ChallengeType discriminates a unified Challenges-list entry.
type ChallengeType string

const (
	ChallengeMonster    ChallengeType = "monster"
	ChallengeHazard     ChallengeType = "hazard"
	ChallengeAffliction ChallengeType = "affliction"
	ChallengeSkillCheck ChallengeType = "skill_check"
	ChallengeMarkdown   ChallengeType = "markdown"
)

// ChallengeItem is one entry in an encounter's ordered Challenges list, unifying the
// formerly-separate monsters/hazards/afflictions/skill_checks/challenge_blocks arrays so
// their interleaved order (and the GM's drag-reordering) is preserved. Exactly one
// payload is set, keyed by Type: Monster carries monster/hazard/affliction (all
// MonsterEntry-shaped — Type distinguishes them for XP); SkillCheck and Markdown carry
// their own. ID is a stable client-assigned key the reorder UI drags by. Encounters
// created before this field migrate their legacy arrays into Challenges on the client.
type ChallengeItem struct {
	ID         string        `json:"id"`
	Type       ChallengeType `json:"type"`
	Monster    *MonsterEntry `json:"monster,omitempty"`
	SkillCheck *SkillCheck   `json:"skill_check,omitempty"`
	Markdown   *TextBlock    `json:"markdown,omitempty"`
}

type Encounter struct {
	ID              string          `json:"id"`
	CampaignID      string          `json:"campaign_id"`
	Name            string          `json:"name"`
	Status          Status          `json:"status"`
	ChapterID       string          `json:"chapter_id,omitempty"`
	Description     string          `json:"description,omitempty"` // legacy single body; migrated into TextBlocks on save
	TextBlocks      []TextBlock     `json:"text_blocks,omitempty"`
	ChallengeBlocks []TextBlock     `json:"challenge_blocks,omitempty"` // markdown sections under the Challenges tab
	Notes           string          `json:"notes,omitempty"`
	Monsters        []MonsterEntry  `json:"monsters"`
	Hazards         []MonsterEntry  `json:"hazards,omitempty"`
	Afflictions     []MonsterEntry  `json:"afflictions,omitempty"`
	Treasure        []TreasureLine  `json:"treasure"`
	TreasurePools   []TreasurePool  `json:"treasure_pools,omitempty"`
	XPAwards        []XPAward       `json:"xp_awards,omitempty"`
	RoomType        RoomType        `json:"room_type,omitempty"`
	Rewards         []Reward        `json:"rewards,omitempty"`
	SkillChecks     []SkillCheck    `json:"skill_checks,omitempty"`
	Challenges      []ChallengeItem `json:"challenges,omitempty"` // unified ordered list; supersedes the monsters/hazards/afflictions/skill_checks/challenge_blocks arrays above (migrated on the client)
	Exits           []Exit          `json:"exits,omitempty"`
	Currency        Currency        `json:"currency"`
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
	PartyLevel *int `json:"party_level,omitempty"`
	PartySize  *int `json:"party_size,omitempty"`
	// MapPositions is the GM's hand-arranged chapter-map layout: a {encounter_id:{x,y}}
	// blob the connectivity map seeds node positions from. Opaque to the API (stored +
	// round-tripped, not interpreted); the frontend reconciles it against the current
	// rooms (auto-place added, prune removed).
	MapPositions json.RawMessage `json:"map_positions,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ChapterInput is the client-writable shape for create/update (server owns
// id/campaign/timestamps). Order + MapPositions are touch-only-when-supplied (omit =
// leave unchanged) so a rename/party edit that doesn't send them can't wipe them.
type ChapterInput struct {
	Name         string           `json:"name"`
	Order        *int             `json:"order,omitempty"`
	PartyLevel   *int             `json:"party_level,omitempty"`
	PartySize    *int             `json:"party_size,omitempty"`
	MapPositions *json.RawMessage `json:"map_positions,omitempty"`
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
var validRoomTypes = map[RoomType]bool{
	RoomCombat: true, RoomHazard: true, RoomHaunt: true, RoomExploration: true,
	RoomSocial: true, RoomKnowledge: true, RoomEmpty: true,
}
var validRewardKinds = map[RewardKind]bool{
	RewardInformation: true, RewardRitual: true, RewardAlly: true, RewardItem: true,
}
var validChallengeTypes = map[ChallengeType]bool{
	ChallengeMonster: true, ChallengeHazard: true, ChallengeAffliction: true,
	ChallengeSkillCheck: true, ChallengeMarkdown: true,
}

// EncounterInput is the client-writable shape for create/update. Keeping it
// separate from Encounter is the invariant: server-owned fields (id, campaign,
// status lifecycle, timestamps) simply aren't in this struct, so a client can't
// set them — the handler maps the validated input onto a server-owned Encounter.
type EncounterInput struct {
	Name            string          `json:"name"`
	ChapterID       string          `json:"chapter_id,omitempty"`
	Description     string          `json:"description,omitempty"`
	TextBlocks      []TextBlock     `json:"text_blocks,omitempty"`
	ChallengeBlocks []TextBlock     `json:"challenge_blocks,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	Monsters        []MonsterEntry  `json:"monsters,omitempty"`
	Hazards         []MonsterEntry  `json:"hazards,omitempty"`
	Afflictions     []MonsterEntry  `json:"afflictions,omitempty"`
	Treasure        []TreasureLine  `json:"treasure,omitempty"`
	TreasurePools   []TreasurePool  `json:"treasure_pools,omitempty"`
	XPAwards        []XPAward       `json:"xp_awards,omitempty"`
	RoomType        RoomType        `json:"room_type,omitempty"`
	Rewards         []Reward        `json:"rewards,omitempty"`
	SkillChecks     []SkillCheck    `json:"skill_checks,omitempty"`
	Challenges      []ChallengeItem `json:"challenges,omitempty"`
	Exits           []Exit          `json:"exits,omitempty"`
	Currency        Currency        `json:"currency"`
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
		for j := range m.Loadout {
			l := &m.Loadout[j]
			if l.Qty < 1 {
				return fmt.Errorf("monster[%d].loadout[%d]: qty must be >= 1", i, j)
			}
			if l.Ref.isEmpty() {
				return fmt.Errorf("monster[%d].loadout[%d]: ref must reference content", i, j)
			}
		}
	}
	// Hazards reuse MonsterEntry (ref + count; a haunt/hazard has no elite/weak, so
	// adjustment normalizes to none). Same ref/count invariants as monsters.
	for i := range in.Hazards {
		h := &in.Hazards[i]
		if h.Count < 1 {
			return fmt.Errorf("hazard[%d]: count must be >= 1", i)
		}
		if h.Ref.isEmpty() {
			return fmt.Errorf("hazard[%d]: ref must reference content (game_id, base, or json)", i)
		}
		// A hazard has no elite/weak — force none so a stray adjustment can't persist.
		h.Adjustment = AdjustmentNone
	}
	// Afflictions (curses/diseases) reuse MonsterEntry the same way — ref + count, no
	// elite/weak.
	for i := range in.Afflictions {
		a := &in.Afflictions[i]
		if a.Count < 1 {
			return fmt.Errorf("affliction[%d]: count must be >= 1", i)
		}
		if a.Ref.isEmpty() {
			return fmt.Errorf("affliction[%d]: ref must reference content (game_id, base, or json)", i)
		}
		a.Adjustment = AdjustmentNone
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
	for i := range in.XPAwards {
		if in.XPAwards[i].Amount < 1 {
			return fmt.Errorf("xp_award[%d]: amount must be >= 1", i)
		}
	}
	if in.RoomType == "" {
		in.RoomType = RoomCombat
	} else if !validRoomTypes[in.RoomType] {
		return fmt.Errorf("invalid room_type %q", in.RoomType)
	}
	for i := range in.Rewards {
		r := &in.Rewards[i]
		if r.Kind == "" {
			return fmt.Errorf("reward[%d]: kind is required", i)
		}
		if !validRewardKinds[r.Kind] {
			return fmt.Errorf("reward[%d]: invalid kind %q", i, r.Kind)
		}
		if r.Label == "" {
			return fmt.Errorf("reward[%d]: label is required", i)
		}
	}
	for i := range in.SkillChecks {
		s := &in.SkillChecks[i]
		if s.Skill == "" {
			return fmt.Errorf("skill_check[%d]: skill is required", i)
		}
		if s.DC < 1 {
			return fmt.Errorf("skill_check[%d]: dc must be >= 1", i)
		}
		if s.Successes < 0 {
			return fmt.Errorf("skill_check[%d]: successes must be >= 0", i)
		}
		for j := range s.Alternatives {
			a := &s.Alternatives[j]
			if a.Skill == "" {
				return fmt.Errorf("skill_check[%d].alternative[%d]: skill is required", i, j)
			}
			if a.DC < 1 {
				return fmt.Errorf("skill_check[%d].alternative[%d]: dc must be >= 1", i, j)
			}
		}
	}
	// Challenges is the unified successor to the arrays above; validate each entry by
	// type using the same per-kind rules. The client sends only complete items (empty
	// placeholders are dropped on save, like today's monster rows), so strict here.
	for i := range in.Challenges {
		c := &in.Challenges[i]
		if !validChallengeTypes[c.Type] {
			return fmt.Errorf("challenge[%d]: invalid type %q", i, c.Type)
		}
		switch c.Type {
		case ChallengeMonster, ChallengeHazard, ChallengeAffliction:
			m := c.Monster
			if m == nil {
				return fmt.Errorf("challenge[%d]: %s requires a monster payload", i, c.Type)
			}
			if m.Count < 1 {
				return fmt.Errorf("challenge[%d]: count must be >= 1", i)
			}
			if m.Ref.isEmpty() {
				return fmt.Errorf("challenge[%d]: ref must reference content (game_id, base, or json)", i)
			}
			// Only a monster can be elite/weak; a hazard/affliction normalizes to none.
			if c.Type == ChallengeMonster {
				if m.Adjustment == "" {
					m.Adjustment = AdjustmentNone
				} else if !validAdjustments[m.Adjustment] {
					return fmt.Errorf("challenge[%d]: invalid adjustment %q", i, m.Adjustment)
				}
			} else {
				m.Adjustment = AdjustmentNone
			}
			for j := range m.Loadout {
				l := &m.Loadout[j]
				if l.Qty < 1 {
					return fmt.Errorf("challenge[%d].loadout[%d]: qty must be >= 1", i, j)
				}
				if l.Ref.isEmpty() {
					return fmt.Errorf("challenge[%d].loadout[%d]: ref must reference content", i, j)
				}
			}
		case ChallengeSkillCheck:
			s := c.SkillCheck
			if s == nil {
				return fmt.Errorf("challenge[%d]: skill_check requires a skill_check payload", i)
			}
			if s.Skill == "" {
				return fmt.Errorf("challenge[%d]: skill is required", i)
			}
			if s.DC < 1 {
				return fmt.Errorf("challenge[%d]: dc must be >= 1", i)
			}
			if s.Successes < 0 {
				return fmt.Errorf("challenge[%d]: successes must be >= 0", i)
			}
			for j := range s.Alternatives {
				a := &s.Alternatives[j]
				if a.Skill == "" {
					return fmt.Errorf("challenge[%d].alternative[%d]: skill is required", i, j)
				}
				if a.DC < 1 {
					return fmt.Errorf("challenge[%d].alternative[%d]: dc must be >= 1", i, j)
				}
			}
		case ChallengeMarkdown:
			if c.Markdown == nil {
				return fmt.Errorf("challenge[%d]: markdown requires a markdown payload", i)
			}
		}
	}
	// Exits are NOT validated for content: a blank exit row (no target, no label) is a
	// legitimate placeholder the GM adds via "+ exit" and fills in later, so it must
	// persist rather than be dropped/rejected. Stored as-is; the frontend's connectivity
	// map simply renders nothing for a blank exit.
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
