package main

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

func openArchive(archivePath, lockPath string) (*catalogdata.Archive, error) {
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock: %w", err)
	}
	lock, err := catalogdata.ParseLock(lockBytes)
	if err != nil {
		return nil, err
	}
	return catalogdata.OpenFile(archivePath, lock)
}

func runInspect(args []string) error {
	flags := flag.NewFlagSet("inspect", flag.ExitOnError)
	archivePath := flags.String("archive", "", "required path of the archive to inspect")
	lockPath := flags.String("lock", "", "path of the lock that identifies the archive; defaults to <archive>.lock.json")
	verbose := flags.Bool("sets", false, "list every set with its coverage")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *archivePath == "" {
		return fmt.Errorf("inspect: -archive is required")
	}
	if *lockPath == "" {
		*lockPath = *archivePath + ".lock.json"
	}
	archive, err := openArchive(*archivePath, *lockPath)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	defer archive.Close()

	// Include the preserved source entries in the integrity check.
	if err := archive.VerifyEntries(); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	cards, printings, incomplete := 0, 0, 0
	if err := archive.EachSet(func(entry catalogdata.IndexEntry, record catalogdata.SetRecord) error {
		cards += len(record.Cards)
		for _, card := range record.Cards {
			printings += len(card.Card.VariantsDetailed)
		}
		if !record.CardsComplete {
			incomplete++
		}
		return nil
	}); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	manifest := archive.Manifest()
	if cards != manifest.AppCounts.Cards || printings != manifest.AppCounts.Printings {
		return fmt.Errorf("inspect: the sets hold %d cards and %d printings, the manifest reports %d and %d",
			cards, printings, manifest.AppCounts.Cards, manifest.AppCounts.Printings)
	}
	fmt.Printf("catalog %s revision %d, format %d\n", manifest.Kind, manifest.Revision, manifest.FormatVersion)
	fmt.Printf("source %s %s %s\n", manifest.SourceAPI, manifest.SourceTag, manifest.SourceCommit)
	fmt.Printf("builder %s, compiler %s\n", manifest.BuilderVersion, manifest.CompilerImageID)
	fmt.Printf("archive %d bytes, sha256 %s\n", archive.Bytes(), archive.SHA256())
	fmt.Printf("source snapshot %d bytes sha256 %s\n", manifest.SourceInventory.ArchiveBytes, manifest.SourceInventory.ArchiveSHA256)
	fmt.Printf("source languages %s\n", strings.Join(manifest.SourceInventory.Languages, ", "))
	fmt.Printf("source series %s\n", strings.Join(manifest.SourceInventory.Series, ", "))
	for _, row := range manifest.SourceInventory.Directories {
		fmt.Printf("  source %s: %d files, %d bytes\n", row.Path, row.Files, row.Bytes)
	}
	fmt.Printf("application languages %s\n", strings.Join(manifest.AppLanguages, ", "))
	fmt.Printf("sets %d, cards %d, printings %d, incomplete sets %d\n", manifest.AppCounts.Sets, cards, printings, incomplete)
	for _, coverage := range manifest.AppCounts.Coverage {
		fmt.Printf("  %s: %d sets, %d cards\n", coverage.Language, coverage.Sets, coverage.Cards)
	}
	fmt.Printf("findings %d\n", len(manifest.Findings))
	for _, finding := range manifest.Findings {
		fmt.Printf("  %s %s %s: %s\n", finding.Code, finding.SetID, finding.Language, finding.Message)
	}
	if *verbose {
		for _, entry := range archive.Index().Sets {
			fmt.Printf("  set %s %q cards %d languages %s complete %t\n",
				entry.SetID, entry.Name, entry.ListedCardCount, strings.Join(entry.Languages, ","), entry.CardsComplete)
		}
	}
	return nil
}

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
	for _, setID := range sortedKeys(afterSets) {
		candidate := afterSets[setID]
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
	for _, setID := range sortedKeys(beforeSets) {
		if _, exists := afterSets[setID]; exists {
			continue
		}
		removed++
		fmt.Printf("- set %s %q cards %d is missing from the candidate\n", setID, beforeSets[setID].Name, beforeSets[setID].ListedCardCount)
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
	languages := make([]string, 0, len(coverage))
	for language := range coverage {
		languages = append(languages, language)
	}
	for _, language := range catalogdata.SortLanguages(languages) {
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
	if strings.Join(previous.Languages, ",") != strings.Join(candidate.Languages, ",") {
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
	missing := make([]string, 0, 4)
	changed := make([]string, 0, 4)
	added := make([]string, 0, 4)
	previousCards := map[string]catalogdata.CardRecord{}
	for _, card := range previousRecord.Cards {
		previousCards[card.Card.ID] = card
	}
	candidateCards := map[string]catalogdata.CardRecord{}
	for _, card := range candidateRecord.Cards {
		candidateCards[card.Card.ID] = card
		if previous, exists := previousCards[card.Card.ID]; !exists {
			added = append(added, card.Card.ID)
		} else if !reflect.DeepEqual(previous, card) {
			changed = append(changed, card.Card.ID)
		}
	}
	for cardID := range previousCards {
		if _, exists := candidateCards[cardID]; !exists {
			missing = append(missing, cardID)
		}
	}
	sort.Strings(missing)
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

func sortedKeys(values map[string]catalogdata.IndexEntry) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
