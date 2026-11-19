package catalogdata

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// FixtureBuilderVersion identifies the fixture generator. Changing it requires
// regenerating the fixture checked into SOMEONE'S PC.
const FixtureBuilderVersion = "fixture/1"

var fixtureEpoch = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// FixtureInput returns the complete build input for the development fixture:
// two English sets with localizations, plus a source snapshot that also
// carries the catalogs, languages and fields the application does not use.
func FixtureInput() BuildInput {
	source, dirs := fixtureSource()
	return BuildInput{
		Kind:            KindFixture,
		Revision:        0,
		SourceAPI:       "v2",
		SourceTag:       "fixture",
		SourceCommit:    "0000000000000000000000000000000000000000",
		BuilderVersion:  FixtureBuilderVersion,
		CompilerImageID: "fixture",
		AppLanguages:    AppLanguages,
		SourceArchive:   source,
		AssetsIndex:     fixtureAssetsIndex,
		SourceDirs:      dirs,
		// The snapshot keeps languages and series the projection drops. The
		// inventory names them so a reader can see the retention without
		// opening the source archive.
		SourceLanguages: []string{"en", "it", "fr", "de", "es", "pt", "ja", "zh-tw"},
		SourceSeries:    []string{"fx", "tcgp"},
		Sets:            []SetRecord{fixtureSetA(), fixtureSetB()},
		Findings: []Finding{
			{
				Code:    FindingSourceUnprojected,
				Message: "Pocket series tcgp and languages ja, zh-tw are retained in the source snapshot and not projected",
			},
			{
				Code:     FindingLocalizationMissing,
				SetID:    "fxb",
				Language: "fr",
				Message:  "Upstream has no French translation for this set",
			},
		},
	}
}

// BuildFixture writes the development fixture archive.
func BuildFixture(w io.Writer) (BuildResult, error) {
	return Build(w, FixtureInput())
}

func fixtureAsset(parts ...string) string {
	return "https://" + AssetHost + "/" + strings.Join(parts, "/")
}

func fixtureSet(id, name, language string) Set {
	return Set{
		ID:                id,
		Name:              name,
		Logo:              fixtureAsset(language, "fx", id, "logo"),
		Symbol:            fixtureAsset(language, "fx", id, "symbol"),
		ReleaseDate:       "2024-02-01",
		SeriesID:          "fx",
		SeriesName:        "Fixture Series",
		CardCountOfficial: 3,
		CardCountTotal:    3,
	}
}

func fixtureCard(setID, localID, name, language string) Card {
	hp := 60
	retreat := 1
	return Card{
		ID:             setID + "-" + localID,
		LocalID:        localID,
		Name:           name,
		Image:          fixtureAsset(language, "fx", setID, localID),
		Category:       "Pokemon",
		Rarity:         "Common",
		Illustrator:    "Fixture Artist",
		RegulationMark: "G",
		Effect:         "",
		HP:             &hp,
		Types:          []string{"Grass"},
		Stage:          "Basic",
		DexIDs:         []int{1},
		Level:          "12",
		Attacks: []TextBlock{
			{Name: "Fixture Strike", Effect: "Does nothing.", Damage: float64(20), Cost: []string{"Grass"}},
		},
		Weaknesses: []TypeValue{{Type: "Fire", Value: "×2"}},
		Retreat:    &retreat,
		SetID:      setID,
		SetName:    "Fixture " + strings.ToUpper(setID),
	}
}

