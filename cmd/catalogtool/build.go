package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogbuild"
	"github.com/furacca/someone-s-pc-catalog/internal/catalogbuild/tcgdexv2"
	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

func runBuild(args []string) error {
	flags := flag.NewFlagSet("build", flag.ExitOnError)
	source := flags.String("source", "", "path to the byte-preserved upstream source snapshot (.tar.gz)")
	generated := flags.String("generated", "", "path to the compiler output directory (server/generated)")
	assetsIndex := flags.String("assets-index", "", "path to the captured assets index JSON")
	out := flags.String("out", "", "required path of the archive to write (<release-tag>.zip)")
	lockPath := flags.String("lock", "", "path of the lock file to write; defaults to <out>.lock.json")
	reportPath := flags.String("report", "", "path of the preparation report to write; defaults to <out>.report.txt")
	revision := flags.Int64("revision", 0, "catalog revision; prepare.sh uses the UTC build time as YYYYMMDDHHMM")
	sourceTag := flags.String("source-tag", "", "upstream release tag")
	sourceCommit := flags.String("source-commit", "", "full upstream commit")
	builderVersion := flags.String("builder-version", "", "identifier of the builder that produced this archive")
	compilerImage := flags.String("compiler-image", "", "identifier of the recorded compiler image")
	artifactURL := flags.String("artifact-url", "", "immutable location the lock should record")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"-source":       *source,
		"-generated":    *generated,
		"-assets-index": *assetsIndex,
		"-out":          *out,
	} {
		if value == "" {
			return fmt.Errorf("build: %s is required", name)
		}
	}
	if *builderVersion == "" {
		return fmt.Errorf("build: -builder-version is required")
	}

	sourceBytes, err := os.ReadFile(*source)
	if err != nil {
		return fmt.Errorf("build: read source snapshot: %w", err)
	}
	assetsBytes, err := os.ReadFile(*assetsIndex)
	if err != nil {
		return fmt.Errorf("build: read assets index: %w", err)
	}
	projection, err := tcgdexv2.Project(*generated)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	sourceDirs, err := catalogbuild.InventorySnapshot(*source, *generated, projection.SourceLanguages)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	input := catalogdata.BuildInput{
		Kind:            catalogdata.KindProduction,
		Revision:        *revision,
		SourceAPI:       "v2",
		SourceTag:       *sourceTag,
		SourceCommit:    *sourceCommit,
		BuilderVersion:  *builderVersion,
		CompilerImageID: *compilerImage,
		AppLanguages:    catalogdata.AppLanguages,
		SourceArchive:   sourceBytes,
		AssetsIndex:     assetsBytes,
		SourceDirs:      sourceDirs,
		SourceLanguages: projection.SourceLanguages,
		SourceSeries:    projection.SourceSeries,
		Sets:            projection.Sets,
		Findings:        projection.Findings,
	}

	staged, err := os.CreateTemp(filepath.Dir(*out), ".catalog-*.zip")
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)

	result, err := catalogdata.Build(staged, input)
	if err != nil {
		staged.Close()
		return fmt.Errorf("build: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	result.Lock.ArtifactURL = *artifactURL

	// Verify the written ZIP before it replaces a previous build.
	archive, err := catalogdata.OpenFile(stagedPath, result.Lock)
	if err != nil {
		return fmt.Errorf("build: verify written archive: %w", err)
	}
	if err := archive.VerifyEntries(); err != nil {
		archive.Close()
		return fmt.Errorf("build: verify written archive: %w", err)
	}
	archive.Close()

	lockBytes, err := catalogdata.MarshalLock(result.Lock)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := os.Rename(stagedPath, *out); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	lockTarget := *lockPath
	if lockTarget == "" {
		lockTarget = *out + ".lock.json"
	}
	if err := os.WriteFile(lockTarget, lockBytes, 0o644); err != nil {
		return fmt.Errorf("build: write lock: %w", err)
	}
	reportTarget := *reportPath
	if reportTarget == "" {
		reportTarget = *out + ".report.txt"
	}
	report := formatReport(result, projection)
	if err := os.WriteFile(reportTarget, []byte(report), 0o644); err != nil {
		return fmt.Errorf("build: write report: %w", err)
	}
	fmt.Print(report)
	return nil
}

func formatReport(result catalogdata.BuildResult, projection tcgdexv2.Result) string {
	manifest := result.Manifest
	out := fmt.Sprintf("catalog %s revision %d, format %d\n", manifest.Kind, manifest.Revision, manifest.FormatVersion)
	out += fmt.Sprintf("source %s %s %s\n", manifest.SourceAPI, manifest.SourceTag, manifest.SourceCommit)
	out += fmt.Sprintf("builder %s, compiler %s\n", manifest.BuilderVersion, manifest.CompilerImageID)
	out += fmt.Sprintf("archive %d bytes, sha256 %s\n", result.Lock.ArchiveBytes, result.Lock.ArchiveSHA256)
	out += fmt.Sprintf("source snapshot %d bytes, %d compiled languages, series %v\n",
		manifest.SourceInventory.ArchiveBytes, projection.Report.SourceLanguages, manifest.SourceInventory.Series)
	out += fmt.Sprintf("source sets %d, source cards %d\n", projection.Report.SourceSets, projection.Report.SourceCards)
	out += fmt.Sprintf("projected sets %d, cards %d, printings %d, skipped Pocket sets %d, incomplete sets %d\n",
		manifest.AppCounts.Sets, manifest.AppCounts.Cards, manifest.AppCounts.Printings,
		projection.Report.SkippedPocketSets, projection.Report.IncompleteSets)
	for _, coverage := range manifest.AppCounts.Coverage {
		out += fmt.Sprintf("  %s: %d sets, %d cards\n", coverage.Language, coverage.Sets, coverage.Cards)
	}
	out += fmt.Sprintf("findings %d\n", len(manifest.Findings))
	for _, finding := range manifest.Findings {
		out += fmt.Sprintf("  %s %s %s: %s\n", finding.Code, finding.SetID, finding.Language, finding.Message)
	}
	return out
}
