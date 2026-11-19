package catalogdata

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func buildFixture(t *testing.T) ([]byte, Lock, Manifest) {
	t.Helper()
	var buffer bytes.Buffer
	result, err := BuildFixture(&buffer)
	if err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	return buffer.Bytes(), result.Lock, result.Manifest
}

func openFixture(t *testing.T) *Archive {
	t.Helper()
	payload, lock, _ := buildFixture(t)
	archive, err := OpenBytes(payload, lock)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	return archive
}

func TestArchiveFixtureRoundTrip(t *testing.T) {
	archive := openFixture(t)
	defer archive.Close()

	manifest := archive.Manifest()
	if manifest.Kind != KindFixture || manifest.Revision != 0 {
		t.Fatalf("unexpected fixture identity %s/%d", manifest.Kind, manifest.Revision)
	}
	if manifest.FormatVersion != FormatVersion {
		t.Fatalf("format version = %d, want %d", manifest.FormatVersion, FormatVersion)
	}

	index := archive.Index()
	if len(index.Sets) != 2 {
		t.Fatalf("index has %d sets, want the two-set fixture", len(index.Sets))
	}
	if index.Sets[0].SetID != "fxa" || index.Sets[1].SetID != "fxb" {
		t.Fatalf("index sets are %q and %q", index.Sets[0].SetID, index.Sets[1].SetID)
	}
	if got := index.Sets[0].Languages; strings.Join(got, ",") != "en,fr,it" {
		t.Fatalf("fxa languages = %v, want en first then sorted", got)
	}

	record, err := archive.Set("fxa")
	if err != nil {
		t.Fatalf("read fxa: %v", err)
	}
	if len(record.Cards) != 3 {
		t.Fatalf("fxa has %d cards, want 3", len(record.Cards))
	}
	for _, localization := range record.Localizations {
		if localization.Language == "en" {
			t.Fatal("English must be the record itself, not a stored localization")
		}
	}
	if manifest.AppCounts.Sets != 2 || manifest.AppCounts.Cards != 5 {
		t.Fatalf("app counts = %d sets / %d cards", manifest.AppCounts.Sets, manifest.AppCounts.Cards)
	}
	if manifest.AppCounts.Printings != 4 {
		t.Fatalf("printing count = %d, want one per provider variant", manifest.AppCounts.Printings)
	}
	if err := archive.VerifyEntries(); err != nil {
		t.Fatalf("verify entries: %v", err)
	}
}

func TestArchiveBuildIsDeterministic(t *testing.T) {
	first, firstLock, _ := buildFixture(t)
	second, secondLock, _ := buildFixture(t)
	if !bytes.Equal(first, second) {
		t.Fatal("identical inputs produced different archive bytes")
	}
	if firstLock != secondLock {
		t.Fatalf("identical inputs produced different locks: %+v vs %+v", firstLock, secondLock)
	}

	// Wall-clock build time must not reach the hashed bytes.
	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	for _, file := range reader.File {
		if !file.Modified.Equal(zipEpoch) {
			t.Fatalf("entry %q carries modification time %s", file.Name, file.Modified)
		}
	}
}

func TestArchiveBuildSortsRecordsAndLanguages(t *testing.T) {
	input := FixtureInput()
	input.Sets = []SetRecord{input.Sets[1], input.Sets[0]}
	reversed := input.Sets[1]
	reversed.Cards = []CardRecord{reversed.Cards[2], reversed.Cards[0], reversed.Cards[1]}
	reversed.Localizations = []SetLocalization{reversed.Localizations[1], reversed.Localizations[0]}
	input.Sets[1] = reversed

	var shuffled bytes.Buffer
	if _, err := Build(&shuffled, input); err != nil {
		t.Fatalf("build shuffled: %v", err)
	}
	ordered, _, _ := buildFixture(t)
	if !bytes.Equal(shuffled.Bytes(), ordered) {
		t.Fatal("input order changed the archive bytes")
	}
}