func fixtureSetA() SetRecord {
	english := fixtureSet("fxa", "Fixture Alpha", "en")

	// A card whose provider variants are present and describe two distinct
	// physical printings, one of them First Edition stamped.
	first := fixtureCard("fxa", "1", "Fixture Bulba", "en")
	first.VariantsDetailedPresent = true
	first.VariantsFirstEdition = true
	first.VariantsDetailed = []Variant{
		{Type: "normal", Size: "standard", Stamp: []string{}, VariantID: "fxa-1-normal"},
		{Type: "holo", Size: "standard", Stamp: []string{"1st-edition"}, Foil: "cosmos", VariantID: "fxa-1-holo"},
	}

	// A card whose provider variants are an explicit empty array: the
	// application may retire its printings.
	second := fixtureCard("fxa", "2", "Fixture Squirt", "en")
	second.VariantsDetailedPresent = true
	second.VariantsDetailed = []Variant{}

	// A card with no variants_detailed field at all: unknown, so existing
	// printings are preserved rather than retired.
	third := fixtureCard("fxa", "3", "Fixture Char", "en")
	third.VariantsDetailedPresent = false

	localizedSet := func(language, name string) SetLocalization {
		set := fixtureSet("fxa", name, language)
		return SetLocalization{Language: language, Set: set}
	}
	localizedCard := func(card Card, language, name string) CardLocalization {
		localized := fixtureCard("fxa", card.LocalID, name, language)
		localized.VariantsDetailed = card.VariantsDetailed
		localized.VariantsDetailedPresent = card.VariantsDetailedPresent
		localized.VariantsFirstEdition = card.VariantsFirstEdition
		return CardLocalization{Language: language, Card: localized}
	}

	return SetRecord{
		Set:           english,
		CardsComplete: true,
		Localizations: []SetLocalization{
			localizedSet("it", "Fixture Alfa"),
			localizedSet("fr", "Fixture Alpha FR"),
		},
		Cards: []CardRecord{
			{Card: first, Localizations: []CardLocalization{
				localizedCard(first, "it", "Bulba Fixture"),
				localizedCard(first, "fr", "Bulba Fixture FR"),
			}},
			{Card: second, Localizations: []CardLocalization{
				localizedCard(second, "it", "Squirt Fixture"),
			}},
			{Card: third, Localizations: nil},
		},
	}
}

func fixtureSetB() SetRecord {
	english := fixtureSet("fxb", "Fixture Beta", "en")
	english.CardCountOfficial = 2
	english.CardCountTotal = 2

	first := fixtureCard("fxb", "1", "Fixture Pika", "en")
	first.VariantsDetailedPresent = true
	first.VariantsDetailed = []Variant{
		{Type: "normal", Size: "standard", Stamp: []string{}, VariantID: "fxb-1-normal"},
	}
	second := fixtureCard("fxb", "2", "Fixture Eevee", "en")
	second.Category = "Trainer"
	second.HP = nil
	second.Types = nil
	second.Stage = ""
	second.DexIDs = nil
	second.Attacks = nil
	second.Weaknesses = nil
	second.Retreat = nil
	second.Level = ""
	second.TrainerType = "Item"
	second.VariantsDetailedPresent = true
	second.VariantsDetailed = []Variant{
		{Type: "normal", Size: "standard", Stamp: []string{}, VariantID: "fxb-2-normal"},
	}

	italianSet := fixtureSet("fxb", "Fixture Beta IT", "it")
	italianSet.CardCountOfficial = 2
	italianSet.CardCountTotal = 2

	localizedCard := func(card Card, name string) CardLocalization {
		localized := fixtureCard("fxb", card.LocalID, name, "it")
		localized.Category = card.Category
		localized.HP = card.HP
		localized.Types = card.Types
		localized.Stage = card.Stage
		localized.DexIDs = card.DexIDs
		localized.Attacks = card.Attacks
		localized.Weaknesses = card.Weaknesses
		localized.Retreat = card.Retreat
		localized.Level = card.Level
		localized.TrainerType = card.TrainerType
		localized.VariantsDetailed = card.VariantsDetailed
		localized.VariantsDetailedPresent = card.VariantsDetailedPresent
		return CardLocalization{Language: "it", Card: localized}
	}

	return SetRecord{
		Set:           english,
		CardsComplete: true,
		Localizations: []SetLocalization{{Language: "it", Set: italianSet}},
		Cards: []CardRecord{
			{Card: first, Localizations: []CardLocalization{localizedCard(first, "Pika Fixture")}},
			{Card: second, Localizations: []CardLocalization{localizedCard(second, "Eevee Fixture")}},
		},
	}
}

