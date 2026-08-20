package model

import (
	"encoding/json"
	"testing"
)

func TestEncounterInput_Validate(t *testing.T) {
	pristine := ContentRef{GameID: "abc"}
	ip := func(n int) *int { return &n }
	cases := map[string]struct {
		in      EncounterInput
		wantErr bool
	}{
		"ok minimal":        {EncounterInput{Name: "x"}, false},
		"missing name":      {EncounterInput{}, true},
		"monster no ref":    {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Count: 1}}}, true},
		"monster count 0":   {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Ref: pristine, Count: 0}}}, true},
		"bad adjustment":    {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Ref: pristine, Count: 1, Adjustment: "huge"}}}, true},
		"hazard ok":         {EncounterInput{Name: "x", Hazards: []MonsterEntry{{Ref: pristine, Count: 1}}}, false},
		"hazard no ref":     {EncounterInput{Name: "x", Hazards: []MonsterEntry{{Count: 1}}}, true},
		"hazard count 0":    {EncounterInput{Name: "x", Hazards: []MonsterEntry{{Ref: pristine, Count: 0}}}, true},
		"affliction ok":     {EncounterInput{Name: "x", Afflictions: []MonsterEntry{{Ref: pristine, Count: 1}}}, false},
		"affliction no ref": {EncounterInput{Name: "x", Afflictions: []MonsterEntry{{Count: 1}}}, true},
		"treasure no ref":   {EncounterInput{Name: "x", Treasure: []TreasureLine{{Qty: 1}}}, true},
		"treasure qty 0":    {EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 0}}}, true},
		"bad sale_class":    {EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 1, SaleClass: "junk"}}}, true},
		"bad state":         {EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 1, State: "vaporized"}}}, true},
		"negative currency": {EncounterInput{Name: "x", Currency: Currency{GP: -1}}, true},
		"value_tiers negative": {
			EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 1, ValueTiers: &ValueTiers{Success: ip(-1)}}}},
			true,
		},
		"pool missing id": {EncounterInput{Name: "x", TreasurePools: []TreasurePool{{Name: "altar"}}}, true},
		"pool gate negative dc": {
			EncounterInput{Name: "x", TreasurePools: []TreasurePool{{ID: "p1", Gate: &Gate{Skill: "Perception", DC: -1}}}},
			true,
		},
		"pool gate no skill": {
			EncounterInput{Name: "x", TreasurePools: []TreasurePool{{ID: "p1", Gate: &Gate{DC: 5}}}},
			true,
		},
		"value_tiers empty (no tier set)": {
			EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 1, ValueTiers: &ValueTiers{}}}},
			true,
		},
		"identify_dc negative": {
			EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 1, IdentifyDC: -1}}},
			true,
		},
		"pools + value_tiers ok": {
			EncounterInput{
				Name:          "x",
				TreasurePools: []TreasurePool{{ID: "p1", Name: "altar", Description: "# hidden", Gate: &Gate{Skill: "Perception", DC: 18}}},
				Treasure:      []TreasureLine{{Ref: pristine, Qty: 1, PoolID: "p1", ValueTiers: &ValueTiers{Success: ip(4000), Failure: ip(2000), CritFailure: ip(0)}}},
			},
			false,
		},
		"dangling pool_id is allowed (falls to default)": {
			EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 1, PoolID: "gone"}}},
			false,
		},
		"xp_award amount 0": {EncounterInput{Name: "x", XPAwards: []XPAward{{Reason: "ally"}}}, true},
		"xp_award negative": {EncounterInput{Name: "x", XPAwards: []XPAward{{Amount: -5}}}, true},
		"xp_award ok":       {EncounterInput{Name: "x", XPAwards: []XPAward{{Amount: 30, Reason: "gained Augrael as an ally"}}}, false},
		"bad room_type":     {EncounterInput{Name: "x", RoomType: "dungeon"}, true},
		"room_type ok":      {EncounterInput{Name: "x", RoomType: RoomKnowledge}, false},
		"reward bad kind":   {EncounterInput{Name: "x", Rewards: []Reward{{Kind: "xp", Label: "lore"}}}, true},
		"reward no kind":    {EncounterInput{Name: "x", Rewards: []Reward{{Label: "lore"}}}, true},
		"reward no label":   {EncounterInput{Name: "x", Rewards: []Reward{{Kind: RewardInformation}}}, true},
		"reward ok": {
			EncounterInput{Name: "x", Rewards: []Reward{{Kind: RewardItem, Label: "The Whispering Reeds", Description: "# a unique book"}}},
			false,
		},
		"skill_check no skill": {EncounterInput{Name: "x", SkillChecks: []SkillCheck{{DC: 12}}}, true},
		"skill_check dc 0":     {EncounterInput{Name: "x", SkillChecks: []SkillCheck{{Skill: "Perception"}}}, true},
		"skill_check ok": {
			EncounterInput{Name: "x", SkillChecks: []SkillCheck{{Skill: "Perception", DC: 12, Description: "spot the loose planks"}}},
			false,
		},
		"skill_check rich ok": {
			EncounterInput{Name: "x", SkillChecks: []SkillCheck{{
				Skill: "Thievery", DC: 25, Successes: 4,
				Alternatives: []SkillOption{{Skill: "Religion", DC: 20}},
				Outcomes:     &DegreeOutcomes{CritSuccess: "extra clue", Failure: "alarm"},
			}}},
			false,
		},
		"skill_check bad alternative dc": {
			EncounterInput{Name: "x", SkillChecks: []SkillCheck{{Skill: "Thievery", DC: 22, Alternatives: []SkillOption{{Skill: "Religion", DC: 0}}}}},
			true,
		},
		"skill_check bad alternative skill": {
			EncounterInput{Name: "x", SkillChecks: []SkillCheck{{Skill: "Thievery", DC: 22, Alternatives: []SkillOption{{DC: 20}}}}},
			true,
		},
		"skill_check negative successes": {
			EncounterInput{Name: "x", SkillChecks: []SkillCheck{{Skill: "Perception", DC: 12, Successes: -1}}},
			true,
		},
		"loadout qty 0":     {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Count: 1, Ref: ContentRef{GameID: "g"}, Loadout: []LoadoutItem{{Qty: 0, Ref: ContentRef{GameID: "w"}}}}}}, true},
		"loadout empty ref": {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Count: 1, Ref: ContentRef{GameID: "g"}, Loadout: []LoadoutItem{{Qty: 1}}}}}, true},
		"loadout ok":        {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Count: 1, Ref: ContentRef{GameID: "g"}, Loadout: []LoadoutItem{{Qty: 2, Ref: ContentRef{GameID: "w"}}}}}}, false},
		"exit empty ok":     {EncounterInput{Name: "x", Exits: []Exit{{}}}, false}, // blank placeholder rows persist
		"exit to target ok": {EncounterInput{Name: "x", Exits: []Exit{{ToEncounterID: "enc-a2"}}}, false},
		"exit external ok":  {EncounterInput{Name: "x", Exits: []Exit{{Label: "Exterior"}}}, false},
	}
	for name, tc := range cases {
		in := tc.in
		err := in.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", name, err, tc.wantErr)
		}
	}
}

