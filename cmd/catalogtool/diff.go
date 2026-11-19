package main

import (
	"flag"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

func runDiff(args []string) error {
	flags := flag.NewFlagSet("diff", flag.ExitOnError)
	oldArchive := flags.String("old", "", "path of the previous archive")
	oldLock := flags.String("old-lock", "", "path of the previous lock; defaults to <old>.lock.json")
	newArchive := flags.String("new", "", "path of the candidate archive")
	newLock := flags.String("new-lock", "", "path of the candidate lock; defaults to <new>.lock.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *oldArchive == "" || *newArchive == "" {
		return fmt.Errorf("diff: -old and -new are required")
	}
	if *oldLock == "" {
		*oldLock = *oldArchive + ".lock.json"
	}
	if *newLock == "" {
		*newLock = *newArchive + ".lock.json"
	}
	before, err := openArchive(*oldArchive, *oldLock)
	if err != nil {
		return fmt.Errorf("diff: previous archive: %w", err)
	}
	defer before.Close()
	after, err := openArchive(*newArchive, *newLock)
	if err != nil {
		return fmt.Errorf("diff: candidate archive: %w", err)
	}
	defer after.Close()

	// Validate additions too, before writing any diff output.
	for _, archive := range []*catalogdata.Archive{before, after} {
		if err := archive.VerifyEntries(); err != nil {
			return fmt.Errorf("diff: %w", err)
		}
		if err := archive.EachSet(func(catalogdata.IndexEntry, catalogdata.SetRecord) error { return nil }); err != nil {
			return fmt.Errorf("diff: %w", err)
		}
	}
	if after.Manifest().Revision <= before.Manifest().Revision && after.SHA256() != before.SHA256() {
		return fmt.Errorf("diff: a changed catalog requires a revision newer than %d", before.Manifest().Revision)
	}

	fmt.Printf("revision %d -> %d\n", before.Manifest().Revision, after.Manifest().Revision)
	fmt.Printf("source commit %s -> %s\n", before.Manifest().SourceCommit, after.Manifest().SourceCommit)
	if !reflect.DeepEqual(findingsForSet(before.Manifest(), ""), findingsForSet(after.Manifest(), "")) {
		fmt.Println("source findings changed")
	}

	beforeSets := indexByID(before.Index())
	afterSets := indexByID(after.Index())

	added, removed, changed := 0, 0, 0
	for _, candidate := range after.Index().Sets {
		setID := candidate.SetID
		previous, existed := beforeSets[setID]
		if !existed {
			added++
			fmt.Printf("+ set %s %q cards %d\n", setID, candidate.Name, candidate.ListedCardCount)
			continue
		}
		summary, err := compareSets(before, after, setID, previous, candidate)
		if err != nil {
			return fmt.Errorf("diff: %w", err)
		}
		if summary != "" {
			changed++
			fmt.Printf("~ set %s: %s\n", setID, summary)
		}
	}
	for _, previous := range before.Index().Sets {
		setID := previous.SetID
		if _, exists := afterSets[setID]; exists {
			continue
		}
		removed++
		fmt.Printf("- set %s %q cards %d is missing from the candidate\n", setID, previous.Name, previous.ListedCardCount)
	}

	fmt.Printf("sets added %d, changed %d, missing %d\n", added, changed, removed)
	fmt.Printf("cards %d -> %d, printings %d -> %d\n",
		before.Manifest().AppCounts.Cards, after.Manifest().AppCounts.Cards,
		before.Manifest().AppCounts.Printings, after.Manifest().AppCounts.Printings)
	fmt.Println("localization coverage:")
	coverage := map[string][2]int{}
	for _, row := range before.Manifest().AppCounts.Coverage {
		coverage[row.Language] = [2]int{row.Cards, 0}
	}
	for _, row := range after.Manifest().AppCounts.Coverage {
		current := coverage[row.Language]
		current[1] = row.Cards
		coverage[row.Language] = current
	}
	for _, language := range catalogdata.SortLanguages(slices.Collect(maps.Keys(coverage))) {
		fmt.Printf("  %s: %d -> %d cards\n", language, coverage[language][0], coverage[language][1])
	}
	if removed > 0 {
		return fmt.Errorf("diff: %d sets present in the previous archive are missing from the candidate", removed)
	}
	return nil
}

func compareSets(before, after *catalogdata.Archive, setID string, previous, candidate catalogdata.IndexEntry) (string, error) {
	notes := make([]string, 0, 4)
	if previous.Name != candidate.Name {
		notes = append(notes, fmt.Sprintf("name %q -> %q", previous.Name, candidate.Name))
	}
	if previous.ListedCardCount != candidate.ListedCardCount {
		notes = append(notes, fmt.Sprintf("cards %d -> %d", previous.ListedCardCount, candidate.ListedCardCount))
	}
	if previous.CardsComplete != candidate.CardsComplete {
		notes = append(notes, fmt.Sprintf("complete %t -> %t", previous.CardsComplete, candidate.CardsComplete))
	}
	if !slices.Equal(previous.Languages, candidate.Languages) {
		notes = append(notes, fmt.Sprintf("languages %s -> %s", strings.Join(previous.Languages, ","), strings.Join(candidate.Languages, ",")))
	}
	previousRecord, err := before.Set(setID)
	if err != nil {
		return "", fmt.Errorf("previous archive cannot be read: %w", err)
	}
	candidateRecord, err := after.Set(setID)
	if err != nil {
		return "", fmt.Errorf("candidate cannot be read: %w", err)
	}
	if !reflect.DeepEqual(previousRecord.Set, candidateRecord.Set) || !reflect.DeepEqual(previousRecord.Localizations, candidateRecord.Localizations) {
		notes = append(notes, "set metadata or translations changed")
	}
	if previousRecord.IncompleteReason != candidateRecord.IncompleteReason {
		notes = append(notes, fmt.Sprintf("incomplete reason %q -> %q", previousRecord.IncompleteReason, candidateRecord.IncompleteReason))
	}
	if !reflect.DeepEqual(findingsForSet(before.Manifest(), setID), findingsForSet(after.Manifest(), setID)) {
		notes = append(notes, "findings changed")
	}
	changed := make([]string, 0, 4)
	added := make([]string, 0, 4)
	previousCards := map[string]catalogdata.CardRecord{}
	for _, card := range previousRecord.Cards {
		previousCards[card.Card.ID] = card
	}
	for _, card := range candidateRecord.Cards {
		if previous, exists := previousCards[card.Card.ID]; !exists {
			added = append(added, card.Card.ID)
		} else if !reflect.DeepEqual(previous, card) {
			changed = append(changed, card.Card.ID)
		}
		delete(previousCards, card.Card.ID)
	}
	missing := slices.Sorted(maps.Keys(previousCards))
	if len(missing) > 0 {
		notes = append(notes, "cards missing from the candidate: "+strings.Join(missing, ", "))
	}
	if len(added) > 0 {
		notes = append(notes, "cards added: "+strings.Join(added, ", "))
	}
	if len(changed) > 0 {
		notes = append(notes, "card metadata, variants or translations changed: "+strings.Join(changed, ", "))
	}
	return strings.Join(notes, "; "), nil
}

func findingsForSet(manifest catalogdata.Manifest, setID string) []catalogdata.Finding {
	var findings []catalogdata.Finding
	for _, finding := range manifest.Findings {
		if finding.SetID == setID {
			findings = append(findings, finding)
		}
	}
	return findings
}

func indexByID(index catalogdata.Index) map[string]catalogdata.IndexEntry {
	out := make(map[string]catalogdata.IndexEntry, len(index.Sets))
	for _, entry := range index.Sets {
		out[entry.SetID] = entry
	}
	return out
}