func TestArchiveRejectsChecksumAndSizeMismatch(t *testing.T) {
	payload, lock, _ := buildFixture(t)

	wrongSum := lock
	wrongSum.ArchiveSHA256 = strings.Repeat("a", 64)
	if _, err := OpenBytes(payload, wrongSum); err == nil {
		t.Fatal("a mismatched checksum was accepted")
	}

	wrongSize := lock
	wrongSize.ArchiveBytes = lock.ArchiveBytes + 1
	if _, err := OpenBytes(payload, wrongSize); err == nil {
		t.Fatal("a mismatched byte size was accepted")
	}

	tampered := append([]byte(nil), payload...)
	tampered[len(tampered)/2] ^= 0xff
	if _, err := OpenBytes(tampered, lock); err == nil {
		t.Fatal("tampered archive bytes were accepted")
	}
}

func TestArchiveRejectsUnsupportedFormatVersion(t *testing.T) {
	payload, lock := rewriteManifest(t, func(manifest *Manifest) {
		manifest.FormatVersion = FormatVersion + 1
	})
	if _, err := OpenBytes(payload, lock); err == nil {
		t.Fatal("an unknown archive format version was accepted")
	}

	original, originalLock, _ := buildFixture(t)
	futureLock := originalLock
	futureLock.FormatVersion = FormatVersion + 1
	if _, err := OpenBytes(original, futureLock); err == nil {
		t.Fatal("an unknown lock format version was accepted")
	}
}

func TestArchiveRejectsIdentityMismatchWithLock(t *testing.T) {
	payload, lock, _ := buildFixture(t)

	wrongKind := lock
	wrongKind.Kind = KindProduction
	wrongKind.Revision = 1
	if _, err := OpenBytes(payload, wrongKind); err == nil {
		t.Fatal("a production lock accepted a fixture archive")
	}

	wrongRevision := lock
	wrongRevision.Revision = 7
	if _, err := OpenBytes(payload, wrongRevision); err == nil {
		t.Fatal("a mismatched revision was accepted")
	}
}

