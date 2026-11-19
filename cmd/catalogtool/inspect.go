package main

import (
	"flag"
	"fmt"
	"os"
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