var fixtureAssetsIndex = []byte(`{"en/fx/fxa/1":["high.webp","low.webp"],"en/fx/fxb/1":["high.webp","low.webp"]}` + "\n")

// fixtureSourceFiles is the synthetic upstream checkout. It keeps a Pocket
// series, two catalogs outside the application languages and fields the
// application never maps, which the retention tests look for.
var fixtureSourceFiles = []struct {
	Path string
	Body string
}{
	{"LICENSE", "Fixture upstream license. Not a real TCGdex license.\n"},
	{"server/generated/en/series.json", `[{"id":"fx","name":"Fixture Series"},{"id":"tcgp","name":"Pocket"}]` + "\n"},
	{"server/generated/en/sets.json", `[{"id":"fxa","name":"Fixture Alpha","serie":{"id":"fx"},"boosters":["fixture-booster"]},{"id":"tcgp1","name":"Pocket Fixture","serie":{"id":"tcgp"}}]` + "\n"},
	{"server/generated/en/cards.json", `[{"id":"fxa-1","name":"Fixture Bulba","variants_detailed":[{"type":"normal","thirdParty":{"cardmarket":12345}},{"type":"reverse","thirdParty":{"cardmarket":12346}}],"pricing":{"tcgplayer":{"unit":"USD"}},"updated":"2024-01-01"}]` + "\n"},
	{"server/generated/en/stats.json", `{"cards":3,"sets":2}` + "\n"},
	{"server/generated/it/cards.json", `[{"id":"fxa-1","name":"Bulba Fixture"}]` + "\n"},
	{"server/generated/fr/cards.json", `[{"id":"fxa-1","name":"Bulba Fixture FR"}]` + "\n"},
	{"server/generated/ja/cards.json", `[{"id":"fxa-1","name":"フィクスチャ"}]` + "\n"},
	{"server/generated/zh-tw/cards.json", `[{"id":"fxa-1","name":"固定卡"}]` + "\n"},
	{"server/compiler/utils/util.ts", "export const fixture = true;\n"},
}

// fixtureSource builds a deterministic tar.gz of the synthetic checkout and
// the directory inventory that describes it.
func fixtureSource() ([]byte, []SourceDirRow) {
	var raw bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&raw, gzip.BestCompression)
	if err != nil {
		panic(fmt.Sprintf("catalogdata fixture: %v", err))
	}
	tarWriter := tar.NewWriter(gzipWriter)
	stats := map[string]*SourceDirRow{}
	for _, file := range fixtureSourceFiles {
		header := &tar.Header{
			Name:     file.Path,
			Mode:     0o644,
			Size:     int64(len(file.Body)),
			ModTime:  fixtureEpoch,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			panic(fmt.Sprintf("catalogdata fixture: %v", err))
		}
		if _, err := tarWriter.Write([]byte(file.Body)); err != nil {
			panic(fmt.Sprintf("catalogdata fixture: %v", err))
		}
		top := file.Path
		if index := strings.Index(top, "/"); index >= 0 {
			top = top[:index]
		} else {
			top = "."
		}
		if stats[top] == nil {
			stats[top] = &SourceDirRow{Path: top}
		}
		stats[top].Files++
		stats[top].Bytes += int64(len(file.Body))
	}
	if err := tarWriter.Close(); err != nil {
		panic(fmt.Sprintf("catalogdata fixture: %v", err))
	}
	if err := gzipWriter.Close(); err != nil {
		panic(fmt.Sprintf("catalogdata fixture: %v", err))
	}
	dirs := make([]SourceDirRow, 0, len(stats))
	for _, row := range stats {
		dirs = append(dirs, *row)
	}
	sort.Slice(dirs, func(a, b int) bool { return dirs[a].Path < dirs[b].Path })
	return raw.Bytes(), dirs
}