func TestArchiveRejectsInvalidLockValues(t *testing.T) {
	base := Lock{Kind: KindProduction, Revision: 1, FormatVersion: FormatVersion, ArchiveSHA256: strings.Repeat("0", 64), ArchiveBytes: 10}
	for name, mutate := range map[string]func(Lock) Lock{
		"production revision zero": func(l Lock) Lock { l.Revision = 0; return l },
		"negative revision":        func(l Lock) Lock { l.Revision = -1; return l },
		"unknown kind":             func(l Lock) Lock { l.Kind = "staging"; return l },
		"short checksum":           func(l Lock) Lock { l.ArchiveSHA256 = "abcd"; return l },
		"uppercase checksum":       func(l Lock) Lock { l.ArchiveSHA256 = strings.Repeat("A", 64); return l },
		"empty artifact bytes":     func(l Lock) Lock { l.ArchiveBytes = 0; return l },
		"oversized artifact":       func(l Lock) Lock { l.ArchiveBytes = MaxArchiveBytes + 1; return l },
		"credentials in url":       func(l Lock) Lock { l.ArtifactURL = "https://user:token@example.invalid/a.zip"; return l },
	} {
		if err := ValidateLock(mutate(base)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if err := ValidateLock(base); err != nil {
		t.Fatalf("a valid lock was rejected: %v", err)
	}
	// A fixture is not pinned to revision 0: the upgrade and downgrade rules
	// have to be exercisable with fixtures.
	fixture := base
	fixture.Kind = KindFixture
	fixture.Revision = 3
	if err := ValidateLock(fixture); err != nil {
		t.Fatalf("a fixture with a later revision was rejected: %v", err)
	}

	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"}", "]", "{}"} {
		if _, err := ParseLock(append(append([]byte(nil), encoded...), suffix...)); err == nil {
			t.Fatalf("lock accepted trailing %q", suffix)
		}
	}
}

func TestArchiveRejectsDuplicateAndUnsafeEntries(t *testing.T) {
	for name, entryName := range map[string]string{
		"absolute path":  "/etc/passwd",
		"parent escape":  "catalog/../../escape.json",
		"windows drive":  "c:/escape.json",
		"backslash path": `catalog\escape.json`,
		"directory":      "catalog/",
	} {
		payload, lock := archiveWithExtraEntry(t, entryName, []byte("{}"))
		if _, err := OpenBytes(payload, lock); err == nil {
			t.Fatalf("%s entry %q was accepted", name, entryName)
		}
	}

	payload, lock := archiveWithExtraEntry(t, IndexPath, []byte("{}"))
	if _, err := OpenBytes(payload, lock); err == nil {
		t.Fatal("a duplicate entry was accepted")
	}

	payload, lock = archiveWithExtraEntry(t, "catalog/sets/ghost.json", []byte("{}"))
	if _, err := OpenBytes(payload, lock); err == nil {
		t.Fatal("an entry missing from the inventory was accepted")
	}
}

func TestArchiveRejectsInventoryDisagreement(t *testing.T) {
	payload, lock := rewriteManifest(t, func(manifest *Manifest) {
		for index := range manifest.Entries {
			if manifest.Entries[index].Path == IndexPath {
				manifest.Entries[index].SHA256 = strings.Repeat("b", 64)
			}
		}
	})
	if _, err := OpenBytes(payload, lock); err == nil {
		t.Fatal("an inventory hash mismatch was accepted")
	}

	payload, lock = rewriteManifest(t, func(manifest *Manifest) {
		for index := range manifest.Entries {
			if manifest.Entries[index].Path == SourceArchivePath {
				manifest.Entries[index].Bytes++
			}
		}
	})
	if _, err := OpenBytes(payload, lock); err == nil {
		t.Fatal("an inventory size mismatch on the source snapshot was accepted")
	}

	payload, lock = rewriteManifest(t, func(manifest *Manifest) {
		kept := manifest.Entries[:0]
		for _, entry := range manifest.Entries {
			if entry.Path == SourceArchivePath {
				continue
			}
			kept = append(kept, entry)
		}
		manifest.Entries = kept
	})
	if _, err := OpenBytes(payload, lock); err == nil {
		t.Fatal("an archive without an inventoried source snapshot was accepted")
	}
}

func TestArchiveRejectsCorruptedSourceSnapshot(t *testing.T) {
	payload, lock, _ := buildFixture(t)
	archive, err := OpenBytes(payload, lock)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer archive.Close()
	if err := archive.VerifyEntries(); err != nil {
		t.Fatalf("verify entries: %v", err)
	}

	// A corrupted source snapshot changes the whole-archive checksum, so the
	// lock rejects it before any entry is read.
	corrupted := corruptEntry(t, payload, SourceArchivePath)
	if _, err := OpenBytes(corrupted, lock); err == nil {
		t.Fatal("a corrupted source snapshot was accepted")
	}
}

func TestArchiveEnforcesBounds(t *testing.T) {
	payload, lock, _ := buildFixture(t)
	if int64(len(payload)) > MaxArchiveBytes {
		t.Fatalf("the fixture is %d bytes, over the %d byte limit", len(payload), MaxArchiveBytes)
	}

	oversizedLock := lock
	oversizedLock.ArchiveBytes = MaxArchiveBytes + 1
	if err := ValidateLock(oversizedLock); err == nil {
		t.Fatal("an oversized locked artifact was accepted")
	}

	// A ZIP header that understates its entry must not let the reader expand
	// an unbounded amount of data.
	bomb := archiveWithLyingEntry(t)
	if _, err := OpenBytes(bomb.payload, bomb.lock); err == nil {
		t.Fatal("an entry larger than its inventory row was accepted")
	}
}

func TestArchivePreservesFullSourceInventory(t *testing.T) {
	archive := openFixture(t)
	defer archive.Close()

	inventory := archive.Manifest().SourceInventory
	if inventory.ArchiveBytes <= 0 || inventory.AssetsIndexBytes <= 0 {
		t.Fatal("the source inventory reports no preserved bytes")
	}
	if strings.Join(inventory.Series, ",") != "fx,tcgp" {
		t.Fatalf("source series = %v, want the Pocket series retained", inventory.Series)
	}
	if !contains(inventory.Languages, "ja") || !contains(inventory.Languages, "zh-tw") {
		t.Fatalf("source languages = %v, want catalogs outside the application scope retained", inventory.Languages)
	}

	// The projection drops Pocket and unsupported languages; the snapshot must
	// still contain them byte for byte.
	for _, entry := range archive.Index().Sets {
		if IsPocketSeries(entry.SeriesID) {
			t.Fatalf("set %q was projected from the Pocket series", entry.SetID)
		}
		for _, language := range entry.Languages {
			if !IsAppLanguage(language) {
				t.Fatalf("set %q was projected with language %q", entry.SetID, language)
			}
		}
	}

	source, err := archive.OpenSource()
	if err != nil {
		t.Fatalf("open source snapshot: %v", err)
	}
	defer source.Close()
	files := readTarGz(t, source)
	for _, required := range []string{
		"server/generated/ja/cards.json",
		"server/generated/zh-tw/cards.json",
		"LICENSE",
	} {
		if _, exists := files[required]; !exists {
			t.Fatalf("the source snapshot lost %q", required)
		}
	}
	if !strings.Contains(files["server/generated/en/sets.json"], `"serie":{"id":"tcgp"}`) {
		t.Fatal("the source snapshot lost the Pocket series records")
	}
	if !strings.Contains(files["server/generated/en/cards.json"], `"pricing"`) {
		t.Fatal("the source snapshot lost fields the application does not map")
	}
	if !strings.Contains(files["server/generated/en/sets.json"], `"boosters"`) {
		t.Fatal("the source snapshot lost fields the application does not map")
	}
}

func TestArchiveStreamsOneSetAtATime(t *testing.T) {
	archive := openFixture(t)
	defer archive.Close()

	visited := make([]string, 0, 2)
	if err := archive.EachSet(func(entry IndexEntry, record SetRecord) error {
		if record.Set.ID != entry.SetID {
			t.Fatalf("set %q was served for index entry %q", record.Set.ID, entry.SetID)
		}
		visited = append(visited, entry.SetID)
		return nil
	}); err != nil {
		t.Fatalf("iterate sets: %v", err)
	}
	if strings.Join(visited, ",") != "fxa,fxb" {
		t.Fatalf("visited %v, want index order", visited)
	}

	if _, err := archive.Set("missing"); err == nil {
		t.Fatal("a set outside the index was served")
	}
}

func TestArchiveRejectsMalformedProjectionDespiteValidSource(t *testing.T) {
	// The preserved source is untouched in every case below, so a projection
	// error cannot pass because its source snapshot is still valid.
	cases := map[string]func(*BuildInput){
		"card in another set": func(in *BuildInput) {
			in.Sets[0].Cards[0].Card.SetID = "fxb"
		},
		"duplicate card": func(in *BuildInput) {
			in.Sets[0].Cards[1].Card.ID = in.Sets[0].Cards[0].Card.ID
		},
		"duplicate collector number": func(in *BuildInput) {
			in.Sets[0].Cards[1].Card.LocalID = " 1 "
			in.Sets[0].Cards[1].Localizations = nil
		},
		"duplicate fallback collector number": func(in *BuildInput) {
			in.Sets[0].Cards[1].Card.ID = "fxa-01"
			in.Sets[0].Cards[1].Card.LocalID = ""
			in.Sets[0].Cards[1].Localizations = nil
			in.Sets[0].Cards[0].Card.LocalID = "01"
			in.Sets[0].Cards[0].Localizations = nil
		},
		"missing collector number": func(in *BuildInput) {
			in.Sets[0].Cards[0].Card.ID = "unnumbered"
			in.Sets[0].Cards[0].Card.LocalID = ""
			in.Sets[0].Cards[0].Localizations = nil
		},
		"duplicate printing reference": func(in *BuildInput) {
			card := &in.Sets[0].Cards[0].Card
			card.VariantsDetailed = []Variant{
				{Type: "normal", VariantID: "same-id"},
				{Type: "holo", VariantID: " same-id "},
			}
		},
		"conflicting printing identities": func(in *BuildInput) {
			in.Sets[0].Cards[0].Card.VariantsDetailed = []Variant{
				{Type: "normal", Stamp: []string{"staff"}, VariantID: "one"},
				{Type: " normal ", Size: " standard ", Stamp: []string{" staff ", "staff", ""}, VariantID: "two"},
			}
		},
		"card without a name": func(in *BuildInput) {
			in.Sets[0].Cards[0].Card.Name = " "
		},
		"pocket set projected": func(in *BuildInput) {
			in.Sets[0].Set.SeriesID = PocketSeriesID
		},
		"language outside scope": func(in *BuildInput) {
			in.Sets[0].Localizations[0].Language = "ja"
		},
		"stored english localization": func(in *BuildInput) {
			in.Sets[0].Localizations[0].Language = "en"
		},
		"localization of another card": func(in *BuildInput) {
			in.Sets[0].Cards[0].Localizations[0].Card.ID = "fxa-9"
		},
		"localization renumbered": func(in *BuildInput) {
			in.Sets[0].Cards[0].Localizations[0].Card.LocalID = "99"
		},
		"image outside the asset host": func(in *BuildInput) {
			in.Sets[0].Cards[0].Card.Image = "https://cdn.example.invalid/fxa/1"
		},
		"insecure image reference": func(in *BuildInput) {
			in.Sets[0].Set.Logo = "http://" + AssetHost + "/en/fx/fxa/logo"
		},
		"image with credentials": func(in *BuildInput) {
			in.Sets[0].Set.Symbol = "https://user:token@" + AssetHost + "/en/fx/fxa/symbol"
		},
		"unset incomplete reason": func(in *BuildInput) {
			in.Sets[0].CardsComplete = false
		},
		"variants without presence": func(in *BuildInput) {
			in.Sets[0].Cards[2].Card.VariantsDetailed = []Variant{{Type: "normal"}}
		},
		"printing without a type": func(in *BuildInput) {
			in.Sets[0].Cards[0].Card.VariantsDetailed[0].Type = " "
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := FixtureInput()
			mutate(&input)
			if _, err := Build(io.Discard, input); err == nil {
				t.Fatal("malformed projection was accepted")
			}
			payload, lock := rewriteEntry(t, SetEntryPath("fxa"), func([]byte) []byte {
				encoded, err := MarshalCanonical(normalizeSetRecord(input.Sets[0]))
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			})
			archive, err := OpenBytes(payload, lock)
			if err != nil {
				t.Fatalf("open archive with a valid checksum: %v", err)
			}
			defer archive.Close()
			if _, err := archive.Set("fxa"); err == nil {
				t.Fatal("malformed projection was accepted by the archive reader")
			}
		})
	}
	t.Run("allows unreferenced IDs and references scoped to another card", func(t *testing.T) {
		input := FixtureInput()
		input.Sets[0].Cards[0].Card.VariantsDetailed = []Variant{
			{Type: "normal", VariantID: "shared"},
			{Type: " normal ", Size: "standard", VariantID: " shared "},
			{Type: "holo", VariantID: "shared", Stamp: []string{" 1ST-EDITION "}},
			{Type: "normal", VariantID: "generated"},
			{Type: "holo", VariantID: "generated"},
			{Type: "normal"},
			{Type: "holo"},
		}
		input.Sets[0].Cards[1].Card.VariantsDetailed = []Variant{{Type: "normal", VariantID: "shared"}}
		if _, err := Build(io.Discard, input); err != nil {
			t.Fatalf("valid printing references were rejected: %v", err)
		}
	})
}

