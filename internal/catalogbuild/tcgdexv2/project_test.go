package tcgdexv2

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

// The fixtures below are written as the TCGdex v2 compiler writes them: one
// JSON array per file under server/generated/<language>.

type generatedFiles struct {
	Series string
	Sets   string
	Cards  string
}

func writeGenerated(t *testing.T, files map[string]generatedFiles) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "generated")
	for language, content := range files {
		dir := filepath.Join(root, language)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
		for name, payload := range map[string]string{
			seriesFile: content.Series,
			setsFile:   content.Sets,
			cardsFile:  content.Cards,
		} {
			if payload == "" {
				payload = "[]"
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	return root
}

const englishSeries = `[{"id":"fx","name":"Fixture Series"},{"id":"tcgp","name":"Pocket"}]`

const englishSets = `[
  {"id":"fxa","name":"Fixture Alpha","logo":"https://assets.tcgdex.net/en/fx/fxa/logo","symbol":"https://assets.tcgdex.net/en/fx/fxa/symbol","releaseDate":"2024-02-01","serie":{"id":"fx","name":"Fixture Series"},"cardCount":{"official":2,"total":2},"cards":[{"id":"fxa-1"},{"id":"fxa-2"}],"boosters":["unmapped"]},
  {"id":"fxb","name":"Fixture Beta","serie":{"id":"fx","name":"Fixture Series"},"cardCount":{"official":3,"total":3},"cards":[]},
  {"id":"tcgp1","name":"Pocket Fixture","serie":{"id":"tcgp","name":"Pocket"},"cardCount":{"official":1,"total":1},"cards":[{"id":"tcgp1-1"}]}
]`

const englishCards = `[
  {"id":"fxa-1","localId":"1","name":"Fixture Bulba","image":"https://assets.tcgdex.net/en/fx/fxa/1","category":"Pokemon","hp":60,"types":["Grass"],"level":12,"set":{"id":"fxa","name":"Fixture Alpha"},"variants":{"firstEdition":true},"variants_detailed":[{"type":"normal","size":"standard","stamp":[],"variantId":"fxa-1-normal"},{"type":"holo","size":"standard","stamp":["1st-edition"],"foil":"cosmos","variantId":"fxa-1-holo"}],"pricing":{"tcgplayer":{}}},
  {"id":"fxa-2","localId":"2","name":"Fixture Squirt","image":"https://assets.tcgdex.net/en/fx/fxa/2","category":"Trainer","level":"X","set":{"id":"fxa","name":"Fixture Alpha"}},
  {"id":"tcgp1-1","localId":"1","name":"Pocket Card","set":{"id":"tcgp1","name":"Pocket Fixture"},"variants_detailed":[]}
]`

const italianSets = `[{"id":"fxa","name":"Fixture Alfa","serie":{"id":"fx","name":"Serie Fixture"},"cardCount":{"official":2,"total":2},"cards":[{"id":"fxa-1"},{"id":"fxa-2"}]}]`

const italianCards = `[
  {"id":"fxa-1","localId":"1","name":"Bulba Fixture","category":"Pokemon","hp":60,"set":{"id":"fxa"},"variants_detailed":[]},
  {"id":"fxa-2","localId":"99","name":"Squirt Fixture","set":{"id":"fxa"}}
]`

// French translates the set metadata but lists no cards, so it is not a
// usable card-catalog language.
const frenchSets = `[{"id":"fxa","name":"Fixture Alpha FR","serie":{"id":"fx"},"cardCount":{"official":2,"total":2},"cards":[]}]`

// Japanese is outside the application scope and must stay in the source only.
const japaneseSets = `[{"id":"fxa","name":"Fixture JA","serie":{"id":"fx"},"cardCount":{"official":2,"total":2},"cards":[{"id":"fxa-1"}]}]`
const japaneseCards = `[{"id":"fxa-1","localId":"1","name":"Fixture JA","set":{"id":"fxa"}}]`
const japaneseSeries = `[{"id":"fx","name":"Fixture JA"},{"id":"jpx","name":"Japan Only"}]`

func standardGenerated(t *testing.T) string {
	t.Helper()
	return writeGenerated(t, map[string]generatedFiles{
		"en":    {Series: englishSeries, Sets: englishSets, Cards: englishCards},
		"it":    {Series: englishSeries, Sets: italianSets, Cards: italianCards},
		"fr":    {Series: englishSeries, Sets: frenchSets, Cards: "[]"},
		"ja":    {Series: japaneseSeries, Sets: japaneseSets, Cards: japaneseCards},
		"zh-tw": {Series: englishSeries, Sets: "[]", Cards: "[]"},
	})
}

func TestProjectFiltersPocketAndUnsupportedLanguages(t *testing.T) {
	result, err := Project(standardGenerated(t))
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	ids := make([]string, 0, len(result.Sets))
	for _, record := range result.Sets {
		ids = append(ids, record.Set.ID)
	}
	if strings.Join(ids, ",") != "fxa,fxb" {
		t.Fatalf("projected sets %v, want the Pocket set excluded", ids)
	}
	if result.Report.SkippedPocketSets != 1 {
		t.Fatalf("skipped Pocket sets = %d, want 1", result.Report.SkippedPocketSets)
	}

	// The source inventory is discovered independently of the projection.
	if !slices.Contains(result.SourceLanguages, "ja") || !slices.Contains(result.SourceLanguages, "zh-tw") {
		t.Fatalf("source languages %v, want every compiled catalog", result.SourceLanguages)
	}
	if !slices.Contains(result.SourceSeries, "tcgp") || !slices.Contains(result.SourceSeries, "jpx") {
		t.Fatalf("source series %v, want every series the compiler produced", result.SourceSeries)
	}

	alpha := result.Sets[0]
	for _, localization := range alpha.Localizations {
		if !catalogdata.IsAppLanguage(localization.Language) {
			t.Fatalf("set fxa was projected with language %q", localization.Language)
		}
		if localization.Language == "fr" {
			t.Fatal("a translation without localized cards was projected")
		}
	}
	if len(alpha.Localizations) != 1 || alpha.Localizations[0].Language != "it" {
		t.Fatalf("set fxa localizations = %v, want Italian only", alpha.Localizations)
	}
	// The Italian set localization falls back to the English media.
	if alpha.Localizations[0].Set.Logo != alpha.Set.Logo {
		t.Fatalf("Italian logo = %q, want the English fallback %q", alpha.Localizations[0].Set.Logo, alpha.Set.Logo)
	}
	if alpha.Set.Logo != "https://assets.tcgdex.net/en/fx/fxa/logo.webp" {
		t.Fatalf("logo = %q, want the upstream extension convention applied", alpha.Set.Logo)
	}
	if alpha.Set.Symbol != "https://assets.tcgdex.net/en/fx/fxa/symbol.png" {
		t.Fatalf("symbol = %q, want the upstream extension convention applied", alpha.Set.Symbol)
	}
}

func TestProjectRecordsUnavailableTranslationsWithoutFailing(t *testing.T) {
	result, err := Project(standardGenerated(t))
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	byCode := map[string][]catalogdata.Finding{}
	for _, finding := range result.Findings {
		byCode[finding.Code] = append(byCode[finding.Code], finding)
	}
	if len(byCode[catalogdata.FindingLocalizationMissing]) == 0 {
		t.Fatal("no unavailable translation was reported")
	}
	if len(byCode[catalogdata.FindingSourceUnprojected]) != 1 {
		t.Fatalf("source retention findings = %d, want one", len(byCode[catalogdata.FindingSourceUnprojected]))
	}
	retention := byCode[catalogdata.FindingSourceUnprojected][0].Message
	for _, expected := range []string{"Pocket", "ja", "zh-tw"} {
		if !strings.Contains(retention, expected) {
			t.Fatalf("retention finding %q does not mention %q", retention, expected)
		}
	}

	// A card renumbered against English is reported and dropped, never shipped
	// as a record the reader would refuse.
	renumbered := false
	for _, finding := range byCode[catalogdata.FindingLocalizationMissing] {
		if finding.Language == "it" && strings.Contains(finding.Message, "fxa-2") {
			renumbered = true
		}
	}
	if !renumbered {
		t.Fatal("the renumbered Italian card was not reported")
	}
	for _, card := range result.Sets[0].Cards {
		if card.Card.ID != "fxa-2" {
			continue
		}
		if len(card.Localizations) != 0 {
			t.Fatal("the renumbered Italian card was projected anyway")
		}
	}
}

func TestProjectMarksAdvertisedEmptySetIncomplete(t *testing.T) {
	result, err := Project(standardGenerated(t))
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	beta := result.Sets[1]
	if beta.Set.ID != "fxb" {
		t.Fatalf("second set is %q", beta.Set.ID)
	}
	if beta.CardsComplete {
		t.Fatal("a set advertising cards and listing none was marked complete")
	}
	if !strings.Contains(beta.IncompleteReason, "announces 3") {
		t.Fatalf("incomplete reason = %q", beta.IncompleteReason)
	}
	found := false
	for _, finding := range result.Findings {
		if finding.Code == catalogdata.FindingSetIncomplete && finding.SetID == "fxb" {
			found = true
		}
	}
	if !found {
		t.Fatal("the incomplete set produced no finding")
	}
}

func TestProjectPreservesVariantPresence(t *testing.T) {
	result, err := Project(standardGenerated(t))
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	cards := map[string]catalogdata.Card{}
	for _, card := range result.Sets[0].Cards {
		cards[card.Card.ID] = card.Card
	}

	withVariants := cards["fxa-1"]
	if !withVariants.VariantsDetailedPresent || len(withVariants.VariantsDetailed) != 2 {
		t.Fatalf("fxa-1 variants present=%t count=%d", withVariants.VariantsDetailedPresent, len(withVariants.VariantsDetailed))
	}
	if !withVariants.VariantsFirstEdition {
		t.Fatal("the provider firstEdition flag was lost")
	}
	if withVariants.VariantsDetailed[1].Stamp[0] != "1st-edition" {
		t.Fatal("the provider stamp was normalized during preparation")
	}
	if withVariants.Level != "12" {
		t.Fatalf("numeric level = %q, want normalized text", withVariants.Level)
	}

	absent := cards["fxa-2"]
	if absent.VariantsDetailedPresent {
		t.Fatal("a card without variants_detailed was projected as present")
	}
	if len(absent.VariantsDetailed) != 0 {
		t.Fatal("a card without variants_detailed gained variants")
	}
	if absent.Level != "X" {
		t.Fatalf("string level = %q", absent.Level)
	}
}

func TestProjectKeepsTheFirstDuplicateOutsideTheApplicationScope(t *testing.T) {
	// The independent Asian catalogs publish collisions: zh-cn ships two
	// different sets as "CSV1C". They are inventoried, never projected, so the
	// preparation keeps the first and reports it instead of failing.
	duplicateSets := `[
	  {"id":"fxa","name":"Fixture JA","serie":{"id":"fx"},"cardCount":{"official":2,"total":2},"cards":[{"id":"fxa-1"}]},
	  {"id":"fxa","name":"Fixture JA Again","serie":{"id":"fx"},"cardCount":{"official":9,"total":9},"cards":[]}
	]`
	root := writeGenerated(t, map[string]generatedFiles{
		"en": {Series: englishSeries, Sets: englishSets, Cards: englishCards},
		"it": {Series: englishSeries, Sets: italianSets, Cards: italianCards},
		"ja": {Series: japaneseSeries, Sets: duplicateSets, Cards: japaneseCards},
	})
	result, err := Project(root)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	var message string
	for _, finding := range result.Findings {
		if finding.Code == catalogdata.FindingSourceDuplicate {
			message = finding.Message
		}
	}
	if !strings.Contains(message, "ja sets fxa") {
		t.Fatalf("duplicate finding = %q, want it to name ja sets fxa", message)
	}

	lenient, err := readLanguage(root, "ja", false)
	if err != nil {
		t.Fatalf("lenient read: %v", err)
	}
	if len(lenient.Sets) != 1 || len(lenient.SetOrder) != 1 {
		t.Fatalf("lenient read kept %d sets, want the duplicate collapsed to one", len(lenient.Sets))
	}
	if lenient.Sets["fxa"].Name != "Fixture JA" {
		t.Fatalf("lenient read kept %q, want the first occurrence", lenient.Sets["fxa"].Name)
	}

	// The same collision inside the application scope is a defect, not noise.
	if _, err := readLanguage(root, "ja", true); err == nil {
		t.Fatal("a strict read accepted a duplicate set")
	}
	scoped := writeGenerated(t, map[string]generatedFiles{
		"en": {Series: englishSeries, Sets: englishSets, Cards: englishCards},
		"it": {Series: englishSeries, Sets: duplicateSets, Cards: italianCards},
	})
	if _, err := Project(scoped); err == nil {
		t.Fatal("a duplicate set in a projected language was accepted")
	}
}

func TestProjectFailsOnMissingReferencedCard(t *testing.T) {
	root := writeGenerated(t, map[string]generatedFiles{
		"en": {
			Series: englishSeries,
			Sets:   `[{"id":"fxa","name":"Fixture Alpha","serie":{"id":"fx"},"cardCount":{"official":2,"total":2},"cards":[{"id":"fxa-1"},{"id":"fxa-2"}]}]`,
			Cards:  `[{"id":"fxa-1","localId":"1","name":"Fixture Bulba","set":{"id":"fxa"}}]`,
		},
	})
	_, err := Project(root)
	if err == nil {
		t.Fatal("a set referencing a card the catalog does not contain was accepted")
	}
	if !strings.Contains(err.Error(), "fxa-2") {
		t.Fatalf("error = %v, want the missing card named", err)
	}
}

func TestProjectFailsOnInvalidIdentities(t *testing.T) {
	cases := map[string]generatedFiles{
		"card in another set": {
			Series: englishSeries,
			Sets:   `[{"id":"fxa","name":"A","serie":{"id":"fx"},"cardCount":{"official":1,"total":1},"cards":[{"id":"fxa-1"}]}]`,
			Cards:  `[{"id":"fxa-1","localId":"1","name":"A","set":{"id":"other"}}]`,
		},
		"card without a name": {
			Series: englishSeries,
			Sets:   `[{"id":"fxa","name":"A","serie":{"id":"fx"},"cardCount":{"official":1,"total":1},"cards":[{"id":"fxa-1"}]}]`,
			Cards:  `[{"id":"fxa-1","localId":"1","name":"  ","set":{"id":"fxa"}}]`,
		},
		"duplicate card reference": {
			Series: englishSeries,
			Sets:   `[{"id":"fxa","name":"A","serie":{"id":"fx"},"cardCount":{"official":2,"total":2},"cards":[{"id":"fxa-1"},{"id":"fxa-1"}]}]`,
			Cards:  `[{"id":"fxa-1","localId":"1","name":"A","set":{"id":"fxa"}}]`,
		},
		"duplicate card record": {
			Series: englishSeries,
			Sets:   `[{"id":"fxa","name":"A","serie":{"id":"fx"},"cardCount":{"official":1,"total":1},"cards":[{"id":"fxa-1"}]}]`,
			Cards:  `[{"id":"fxa-1","localId":"1","name":"A","set":{"id":"fxa"}},{"id":"fxa-1","localId":"1","name":"A","set":{"id":"fxa"}}]`,
		},
		"reference without an id": {
			Series: englishSeries,
			Sets:   `[{"id":"fxa","name":"A","serie":{"id":"fx"},"cardCount":{"official":1,"total":1},"cards":[{"id":" "}]}]`,
			Cards:  `[{"id":"fxa-1","localId":"1","name":"A","set":{"id":"fxa"}}]`,
		},
	}
	for name, files := range cases {
		root := writeGenerated(t, map[string]generatedFiles{"en": files})
		if _, err := Project(root); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestProjectProducesStableArchiveBytes(t *testing.T) {
	root := standardGenerated(t)
	build := func() ([]byte, catalogdata.Lock) {
		t.Helper()
		result, err := Project(root)
		if err != nil {
			t.Fatalf("project: %v", err)
		}
		var buffer bytes.Buffer
		built, err := catalogdata.Build(&buffer, catalogdata.BuildInput{
			Kind:            catalogdata.KindFixture,
			Revision:        0,
			SourceAPI:       "v2",
			SourceTag:       "test",
			SourceCommit:    strings.Repeat("a", 40),
			BuilderVersion:  "test/1",
			CompilerImageID: "test",
			AppLanguages:    catalogdata.AppLanguages,
			SourceArchive:   []byte("source snapshot bytes"),
			AssetsIndex:     []byte(`{}`),
			SourceDirs:      []catalogdata.SourceDirRow{{Path: "server", Files: 1, Bytes: 21}},
			SourceLanguages: result.SourceLanguages,
			SourceSeries:    result.SourceSeries,
			Sets:            result.Sets,
			Findings:        result.Findings,
		})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		return buffer.Bytes(), built.Lock
	}

	first, firstLock := build()
	second, secondLock := build()
	if !bytes.Equal(first, second) {
		t.Fatal("identical compiler output produced different archive bytes")
	}
	if firstLock != secondLock {
		t.Fatalf("identical compiler output produced different locks: %+v vs %+v", firstLock, secondLock)
	}

	archive, err := catalogdata.OpenBytes(first, firstLock)
	if err != nil {
		t.Fatalf("open built archive: %v", err)
	}
	defer archive.Close()
	if err := archive.VerifyEntries(); err != nil {
		t.Fatalf("verify built archive: %v", err)
	}
	if len(archive.Index().Sets) != 2 {
		t.Fatalf("archive has %d sets", len(archive.Index().Sets))
	}
}

func TestProjectRequiresGeneratedEnglishCatalog(t *testing.T) {
	if _, err := Project(""); err == nil {
		t.Fatal("an empty generated directory was accepted")
	}
	root := standardGenerated(t)
	if err := os.RemoveAll(filepath.Join(root, "en")); err != nil {
		t.Fatal(err)
	}
	if _, err := Project(root); err == nil {
		t.Fatal("a catalog without English was accepted")
	}
}

func TestSourceCardLevelAcceptsProviderStringOrNumber(t *testing.T) {
	// TCGdex publishes level as a number or as text such as "X".
	for payload, want := range map[string]string{
		`{"id":"a","level":76}`:     "76",
		`{"id":"a","level":"X"}`:    "X",
		`{"id":"a","level":null}`:   "",
		`{"id":"a"}`:                "",
		`{"id":"a","level":"76.5"}`: "76.5",
	} {
		var card sourceCard
		if err := json.Unmarshal([]byte(payload), &card); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		if string(card.Level) != want {
			t.Fatalf("%s decoded level %q, want %q", payload, card.Level, want)
		}
	}
	var card sourceCard
	if err := json.Unmarshal([]byte(`{"id":"a","level":{"value":1}}`), &card); err == nil {
		t.Fatal("an object level was accepted")
	}
}

func TestSourceCardDexIDsDropNonIntegralProviderValues(t *testing.T) {
	// TCGdex records 384.1 on the Japanese delta-species Rayquaza cards. A
	// fractional national Pokedex number has nowhere to go in the card model,
	// so it is dropped instead of failing the whole build. A quoted number is
	// accepted like any other provider number; anything that is not a list of
	// numbers is not.
	for payload, want := range map[string][]int{
		`{"id":"a","dexId":[384]}`:       {384},
		`{"id":"a","dexId":[384.1]}`:     {},
		`{"id":"a","dexId":[384,384.1]}`: {384},
		`{"id":"a","dexId":[]}`:          {},
		`{"id":"a","dexId":["384"]}`:     {384},
		`{"id":"a","dexId":null}`:        nil,
		`{"id":"a"}`:                     nil,
	} {
		var card sourceCard
		if err := json.Unmarshal([]byte(payload), &card); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		if len(card.DexIDs) != len(want) {
			t.Fatalf("%s decoded dexId %v, want %v", payload, card.DexIDs, want)
		}
		for i, id := range want {
			if card.DexIDs[i] != id {
				t.Fatalf("%s decoded dexId %v, want %v", payload, card.DexIDs, want)
			}
		}
	}
	for _, payload := range []string{
		`{"id":"a","dexId":384}`,
		`{"id":"a","dexId":["x"]}`,
		`{"id":"a","dexId":[{"value":384}]}`,
	} {
		var card sourceCard
		if err := json.Unmarshal([]byte(payload), &card); err == nil {
			t.Fatalf("%s was accepted", payload)
		}
	}
}

func TestSourceCardVariantPresenceDistinguishesAbsentFromEmpty(t *testing.T) {
	for payload, want := range map[string]bool{
		`{"id":"a","variants_detailed":[]}`:   true,
		`{"id":"a","variants_detailed":null}`: false,
		`{"id":"a"}`:                          false,
	} {
		var card sourceCard
		if err := json.Unmarshal([]byte(payload), &card); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		if present := convertCard(card, "", "").VariantsDetailedPresent; present != want {
			t.Fatalf("%s reported presence %t, want %t", payload, present, want)
		}
	}
}
