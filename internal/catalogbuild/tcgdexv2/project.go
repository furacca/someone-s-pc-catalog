package tcgdexv2

import (
	"fmt"
	"maps"
	"slices"
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
	sourceLanguages, err := discoverLanguages(generatedDir)
	if err != nil {
		return Result{}, err
	}
	result := Result{SourceLanguages: sourceLanguages}
	result.Report.SourceLanguages = len(sourceLanguages)

	var english languageData
	var translations []languageData
	seriesSeen := map[string]struct{}{}
	var duplicates, unusedLanguages []string
	for _, language := range sourceLanguages {
		projected := slices.Contains(catalogdata.AppLanguages, language)
		data, err := readLanguage(generatedDir, language, projected)
		if err != nil {
			return Result{}, err
		}
		for _, series := range data.Series {
			if id := strings.TrimSpace(series.ID); id != "" {
				seriesSeen[id] = struct{}{}
			}
		}
		if len(data.DuplicateSets) > 0 {
			duplicates = append(duplicates, fmt.Sprintf("%s sets %s", language, strings.Join(data.DuplicateSets, ", ")))
		}
		if len(data.DuplicateCards) > 0 {
			duplicates = append(duplicates, fmt.Sprintf("%s cards %s", language, strings.Join(data.DuplicateCards, ", ")))
		}

		switch {
		case language == "en":
			english = data
		case projected:
			translations = append(translations, data)
		default:
			unusedLanguages = append(unusedLanguages, language)
		}
	}
	if english.Sets == nil {
		return Result{}, fmt.Errorf("project: missing compiled English catalog")
	}
	result.SourceSeries = slices.Sorted(maps.Keys(seriesSeen))
	result.Report.SourceSets = len(english.SetOrder)
	result.Report.SourceCards = len(english.Cards)

	for _, setID := range english.SetOrder {
		set := english.Sets[setID]
		if catalogdata.IsPocketSeries(set.Serie.ID) {
			result.Report.SkippedPocketSets++
			continue
		}
		record, findings, err := projectSet(set, english, translations)
		if err != nil {
			return Result{}, err
		}
		result.Findings = append(result.Findings, findings...)
		result.Sets = append(result.Sets, record)
		if !record.CardsComplete {
			result.Report.IncompleteSets++
		}
	}

	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		result.Findings = append(result.Findings, catalogdata.Finding{
			Code:    catalogdata.FindingSourceDuplicate,
			Message: "Kept the first of ids the source publishes twice outside the application scope: " + strings.Join(duplicates, "; "),
		})
	}

	if result.Report.SkippedPocketSets > 0 || len(unusedLanguages) > 0 {
		message := "Retained in the source snapshot without being projected:"
		if result.Report.SkippedPocketSets > 0 {
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

func projectSet(set sourceSet, english languageData, translations []languageData) (catalogdata.SetRecord, []catalogdata.Finding, error) {
	record := catalogdata.SetRecord{Set: convertSet(set), CardsComplete: true}
	findings := make([]catalogdata.Finding, 0, 4)

	advertised := set.CardCount.Total
	if advertised == 0 {
		advertised = set.CardCount.Official
	}
	if len(set.Cards) == 0 && advertised > 0 {
		// Treating this list as complete would retire owned printings. The
		// advertised count is maintained by hand (tcgdex/cards-database#1731).
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
		localizations, missing := localizeCard(converted, translations)
		findings = append(findings, missing...)
		cardRecord := catalogdata.CardRecord{Card: converted, Localizations: localizations}
		record.Cards = append(record.Cards, cardRecord)
	}

	for _, data := range translations {
		language := data.Language
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

func localizeCard(english catalogdata.Card, translations []languageData) ([]catalogdata.CardLocalization, []catalogdata.Finding) {
	var localizations []catalogdata.CardLocalization
	var findings []catalogdata.Finding
	for _, data := range translations {
		language := data.Language
		localizedSet, hasSet := data.Sets[english.SetID]
		if !hasSet || len(localizedSet.Cards) == 0 {
			continue
		}
		localized, hasCard := data.Cards[english.ID]
		if !hasCard {
			continue
		}
		if strings.TrimSpace(localized.Name) == "" {
			findings = append(findings, catalogdata.Finding{
				Code:     catalogdata.FindingLocalizationMissing,
				SetID:    english.SetID,
				Language: language,
				Message:  fmt.Sprintf("card %s has an empty %s name and was not projected", english.ID, language),
			})
			continue
		}
		if strings.TrimSpace(localized.LocalID) != strings.TrimSpace(english.LocalID) {
			findings = append(findings, catalogdata.Finding{
				Code:     catalogdata.FindingLocalizationMissing,
				SetID:    english.SetID,
				Language: language,
				Message:  fmt.Sprintf("card %s has %s number %q against English %q and was not projected", english.ID, language, localized.LocalID, english.LocalID),
			})
			continue
		}
		if strings.TrimSpace(localized.Image) == "" {
			localized.Image = english.Image
		}
		localizedCard := convertCard(localized, english.SetID, localizedSet.Name)
		// Printing identity is English-only; a translation carries the
		// same variant data so the reader can validate it.
		localizedCard.VariantsDetailed = english.VariantsDetailed
		localizedCard.VariantsDetailedPresent = english.VariantsDetailedPresent
		localizedCard.VariantsFirstEdition = english.VariantsFirstEdition
		localizations = append(localizations, catalogdata.CardLocalization{
			Language: language,
			Card:     localizedCard,
		})
	}
	return localizations, findings
}

func convertSet(set sourceSet) catalogdata.Set {
	return catalogdata.Set{
		ID:                strings.TrimSpace(set.ID),
		Name:              set.Name,
		Logo:              withExtension(set.Logo, ".webp"),
		Symbol:            withExtension(set.Symbol, ".png"),
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

		VariantsDetailedPresent: card.VariantsDetailed != nil,
		VariantsFirstEdition:    card.Variants.FirstEdition,
	}
	if card.Item != nil {
		item := catalogdata.TextBlock(*card.Item)
		converted.Item = &item
	}
	for _, block := range card.Abilities {
		converted.Abilities = append(converted.Abilities, catalogdata.TextBlock(block))
	}
	for _, block := range card.Attacks {
		converted.Attacks = append(converted.Attacks, catalogdata.TextBlock(block))
	}
	for _, value := range card.Weaknesses {
		converted.Weaknesses = append(converted.Weaknesses, catalogdata.TypeValue{Type: value.Type, Value: value.Value})
	}
	for _, value := range card.Resistances {
		converted.Resistances = append(converted.Resistances, catalogdata.TypeValue{Type: value.Type, Value: value.Value})
	}
	if card.VariantsDetailed != nil {
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

func seriesName(value sourceNamed) string {
	if strings.TrimSpace(value.Name) != "" {
		return value.Name
	}
	return strings.TrimSpace(value.ID)
}

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