func TestArchiveRejectsUnknownFieldsInEntries(t *testing.T) {
	payload, lock := rewriteEntry(t, IndexPath, func(original []byte) []byte {
		return append(bytes.TrimRight(bytes.TrimSpace(original), "}"), []byte(`,"future":1}`)...)
	})
	if _, err := OpenBytes(payload, lock); err == nil {
		t.Fatal("an index with unknown fields was accepted")
	}
}

func TestVariantPresenceSurvivesRoundTrip(t *testing.T) {
	archive := openFixture(t)
	defer archive.Close()

	record, err := archive.Set("fxa")
	if err != nil {
		t.Fatalf("read fxa: %v", err)
	}
	byID := map[string]Card{}
	for _, card := range record.Cards {
		byID[card.Card.ID] = card.Card
	}

	authoritativeEmpty := byID["fxa-2"]
	if !authoritativeEmpty.VariantsDetailedPresent {
		t.Fatal("an explicitly empty variants_detailed array lost its presence flag")
	}
	if len(authoritativeEmpty.VariantsDetailed) != 0 {
		t.Fatalf("fxa-2 has %d variants, want an empty authoritative array", len(authoritativeEmpty.VariantsDetailed))
	}

	unknown := byID["fxa-3"]
	if unknown.VariantsDetailedPresent {
		t.Fatal("an absent variants_detailed field was read as present")
	}
	if len(unknown.VariantsDetailed) != 0 {
		t.Fatal("an absent variants_detailed field produced variants")
	}
}

