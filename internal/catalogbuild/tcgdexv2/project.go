package tcgdexv2

import (
	"fmt"
	"sort"
	"strings"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

// Result is the projection plus the inventory the manifest needs.
type Result struct {
	Sets            []catalogdata.SetRecord
	Findings        []catalogdata.Finding
	SourceLanguages []string
	SourceSeries    []string
	Report          Report
}

// Report counts source records and projection exclusions for the preparation report.
type Report struct {
	SourceLanguages   int
	SourceSets        int
	SourceCards       int
	SkippedPocketSets int
	IncompleteSets    int
}

// Project reads compiled TCGdex v2 catalogs for catalogdata.AppLanguages.
func Project(generatedDir string) (Result, error) {
	if strings.TrimSpace(generatedDir) == "" {
		return Result{}, fmt.Errorf("project: the generated directory is required")
	}
	languages := catalogdata.SortLanguages(catalogdata.AppLanguages)

	sourceLanguages, err := discoverLanguages(generatedDir)
	if err != nil {
		return Result{}, err
	}

	// Compiled catalogs are large, so each language is read once and shared
	// between the projection and the source inventory.
	loaded := map[string]languageData{}
	load := func(language string) (languageData, error) {
		if data, cached := loaded[language]; cached {
			return data, nil
		}
		data, err := readLanguage(generatedDir, language, containsString(languages, language))
		if err != nil {
			return languageData{}, err
		}
		loaded[language] = data
		return data, nil
	}

	english, err := load("en")
	if err != nil {
		return Result{}, err
	}

	translations := map[string]languageData{}
	for _, language := range languages[1:] {
		if !containsString(sourceLanguages, language) {
			// The compiler produced no such catalog at this revision.
			continue
		}
		data, err := load(language)
		if err != nil {
			return Result{}, err
		}
		translations[language] = data
	}

	result := Result{
		SourceLanguages: append([]string(nil), sourceLanguages...),
		Report: Report{
			SourceLanguages: len(sourceLanguages),
			SourceSets:      len(english.SetOrder),
			SourceCards:     len(english.Cards),
		},
	}
	sort.Strings(result.SourceLanguages)

	seriesSeen := map[string]struct{}{}
	for _, language := range sourceLanguages {
		data, err := load(language)
		if err != nil {
			return Result{}, err
		}
		for _, series := range data.Series {
			id := strings.TrimSpace(series.ID)
			if id == "" {
				continue
			}
			seriesSeen[id] = struct{}{}
		}
	}
	for id := range seriesSeen {
		result.SourceSeries = append(result.SourceSeries, id)
	}
	sort.Strings(result.SourceSeries)

	pocketRetained := false
	for _, setID := range english.SetOrder {
		set := english.Sets[setID]
		if catalogdata.IsPocketSeries(set.Serie.ID) {
			result.Report.SkippedPocketSets++
			pocketRetained = true
			continue
		}
		record, findings, err := projectSet(set, english, translations, languages[1:])
		if err != nil {
			return Result{}, err
		}
		result.Findings = append(result.Findings, findings...)
		result.Sets = append(result.Sets, record)
		if !record.CardsComplete {
			result.Report.IncompleteSets++
		}
	}

	duplicates := make([]string, 0, len(sourceLanguages))
	for _, language := range sourceLanguages {
		data := loaded[language]
		if len(data.DuplicateSets) > 0 {
			duplicates = append(duplicates, fmt.Sprintf("%s sets %s", language, strings.Join(data.DuplicateSets, ", ")))
		}
		if len(data.DuplicateCards) > 0 {
			duplicates = append(duplicates, fmt.Sprintf("%s cards %s", language, strings.Join(data.DuplicateCards, ", ")))
		}
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		result.Findings = append(result.Findings, catalogdata.Finding{
			Code:    catalogdata.FindingSourceDuplicate,
			Message: "Kept the first of ids the source publishes twice outside the application scope: " + strings.Join(duplicates, "; "),
		})
	}

	unusedLanguages := make([]string, 0, len(sourceLanguages))
	for _, language := range sourceLanguages {
		if !containsString(languages, language) {
			unusedLanguages = append(unusedLanguages, language)
		}
	}
	if pocketRetained || len(unusedLanguages) > 0 {
		message := "Retained in the source snapshot without being projected:"
		if pocketRetained {
			message += fmt.Sprintf(" %d Pocket sets;", result.Report.SkippedPocketSets)
		}
		if len(unusedLanguages) > 0 {
			message += " languages " + strings.Join(unusedLanguages, ", ")
		}
		result.Findings = append(result.Findings, catalogdata.Finding{
			Code:    catalogdata.FindingSourceUnprojected,
			Message: strings.TrimRight(message, ";"),
		})
	}
	return result, nil
}

func projectSet(set sourceSet, english languageData, translations map[string]languageData, languages []string) (catalogdata.SetRecord, []catalogdata.Finding, error) {
	record := catalogdata.SetRecord{Set: convertSet(set), CardsComplete: true}
	findings := make([]catalogdata.Finding, 0, 4)

	advertised := set.CardCount.Total
	if advertised == 0 {
		advertised = set.CardCount.Official
	}
	if len(set.Cards) == 0 && advertised > 0 {
		// Upstream lists no cards while its card count still announces some.
		// That count is maintained by hand and upstream has deprecated it
		// (tcgdex/cards-database#1731), so the difference is not a defect to
		// wait out. Keep the set in the index and say so; treating the empty
		// list as authoritative would retire printings the operator still owns.
		record.CardsComplete = false
		record.IncompleteReason = fmt.Sprintf("upstream lists no cards; its hand-maintained count announces %d", advertised)
		findings = append(findings, catalogdata.Finding{
			Code:    catalogdata.FindingSetIncomplete,
			SetID:   set.ID,
			Message: record.IncompleteReason,
		})
	}

	seen := map[string]struct{}{}
	for _, reference := range set.Cards {
		cardID := strings.TrimSpace(reference.ID)
		if cardID == "" {
			return catalogdata.SetRecord{}, nil, fmt.Errorf("set %s references a card without an id", set.ID)
		}
		if _, exists := seen[cardID]; exists {
			return catalogdata.SetRecord{}, nil, fmt.Errorf("set %s references card %s twice", set.ID, cardID)
		}
		seen[cardID] = struct{}{}

		card, exists := english.Cards[cardID]
		if !exists {
			return catalogdata.SetRecord{}, nil, fmt.Errorf("set %s references card %s, which the English catalog does not contain", set.ID, cardID)
		}
		if parent := strings.TrimSpace(card.Set.ID); parent != "" && parent != set.ID {
			return catalogdata.SetRecord{}, nil, fmt.Errorf("card %s belongs to set %s, referenced from set %s", cardID, parent, set.ID)
		}
		if strings.TrimSpace(card.Name) == "" {
			return catalogdata.SetRecord{}, nil, fmt.Errorf("card %s has no name", cardID)
		}

		converted := convertCard(card, set.ID, set.Name)
		cardRecord := catalogdata.CardRecord{Card: converted}

		for _, language := range languages {
			data, available := translations[language]
			if !available {
				continue
			}
			localizedSet, hasSet := data.Sets[set.ID]
			if !hasSet || len(localizedSet.Cards) == 0 {
				continue
			}
			localized, hasCard := data.Cards[cardID]
			if !hasCard {
				continue
			}
			if strings.TrimSpace(localized.Name) == "" {
				findings = append(findings, catalogdata.Finding{
					Code:     catalogdata.FindingLocalizationMissing,
					SetID:    set.ID,
					Language: language,
					Message:  fmt.Sprintf("card %s has an empty %s name and was not projected", cardID, language),
				})
				continue
			}
			if strings.TrimSpace(localized.LocalID) != strings.TrimSpace(card.LocalID) {
				// A translation renumbered against English is unusable;
				// preparation records it instead of shipping a record the
				// reader would reject.
				findings = append(findings, catalogdata.Finding{
					Code:     catalogdata.FindingLocalizationMissing,
					SetID:    set.ID,
					Language: language,
					Message:  fmt.Sprintf("card %s has %s number %q against English %q and was not projected", cardID, language, localized.LocalID, card.LocalID),
				})
				continue
			}
			if strings.TrimSpace(localized.Image) == "" {
				localized.Image = card.Image
			}
			localizedCard := convertCard(localized, set.ID, localizedSet.Name)
			// Printing identity is English-only; a translation carries the
			// same variant data so the reader can validate it.
			localizedCard.VariantsDetailed = converted.VariantsDetailed
			localizedCard.VariantsDetailedPresent = converted.VariantsDetailedPresent
			localizedCard.VariantsFirstEdition = converted.VariantsFirstEdition
			cardRecord.Localizations = append(cardRecord.Localizations, catalogdata.CardLocalization{
				Language: language,
				Card:     localizedCard,
			})
		}
		record.Cards = append(record.Cards, cardRecord)
	}

	for _, language := range languages {
		data, available := translations[language]
		if !available {
			continue
		}
		localizedSet, hasSet := data.Sets[set.ID]
		if !hasSet {
			findings = append(findings, catalogdata.Finding{
				Code:     catalogdata.FindingLocalizationMissing,
				SetID:    set.ID,
				Language: language,
				Message:  "upstream has no translation for this set",
			})
			continue
		}
		if len(localizedSet.Cards) == 0 {
			// Translated set metadata without localized cards is not a usable
			// card-catalog language.
			findings = append(findings, catalogdata.Finding{
				Code:     catalogdata.FindingLocalizationMissing,
				SetID:    set.ID,
				Language: language,
				Message:  "upstream translates the set metadata but lists no cards",
			})
			continue
		}
		if strings.TrimSpace(localizedSet.Logo) == "" {
			localizedSet.Logo = set.Logo
		}
		if strings.TrimSpace(localizedSet.Symbol) == "" {
			localizedSet.Symbol = set.Symbol
		}
		converted := convertSet(localizedSet)
		converted.ID = set.ID
		converted.SeriesID = record.Set.SeriesID
		if strings.TrimSpace(converted.SeriesName) == "" {
			converted.SeriesName = record.Set.SeriesName
		}
		record.Localizations = append(record.Localizations, catalogdata.SetLocalization{
			Language: language,
			Set:      converted,
		})
	}

	return record, findings, nil
}

func convertSet(set sourceSet) catalogdata.Set {
	return catalogdata.Set{
		ID:                strings.TrimSpace(set.ID),
		Name:              set.Name,
		Logo:              setLogoURL(set.Logo),
		Symbol:            setSymbolURL(set.Symbol),
		ReleaseDate:       strings.TrimSpace(set.ReleaseDate),
		SeriesID:          strings.TrimSpace(set.Serie.ID),
		SeriesName:        seriesName(set.Serie),
		CardCountOfficial: set.CardCount.Official,
		CardCountTotal:    set.CardCount.Total,
	}
}

func convertCard(card sourceCard, setID, setName string) catalogdata.Card {
	converted := catalogdata.Card{
		ID:             strings.TrimSpace(card.ID),
		LocalID:        card.LocalID,
		Name:           card.Name,
		Image:          strings.TrimSpace(card.Image),
		Category:       card.Category,
		Rarity:         card.Rarity,
		Illustrator:    card.Illustrator,
		RegulationMark: card.RegulationMark,
		Description:    card.Description,
		Effect:         card.Effect,
		HP:             card.HP,
		Types:          card.Types,
		EvolveFrom:     card.EvolveFrom,
		Weight:         card.Weight,
		Stage:          card.Stage,
		Suffix:         card.Suffix,
		DexIDs:         []int(card.DexIDs),
		Level:          string(card.Level),
		Retreat:        card.Retreat,
		TrainerType:    card.TrainerType,
		EnergyType:     card.EnergyType,
		SetID:          setID,
		SetName:        setName,

		VariantsDetailedPresent: card.variantsDetailedPresent,
		VariantsFirstEdition:    card.Variants.FirstEdition,
	}
	if card.Item != nil {
		item := convertTextBlock(*card.Item)
		converted.Item = &item
	}
	for _, block := range card.Abilities {
		converted.Abilities = append(converted.Abilities, convertTextBlock(block))
	}
	for _, block := range card.Attacks {
		converted.Attacks = append(converted.Attacks, convertTextBlock(block))
	}
	for _, value := range card.Weaknesses {
		converted.Weaknesses = append(converted.Weaknesses, catalogdata.TypeValue{Type: value.Type, Value: value.Value})
	}
	for _, value := range card.Resistances {
		converted.Resistances = append(converted.Resistances, catalogdata.TypeValue{Type: value.Type, Value: value.Value})
	}
	if card.variantsDetailedPresent {
		converted.VariantsDetailed = make([]catalogdata.Variant, 0, len(card.VariantsDetailed))
		for _, variant := range card.VariantsDetailed {
			converted.VariantsDetailed = append(converted.VariantsDetailed, catalogdata.Variant{
				Type:      variant.Type,
				Subtype:   variant.Subtype,
				Size:      variant.Size,
				Stamp:     variant.Stamp,
				Foil:      variant.Foil,
				VariantID: variant.VariantID,
			})
		}
	}
	return converted
}

func convertTextBlock(block sourceTextBlock) catalogdata.TextBlock {
	return catalogdata.TextBlock{
		Type:   block.Type,
		Name:   block.Name,
		Effect: block.Effect,
		Damage: block.Damage,
		Cost:   block.Cost,
	}
}

func seriesName(value sourceNamed) string {
	if strings.TrimSpace(value.Name) != "" {
		return value.Name
	}
	return strings.TrimSpace(value.ID)
}

// setLogoURL and setSymbolURL apply the upstream extension convention, so the
// archive stores references that are usable without further guessing.
func setLogoURL(value string) string { return withExtension(value, ".webp") }

func setSymbolURL(value string) string { return withExtension(value, ".png") }

func withExtension(value, extension string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(lastPathSegment(value), ".") {
		return value
	}
	return value + extension
}

func lastPathSegment(value string) string {
	value = strings.TrimRight(value, "/")
	if before, _, ok := strings.Cut(value, "?"); ok {
		value = before
	}
	parts := strings.Split(value, "/")
	return parts[len(parts)-1]
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
