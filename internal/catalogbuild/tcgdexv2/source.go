// Package tcgdexv2 translates the JSON the TCGdex v2 compiler generates into
// bundled-catalog archive records.
package tcgdexv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	cardsFile  = "cards.json"
	setsFile   = "sets.json"
	seriesFile = "series.json"
)

// The compiler writes one array per file. These types mirror the fields the
// application maps. Unknown fields are ignored here and preserved in the source
// snapshot instead.
type sourceSet struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Logo        string          `json:"logo"`
	Symbol      string          `json:"symbol"`
	ReleaseDate string          `json:"releaseDate"`
	Serie       sourceNamed     `json:"serie"`
	CardCount   sourceCardCount `json:"cardCount"`
	Cards       []sourceCardRef `json:"cards"`
}

type sourceCardRef struct {
	ID string `json:"id"`
}

type sourceNamed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type sourceCardCount struct {
	Official int `json:"official"`
	Total    int `json:"total"`
}

type sourceSeries struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type sourceCard struct {
	ID               string             `json:"id"`
	LocalID          string             `json:"localId"`
	Name             string             `json:"name"`
	Image            string             `json:"image"`
	Category         string             `json:"category"`
	Rarity           string             `json:"rarity"`
	Illustrator      string             `json:"illustrator"`
	RegulationMark   string             `json:"regulationMark"`
	Description      string             `json:"description"`
	Effect           string             `json:"effect"`
	HP               *int               `json:"hp"`
	Types            []string           `json:"types"`
	EvolveFrom       string             `json:"evolveFrom"`
	Weight           string             `json:"weight"`
	Stage            string             `json:"stage"`
	Suffix           string             `json:"suffix"`
	DexIDs           sourceDexIDs       `json:"dexId"`
	Level            sourceTextNumber   `json:"level"`
	Item             *sourceTextBlock   `json:"item"`
	Abilities        []sourceTextBlock  `json:"abilities"`
	Attacks          []sourceTextBlock  `json:"attacks"`
	Weaknesses       []sourceTypeValue  `json:"weaknesses"`
	Resistances      []sourceTypeValue  `json:"resistances"`
	Retreat          *int               `json:"retreat"`
	TrainerType      string             `json:"trainerType"`
	EnergyType       string             `json:"energyType"`
	Set              sourceNamed        `json:"set"`
	VariantsDetailed []sourceVariant    `json:"variants_detailed"`
	Variants         sourceCardVariants `json:"variants"`

	// variantsDetailedPresent records whether the upstream object contained
	// the field at all. Absence means "unknown", not "no printings".
	variantsDetailedPresent bool
}

type sourceCardVariants struct {
	FirstEdition bool `json:"firstEdition"`
}

type sourceTextBlock struct {
	Type   string   `json:"type,omitempty"`
	Name   string   `json:"name,omitempty"`
	Effect string   `json:"effect,omitempty"`
	Damage any      `json:"damage,omitempty"`
	Cost   []string `json:"cost,omitempty"`
}

type sourceTypeValue struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

type sourceVariant struct {
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	Size      string   `json:"size"`
	Stamp     []string `json:"stamp"`
	Foil      string   `json:"foil"`
	VariantID string   `json:"variantId"`
}

// UnmarshalJSON keeps the presence of variants_detailed, which the projection
// serializes explicitly into the archive.
func (c *sourceCard) UnmarshalJSON(data []byte) error {
	type plain sourceCard
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	raw, present := fields["variants_detailed"]
	decoded.variantsDetailedPresent = present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	*c = sourceCard(decoded)
	return nil
}

// sourceTextNumber accepts the level field in both representations TCGdex
// publishes, a number or a string such as "X".
type sourceTextNumber string

// sourceDexIDs holds national Pokedex numbers. TCGdex occasionally records a
// non-integral value there: the Japanese delta-species Rayquaza cards carry
// 384.1. That is not a Pokedex number and has nowhere to go in the card model,
// so it is left out instead of failing the build. The source snapshot keeps
// the provider value as published.
type sourceDexIDs []int