func TestVariantPresenceIsSerializedExplicitly(t *testing.T) {
	// The distinction must live in the archive bytes, not in a decoder that a
	// future reader could drop.
	payload, lock, _ := buildFixture(t)
	archive, err := OpenBytes(payload, lock)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer archive.Close()

	raw, err := archive.readRaw(SetEntryPath("fxa"), MaxEntryBytes)
	if err != nil {
		t.Fatalf("read raw set: %v", err)
	}
	var decoded struct {
		Cards []struct {
			Card struct {
				ID                      string            `json:"id"`
				VariantsDetailed        []json.RawMessage `json:"variants_detailed"`
				VariantsDetailedPresent *bool             `json:"variants_detailed_present"`
			} `json:"card"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode raw set: %v", err)
	}
	seen := 0
	for _, card := range decoded.Cards {
		if card.Card.VariantsDetailedPresent == nil {
			t.Fatalf("card %q has no serialized variants_detailed_present field", card.Card.ID)
		}
		switch card.Card.ID {
		case "fxa-2":
			if !*card.Card.VariantsDetailedPresent {
				t.Fatal("fxa-2 was serialized as absent")
			}
			seen++
		case "fxa-3":
			if *card.Card.VariantsDetailedPresent {
				t.Fatal("fxa-3 was serialized as present")
			}
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("checked %d cards, want both presence cases", seen)
	}
}

// Nonempty variants cannot be marked absent.
func TestVariantPresenceRejectsContradictoryFlagInArchive(t *testing.T) {
	payload, lock := rewriteEntry(t, SetEntryPath("fxa"), func(original []byte) []byte {
		return bytes.ReplaceAll(original, []byte(`"variants_detailed_present":true`), []byte(`"variants_detailed_present":false`))
	})
	archive, err := OpenBytes(payload, lock)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer archive.Close()
	if _, err := archive.Set("fxa"); err == nil {
		t.Fatal("nonempty variants marked absent were accepted")
	}

	t.Run("missing flag on an empty variants array", func(t *testing.T) {
		payload, lock := rewriteEntry(t, SetEntryPath("fxa"), func(original []byte) []byte {
			var record map[string]any
			if err := json.Unmarshal(original, &record); err != nil {
				t.Fatal(err)
			}
			for _, row := range record["cards"].([]any) {
				card := row.(map[string]any)["card"].(map[string]any)
				if card["id"] == "fxa-2" {
					delete(card, "variants_detailed_present")
				}
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		})
		archive, err := OpenBytes(payload, lock)
		if err != nil {
			t.Fatal(err)
		}
		defer archive.Close()
		if _, err := archive.Set("fxa"); err == nil {
			t.Fatal("empty variants without an explicit presence flag were accepted")
		}
	})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// rewriteManifest rebuilds a fixture archive with a mutated manifest, keeping
// every other entry intact, and returns a lock that matches the new bytes.
func rewriteManifest(t *testing.T, mutate func(*Manifest)) ([]byte, Lock) {
	t.Helper()
	return rewriteEntry(t, ManifestPath, func(original []byte) []byte {
		var manifest Manifest
		if err := json.Unmarshal(original, &manifest); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		mutate(&manifest)
		encoded, err := MarshalCanonical(manifest)
		if err != nil {
			t.Fatalf("encode manifest: %v", err)
		}
		return encoded
	})
}

// rewriteEntry rebuilds a fixture archive replacing one entry's bytes and
// repairing the manifest inventory row for it, so the test exercises the check
// it is about rather than tripping the whole-archive checksum or a stale
// inventory hash first.
func rewriteEntry(t *testing.T, path string, mutate func([]byte) []byte) ([]byte, Lock) {
	t.Helper()
	return rewriteEntryRaw(t, path, mutate, path != ManifestPath)
}

// rewriteEntryRaw rebuilds a fixture archive replacing one entry's bytes,
// optionally leaving the manifest inventory stale.
func rewriteEntryRaw(t *testing.T, path string, mutate func([]byte) []byte, repairInventory bool) ([]byte, Lock) {
	t.Helper()
	original, _, _ := buildFixture(t)
	reader, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	payloads := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		payload := readZipEntry(t, file)
		if file.Name == path {
			payload = mutate(payload)
		}
		payloads[file.Name] = payload
	}
	if repairInventory {
		var manifest Manifest
		if err := json.Unmarshal(payloads[ManifestPath], &manifest); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		found := false
		for index := range manifest.Entries {
			if manifest.Entries[index].Path != path {
				continue
			}
			found = true
			manifest.Entries[index].Bytes = int64(len(payloads[path]))
			sum := sha256.Sum256(payloads[path])
			manifest.Entries[index].SHA256 = hex.EncodeToString(sum[:])
		}
		if !found {
			t.Fatalf("entry %q is not in the manifest inventory", path)
		}
		encoded, err := MarshalCanonical(manifest)
		if err != nil {
			t.Fatalf("encode manifest: %v", err)
		}
		payloads[ManifestPath] = encoded
	}

	var rebuilt bytes.Buffer
	writer := zip.NewWriter(&rebuilt)
	for _, file := range reader.File {
		header := &zip.FileHeader{Name: file.Name, Modified: zipEpoch, Method: file.Method}
		header.SetMode(zipEntryMode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		if _, err := entry.Write(payloads[file.Name]); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return rebuilt.Bytes(), lockFor(t, rebuilt.Bytes())
}

// archiveWithExtraEntry appends an entry the manifest inventory does not
// describe, without touching the existing entries.
func archiveWithExtraEntry(t *testing.T, name string, payload []byte) ([]byte, Lock) {
	t.Helper()
	original, _, _ := buildFixture(t)
	reader, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var rebuilt bytes.Buffer
	writer := zip.NewWriter(&rebuilt)
	for _, file := range reader.File {
		header := &zip.FileHeader{Name: file.Name, Modified: zipEpoch, Method: file.Method}
		header.SetMode(zipEntryMode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		if _, err := entry.Write(readZipEntry(t, file)); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	header := &zip.FileHeader{Name: name, Modified: zipEpoch, Method: zip.Deflate}
	header.SetMode(zipEntryMode)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("create extra entry: %v", err)
	}
	if !strings.HasSuffix(name, "/") {
		if _, err := entry.Write(payload); err != nil {
			t.Fatalf("write extra entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return rebuilt.Bytes(), lockFor(t, rebuilt.Bytes())
}

type boundedArchive struct {
	payload []byte
	lock    Lock
}

// archiveWithLyingEntry stores an entry whose real content is longer than the
// size its inventory row declares.
func archiveWithLyingEntry(t *testing.T) boundedArchive {
	t.Helper()
	payload, lock := rewriteEntryRaw(t, IndexPath, func(original []byte) []byte {
		return append(original, bytes.Repeat([]byte(" "), 4096)...)
	}, false)
	return boundedArchive{payload: payload, lock: lock}
}

func corruptEntry(t *testing.T, payload []byte, path string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var rebuilt bytes.Buffer
	writer := zip.NewWriter(&rebuilt)
	for _, file := range reader.File {
		content := readZipEntry(t, file)
		if file.Name == path && len(content) > 0 {
			content = append([]byte(nil), content...)
			content[len(content)/2] ^= 0xff
		}
		header := &zip.FileHeader{Name: file.Name, Modified: zipEpoch, Method: file.Method}
		header.SetMode(zipEntryMode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return rebuilt.Bytes()
}

func readZipEntry(t *testing.T, file *zip.File) []byte {
	t.Helper()
	reader, err := file.Open()
	if err != nil {
		t.Fatalf("open entry %q: %v", file.Name, err)
	}
	defer reader.Close()
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read entry %q: %v", file.Name, err)
	}
	return payload
}

func lockFor(t *testing.T, payload []byte) Lock {
	t.Helper()
	sum := sha256.Sum256(payload)
	return Lock{
		Kind:          KindFixture,
		Revision:      0,
		FormatVersion: FormatVersion,
		ArchiveSHA256: hex.EncodeToString(sum[:]),
		ArchiveBytes:  int64(len(payload)),
	}
}

func readTarGz(t *testing.T, source io.Reader) map[string]string {
	t.Helper()
	decompressed, err := gzip.NewReader(source)
	if err != nil {
		t.Fatalf("open source snapshot: %v", err)
	}
	defer decompressed.Close()
	files := map[string]string{}
	reader := tar.NewReader(decompressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read source snapshot: %v", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read source file %q: %v", header.Name, err)
		}
		if !header.ModTime.Equal(fixtureEpoch) {
			t.Fatalf("source file %q carries modification time %s", header.Name, header.ModTime)
		}
		files[header.Name] = string(body)
	}
	return files
}