func TestValidateParty_Boundaries(t *testing.T) {
	p := func(n int) *int { return &n }
	cases := map[string]struct {
		level, size *int
		wantErr     bool
	}{
		"both nil (inherit)": {nil, nil, false},
		"level 1 ok":         {p(1), nil, false},
		"level 20 ok":        {p(20), nil, false},
		"level 0":            {p(0), nil, true},
		"level 21":           {p(21), nil, true},
		"size 1 ok":          {nil, p(1), false},
		"size 0":             {nil, p(0), true},
		"both set ok":        {p(5), p(4), false},
	}
	for name, tc := range cases {
		if err := validateParty(tc.level, tc.size); (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", name, err, tc.wantErr)
		}
	}
}

func TestEncounterInput_ValidateNormalizesEnums(t *testing.T) {
	in := EncounterInput{
		Name:     "x",
		Monsters: []MonsterEntry{{Ref: ContentRef{GameID: "g"}, Count: 1}},
		// A hazard never has elite/weak — a stray adjustment must be forced to none.
		Hazards:     []MonsterEntry{{Ref: ContentRef{GameID: "h"}, Count: 1, Adjustment: "elite"}},
		Afflictions: []MonsterEntry{{Ref: ContentRef{GameID: "a"}, Count: 1, Adjustment: "weak"}},
		Treasure:    []TreasureLine{{Ref: ContentRef{JSON: json.RawMessage(`{}`)}, Qty: 1}},
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if in.Monsters[0].Adjustment != AdjustmentNone {
		t.Errorf("adjustment = %q, want none", in.Monsters[0].Adjustment)
	}
	if in.Hazards[0].Adjustment != AdjustmentNone {
		t.Errorf("hazard adjustment = %q, want none (forced)", in.Hazards[0].Adjustment)
	}
	if in.Afflictions[0].Adjustment != AdjustmentNone {
		t.Errorf("affliction adjustment = %q, want none (forced)", in.Afflictions[0].Adjustment)
	}
	if in.Treasure[0].SaleClass != SaleNormal || in.Treasure[0].State != TreasureIntact {
		t.Errorf("treasure enums = %q/%q, want normal/intact", in.Treasure[0].SaleClass, in.Treasure[0].State)
	}
	if in.RoomType != RoomCombat {
		t.Errorf("room_type = %q, want combat (default)", in.RoomType)
	}
}

func TestContentRef_isEmpty(t *testing.T) {
	for _, r := range []ContentRef{
		{},                                       // zero
		{Base: &ContentRef{}},                    // non-nil but empty base points at nothing
		{Base: &ContentRef{Base: &ContentRef{}}}, // recursively empty
	} {
		if !r.isEmpty() {
			t.Errorf("%+v should be empty", r)
		}
	}
	for _, r := range []ContentRef{
		{GameID: "g"},
		{Base: &ContentRef{GameID: "g"}},
		{JSON: json.RawMessage(`{}`)},
	} {
		if r.isEmpty() {
			t.Errorf("%+v should not be empty", r)
		}
	}
}

func TestIncompleteContent(t *testing.T) {
	goodRef := ContentRef{GameID: "g"}
	// A fully-finished, mixed encounter — plus a text-only room and an empty list —
	// must report NO gaps (the gate flags half-filled rows, not missing categories).
	complete := []ContentItem{
		{ID: "md", Type: ContentMarkdown, Markdown: &TextBlock{Body: "read aloud"}},
		{ID: "mon", Type: ContentMonster, Monster: &MonsterEntry{Ref: goodRef, Count: 2}},
		{ID: "sc", Type: ContentSkillCheck, SkillCheck: &SkillCheck{Skill: "Perception", DC: 12,
			Alternatives: []SkillOption{{Skill: "Nature", DC: 10}}}},
		{ID: "pool", Type: ContentPool, Pool: &PoolHeader{Name: "altar", Gate: &Gate{Skill: "Perception", DC: 18}}},
		{ID: "tr", Type: ContentTreasure, Treasure: &TreasureLine{Ref: goodRef, Qty: 1}},
		{ID: "coin", Type: ContentCoin, Coin: &Currency{GP: 5}},
		{ID: "xp", Type: ContentXPAward, XPAward: &XPAward{Amount: 30}},
		{ID: "rw", Type: ContentReward, Reward: &Reward{Kind: RewardInformation, Label: "lore"}},
	}
	if gaps := IncompleteContent(complete); len(gaps) != 0 {
		t.Fatalf("complete content reported gaps: %+v", gaps)
	}
	if gaps := IncompleteContent(nil); len(gaps) != 0 {
		t.Fatalf("empty content reported gaps: %+v", gaps)
	}

	// Each half-filled row reports its own missing field(s), keyed by item id.
	incomplete := []ContentItem{
		{ID: "md", Type: ContentBoxText, Markdown: &TextBlock{Body: "  "}}, // blank body
		{ID: "mon", Type: ContentMonster, Monster: &MonsterEntry{}},        // no ref, count 0
		{ID: "haz", Type: ContentHazard, Monster: &MonsterEntry{Ref: goodRef, Count: 1, // loadout row unset
			Loadout: []LoadoutItem{{}}}},
		{ID: "sc", Type: ContentSkillCheck, SkillCheck: &SkillCheck{Skill: "Perception"}}, // no DC
		{ID: "sc2", Type: ContentSkillCheck, SkillCheck: &SkillCheck{Skill: "Thievery", DC: 20, // bad alt
			Alternatives: []SkillOption{{Skill: "", DC: 0}}}},
		{ID: "pool", Type: ContentPool, Pool: &PoolHeader{Gate: &Gate{DC: 0}}}, // no name, bad gate
		{ID: "tr", Type: ContentTreasure, Treasure: &TreasureLine{Qty: 0}},     // no item, qty 0
		{ID: "coin", Type: ContentCoin, Coin: &Currency{}},                     // all zero
		{ID: "xp", Type: ContentXPAward, XPAward: &XPAward{}},                  // amount 0
		{ID: "rw", Type: ContentReward, Reward: &Reward{Kind: "bogus"}},        // bad kind, no label
	}
	gaps := IncompleteContent(incomplete)
	got := map[string][]string{}
	for _, g := range gaps {
		got[g.ItemID] = g.Missing
	}
	want := map[string][]string{
		"md":   {"text"},
		"mon":  {"creature", "count"},
		"haz":  {"equipment item"},
		"sc":   {"DC"},
		"sc2":  {"alternative skill/DC"},
		"pool": {"pool name", "gate skill", "gate DC"},
		"tr":   {"item", "quantity"},
		"coin": {"amount"},
		"xp":   {"XP amount"},
		"rw":   {"kind", "label"},
	}
	if len(got) != len(want) {
		t.Fatalf("gap count = %d, want %d; gaps=%+v", len(got), len(want), gaps)
	}
	for id, wm := range want {
		if !equalStrings(got[id], wm) {
			t.Errorf("item %q missing = %v, want %v", id, got[id], wm)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
