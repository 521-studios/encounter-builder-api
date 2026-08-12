package model

import (
	"encoding/json"
	"testing"
)

func TestEncounterInput_Validate(t *testing.T) {
	pristine := ContentRef{GameID: "abc"}
	cases := map[string]struct {
		in      EncounterInput
		wantErr bool
	}{
		"ok minimal":        {EncounterInput{Name: "x"}, false},
		"missing name":      {EncounterInput{}, true},
		"monster no ref":    {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Count: 1}}}, true},
		"monster count 0":   {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Ref: pristine, Count: 0}}}, true},
		"bad adjustment":    {EncounterInput{Name: "x", Monsters: []MonsterEntry{{Ref: pristine, Count: 1, Adjustment: "huge"}}}, true},
		"treasure no ref":   {EncounterInput{Name: "x", Treasure: []TreasureLine{{Qty: 1}}}, true},
		"treasure qty 0":    {EncounterInput{Name: "x", Treasure: []TreasureLine{{Ref: pristine, Qty: 0}}}, true},
		"negative currency": {EncounterInput{Name: "x", Currency: Currency{GP: -1}}, true},
	}
	for name, tc := range cases {
		in := tc.in
		err := in.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", name, err, tc.wantErr)
		}
	}
}

func TestEncounterInput_ValidateNormalizesEnums(t *testing.T) {
	in := EncounterInput{
		Name:     "x",
		Monsters: []MonsterEntry{{Ref: ContentRef{GameID: "g"}, Count: 1}},
		Treasure: []TreasureLine{{Ref: ContentRef{JSON: json.RawMessage(`{}`)}, Qty: 1}},
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if in.Monsters[0].Adjustment != AdjustmentNone {
		t.Errorf("adjustment = %q, want none", in.Monsters[0].Adjustment)
	}
	if in.Treasure[0].SaleClass != SaleNormal || in.Treasure[0].State != TreasureIntact {
		t.Errorf("treasure enums = %q/%q, want normal/intact", in.Treasure[0].SaleClass, in.Treasure[0].State)
	}
}

func TestContentRef_isEmpty(t *testing.T) {
	if !(ContentRef{}).isEmpty() {
		t.Error("zero ref should be empty")
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