func (v *sourceDexIDs) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*v = nil
		return nil
	}
	var numbers []json.Number
	if err := json.Unmarshal(data, &numbers); err != nil {
		return fmt.Errorf("dexId must be a list of numbers: %w", err)
	}
	ids := make([]int, 0, len(numbers))
	for _, number := range numbers {
		id, err := strconv.Atoi(number.String())
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	*v = ids
	return nil
}

func (v *sourceTextNumber) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*v = ""
		return nil
	}
	var text string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*v = sourceTextNumber(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return fmt.Errorf("level must be a string or number: %w", err)
	}
	if _, err := strconv.ParseFloat(number.String(), 64); err != nil {
		return fmt.Errorf("level must be a string or number: %w", err)
	}
	*v = sourceTextNumber(number.String())
	return nil
}

// languageData is one compiled language, indexed for lookup.
type languageData struct {
	Sets     map[string]sourceSet
	SetOrder []string
	Cards    map[string]sourceCard
	Series   []sourceSeries

	// Duplicate ids kept out of the index, recorded only for a language read
	// outside the application scope. See readLanguage.
	DuplicateSets  []string
	DuplicateCards []string
}

// Outside the application scope, duplicate IDs are retained only in the snapshot
// and reported as findings. For example, zh-cn publishes two sets as "CSV1C".
func readLanguage(generatedDir, language string, strict bool) (languageData, error) {
	data := languageData{
		Sets:  map[string]sourceSet{},
		Cards: map[string]sourceCard{},
	}

	var sets []sourceSet
	if err := readJSONArray(filepath.Join(generatedDir, language, setsFile), &sets); err != nil {
		return languageData{}, err
	}
	for _, set := range sets {
		id := strings.TrimSpace(set.ID)
		if id == "" {
			return languageData{}, fmt.Errorf("%s %s: a set has no id", language, setsFile)
		}
		if _, exists := data.Sets[id]; exists {
			if strict {
				return languageData{}, fmt.Errorf("%s %s: set %q appears twice", language, setsFile, id)
			}
			data.DuplicateSets = append(data.DuplicateSets, id)
			continue
		}
		set.ID = id
		data.Sets[id] = set
		data.SetOrder = append(data.SetOrder, id)
	}

	var cards []sourceCard
	if err := readJSONArray(filepath.Join(generatedDir, language, cardsFile), &cards); err != nil {
		return languageData{}, err
	}
	for _, card := range cards {
		id := strings.TrimSpace(card.ID)
		if id == "" {
			return languageData{}, fmt.Errorf("%s %s: a card has no id", language, cardsFile)
		}
		if _, exists := data.Cards[id]; exists {
			if strict {
				return languageData{}, fmt.Errorf("%s %s: card %q appears twice", language, cardsFile, id)
			}
			data.DuplicateCards = append(data.DuplicateCards, id)
			continue
		}
		card.ID = id
		data.Cards[id] = card
	}

	// series.json and stats.json are part of the compiler output the maintainer
	// keeps. Only the series list feeds the source inventory.
	if err := readJSONArray(filepath.Join(generatedDir, language, seriesFile), &data.Series); err != nil {
		return languageData{}, err
	}
	return data, nil
}

func readJSONArray(path string, target any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// discoverLanguages lists every language the compiler produced, which is the
// source inventory and is wider than the application scope.
func discoverLanguages(generatedDir string) ([]string, error) {
	entries, err := os.ReadDir(generatedDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", generatedDir, err)
	}
	languages := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(generatedDir, entry.Name(), cardsFile)); err != nil {
			continue
		}
		languages = append(languages, entry.Name())
	}
	if len(languages) == 0 {
		return nil, fmt.Errorf("read %s: no compiled language contains %s", generatedDir, cardsFile)
	}
	return languages, nil
}
