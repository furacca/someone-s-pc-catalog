package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

type sourceFile struct {
	Path string
	Body string
}

const testSeries = `[{"id":"fx","name":"Fixture Series"},{"id":"tcgp","name":"Pocket"}]`

func englishSets(cards string) string {
	return `[{"id":"fxa","name":"Fixture Alpha","logo":"https://assets.tcgdex.net/en/fx/fxa/logo","symbol":"https://assets.tcgdex.net/en/fx/fxa/symbol","releaseDate":"2024-02-01","serie":{"id":"fx","name":"Fixture Series"},"cardCount":{"official":2,"total":2},"cards":` + cards + `},` +
		`{"id":"tcgp1","name":"Pocket","serie":{"id":"tcgp"},"cardCount":{"official":1,"total":1},"cards":[{"id":"tcgp1-1"}]}]`
}

const englishCards = `[
 {"id":"fxa-1","localId":"1","name":"Fixture Bulba","image":"https://assets.tcgdex.net/en/fx/fxa/1","set":{"id":"fxa"},"variants_detailed":[{"type":"normal","size":"standard","stamp":[],"variantId":"fxa-1-normal"}]},
 {"id":"fxa-2","localId":"2","name":"Fixture Squirt","image":"https://assets.tcgdex.net/en/fx/fxa/2","set":{"id":"fxa"}},
 {"id":"tcgp1-1","localId":"1","name":"Pocket Card","set":{"id":"tcgp1"}}
]`

const italianSets = `[{"id":"fxa","name":"Fixture Alfa","serie":{"id":"fx"},"cardCount":{"official":2,"total":2},"cards":[{"id":"fxa-1"}]}]`
const italianCards = `[{"id":"fxa-1","localId":"1","name":"Bulba Fixture","set":{"id":"fxa"}}]`
const japaneseSets = `[{"id":"fxa","name":"JA","serie":{"id":"fx"},"cardCount":{"official":1,"total":1},"cards":[{"id":"fxa-1"}]}]`
const japaneseCards = `[{"id":"fxa-1","localId":"1","name":"JA","set":{"id":"fxa"}}]`

// generatedLayout is the compiler output the preparation step would keep,
// including a catalog the application never projects.
func generatedLayout(cardRefs string) map[string]map[string]string {
	return map[string]map[string]string{
		"en": {"series.json": testSeries, "sets.json": englishSets(cardRefs), "cards.json": englishCards, "stats.json": `{"cards":3}`},
		"it": {"series.json": testSeries, "sets.json": italianSets, "cards.json": italianCards, "stats.json": `{}`},
		"ja": {"series.json": testSeries, "sets.json": japaneseSets, "cards.json": japaneseCards, "stats.json": `{}`},
	}
}

func writeGenerated(t *testing.T, root string, layout map[string]map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, "generated")
	for language, files := range layout {
		if err := os.MkdirAll(filepath.Join(dir, language), 0o755); err != nil {
			t.Fatalf("create generated dir: %v", err)
		}
		for name, payload := range files {
			if err := os.WriteFile(filepath.Join(dir, language, name), []byte(payload), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	return dir
}

func writeSnapshot(t *testing.T, path string, layout map[string]map[string]string, extra []sourceFile) {
	t.Helper()
	var raw bytes.Buffer
	gzipWriter := gzip.NewWriter(&raw)
	tarWriter := tar.NewWriter(gzipWriter)
	write := func(name, body string) {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write snapshot header: %v", err)
		}
		if _, err := tarWriter.Write([]byte(body)); err != nil {
			t.Fatalf("write snapshot body: %v", err)
		}
	}
	write("LICENSE", "Fixture upstream license\n")
	for language, files := range layout {
		for name, payload := range files {
			write("tcgdex/server/generated/"+language+"/"+name, payload)
		}
	}
	for _, file := range extra {
		write(file.Path, file.Body)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

type prepared struct {
	root      string
	generated string
	snapshot  string
	assets    string
}

func prepare(t *testing.T, cardRefs string) prepared {
	t.Helper()
	root := t.TempDir()
	layout := generatedLayout(cardRefs)
	generated := writeGenerated(t, root, layout)
	snapshot := filepath.Join(root, "upstream.tar.gz")
	writeSnapshot(t, snapshot, layout, nil)
	assets := filepath.Join(root, "assets-index.json")
	if err := os.WriteFile(assets, []byte(`{"en/fx/fxa/1":["high.webp"]}`), 0o644); err != nil {
		t.Fatalf("write assets index: %v", err)
	}
	return prepared{root: root, generated: generated, snapshot: snapshot, assets: assets}
}

func buildArgs(input prepared, out string, extra ...string) []string {
	args := []string{
		"-source", input.snapshot,
		"-generated", input.generated,
		"-assets-index", input.assets,
		"-out", out,
		"-revision", "1",
		"-source-tag", "v2.47.0",
		"-source-commit", strings.Repeat("a", 40),
		"-builder-version", "catalogtool-test/1",
		"-compiler-image", "test-image",
	}
	return append(args, extra...)
}

func TestBuildProducesAVerifiableArchiveAndLock(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	out := filepath.Join(input.root, "catalog.zip")
	if err := runBuild(buildArgs(input, out)); err != nil {
		t.Fatalf("build: %v", err)
	}

	lockBytes, err := os.ReadFile(out + ".lock.json")
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	lock, err := catalogdata.ParseLock(lockBytes)
	if err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	if lock.Kind != catalogdata.KindProduction || lock.Revision != 1 {
		t.Fatalf("lock identity = %s/%d", lock.Kind, lock.Revision)
	}
	archive, err := catalogdata.OpenFile(out, lock)
	if err != nil {
		t.Fatalf("open built archive: %v", err)
	}
	defer archive.Close()
	if err := archive.VerifyEntries(); err != nil {
		t.Fatalf("verify built archive: %v", err)
	}
	if len(archive.Index().Sets) != 1 || archive.Index().Sets[0].SetID != "fxa" {
		t.Fatalf("index = %+v, want the Pocket set filtered out", archive.Index().Sets)
	}
	manifest := archive.Manifest()
	if manifest.SourceAPI != "v2" {
		t.Fatalf("source API = %q, want v2", manifest.SourceAPI)
	}
	if !slices.Contains(manifest.SourceInventory.Languages, "ja") {
		t.Fatalf("source languages %v, want the unprojected catalog retained", manifest.SourceInventory.Languages)
	}
	if !slices.Contains(manifest.SourceInventory.Series, "tcgp") {
		t.Fatalf("source series %v, want the Pocket series retained", manifest.SourceInventory.Series)
	}

	// The preparation report is written next to the artifact and is not part
	// of the hashed archive.
	report, err := os.ReadFile(out + ".report.txt")
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(report), "projected sets 1") {
		t.Fatalf("report = %s", report)
	}

	if err := runInspect([]string{"-archive", out, "-sets"}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	first := filepath.Join(input.root, "first.zip")
	second := filepath.Join(input.root, "second.zip")
	if err := runBuild(buildArgs(input, first)); err != nil {
		t.Fatalf("build first: %v", err)
	}
	if err := runBuild(buildArgs(input, second)); err != nil {
		t.Fatalf("build second: %v", err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("the same inputs produced different artifacts")
	}
}

func TestInspectRejectsManifestCountsThatDisagreeWithTheSets(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	out := filepath.Join(input.root, "catalog.zip")
	if err := runBuild(buildArgs(input, out)); err != nil {
		t.Fatalf("build: %v", err)
	}
	original, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()

	var tampered bytes.Buffer
	writer := zip.NewWriter(&tampered)
	for _, file := range original.File {
		if file.Name != catalogdata.ManifestPath {
			if err := writer.Copy(file); err != nil {
				t.Fatal(err)
			}
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		lying := bytes.Replace(manifest, []byte(`"app_counts":{"sets":1,"cards":2`), []byte(`"app_counts":{"sets":1,"cards":3`), 1)
		if bytes.Equal(lying, manifest) {
			t.Fatal("the manifest card count was not found")
		}
		entry, err := writer.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(lying); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(tampered.Bytes())
	lock, err := catalogdata.MarshalLock(catalogdata.Lock{
		Kind:          catalogdata.KindProduction,
		Revision:      1,
		FormatVersion: catalogdata.FormatVersion,
		ArchiveSHA256: hex.EncodeToString(sum[:]),
		ArchiveBytes:  int64(tampered.Len()),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(input.root, "tampered.zip")
	if err := os.WriteFile(path, tampered.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock.json", lock, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInspect([]string{"-archive", path}); err == nil || !strings.Contains(err.Error(), "manifest reports") {
		t.Fatalf("inspect error = %v, want a count mismatch", err)
	}
}

func TestBuildFailsOnAMissingReferencedCard(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-9"}]`)
	out := filepath.Join(input.root, "catalog.zip")
	err := runBuild(buildArgs(input, out))
	if err == nil {
		t.Fatal("a catalog with a missing referenced card was built")
	}
	if !strings.Contains(err.Error(), "fxa-9") {
		t.Fatalf("error = %v, want the missing card named", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("a failed preparation left an artifact behind")
	}
}

func TestBuildRefusesASnapshotThatIsNotTheCompiledInput(t *testing.T) {
	for _, scenario := range []string{"missing language", "changed cards", "changed sets", "changed series", "missing sets", "duplicate cards"} {
		t.Run(scenario, func(t *testing.T) {
			input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
			layout := generatedLayout(`[{"id":"fxa-1"},{"id":"fxa-2"}]`)
			var extra []sourceFile
			switch scenario {
			case "missing language":
				delete(layout, "ja")
			case "changed cards":
				layout["en"]["cards.json"] = strings.Replace(englishCards, "Fixture Bulba", "Different name", 1)
			case "changed sets":
				layout["en"]["sets.json"] = englishSets(`[{"id":"fxa-1"}]`)
			case "changed series":
				layout["ja"]["series.json"] = `[]`
			case "missing sets":
				delete(layout["it"], "sets.json")
			case "duplicate cards":
				extra = []sourceFile{{Path: "other/server/generated/en/cards.json", Body: englishCards}}
			}
			writeSnapshot(t, input.snapshot, layout, extra)
			out := filepath.Join(input.root, "catalog.zip")
			if err := runBuild(buildArgs(input, out)); err == nil {
				t.Fatal("a snapshot that does not match the compiled input was accepted")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("failed build left a final artifact: %v", err)
			}
		})
	}
}

func TestBuildRejectsAnInvalidProductionIdentity(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	out := filepath.Join(input.root, "catalog.zip")
	if err := runBuild(buildArgs(input, out, "-revision", "0")); err == nil {
		t.Fatal("a production catalog with revision 0 was built")
	}
	if err := runBuild(buildArgs(input, out, "-source-commit", "abc")); err == nil {
		t.Fatal("a production catalog without a full commit was built")
	}
}

func TestDiffReportsAddedChangedAndMissingSets(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	previous := filepath.Join(input.root, "previous.zip")
	if err := runBuild(buildArgs(input, previous)); err != nil {
		t.Fatalf("build previous: %v", err)
	}

	// The candidate drops one card from the set.
	reduced := prepare(t, `[{"id":"fxa-1"}]`)
	candidate := filepath.Join(reduced.root, "candidate.zip")
	if err := runBuild(buildArgs(reduced, candidate, "-revision", "2")); err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	if err := runDiff([]string{"-old", previous, "-new", candidate}); err != nil {
		t.Fatalf("diff: %v", err)
	}

	before, err := openArchive(previous, previous+".lock.json")
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	for _, field := range []string{"variant", "translation", "card name", "set date"} {
		t.Run(field, func(t *testing.T) {
			changed := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
			layout := generatedLayout(`[{"id":"fxa-1"},{"id":"fxa-2"}]`)
			switch field {
			case "variant":
				layout["en"]["cards.json"] = strings.Replace(englishCards, `"size":"standard"`, `"size":"standard","foil":"cosmos"`, 1)
			case "translation":
				layout["it"]["cards.json"] = strings.Replace(italianCards, "Bulba Fixture", "Changed translation", 1)
			case "card name":
				layout["en"]["cards.json"] = strings.Replace(englishCards, "Fixture Bulba", "Changed name", 1)
			case "set date":
				layout["en"]["sets.json"] = strings.Replace(layout["en"]["sets.json"], "2024-02-01", "2024-03-01", 1)
			}
			writeGenerated(t, changed.root, layout)
			writeSnapshot(t, changed.snapshot, layout, nil)
			path := filepath.Join(changed.root, "changed.zip")
			if err := runBuild(buildArgs(changed, path, "-revision", "2")); err != nil {
				t.Fatal(err)
			}
			after, err := openArchive(path, path+".lock.json")
			if err != nil {
				t.Fatal(err)
			}
			defer after.Close()
			if summary, err := compareSets(before, after, "fxa", before.Index().Sets[0], after.Index().Sets[0]); err != nil || summary == "" {
				t.Fatalf("diff hid a changed %s with unchanged counts", field)
			}
		})
	}

	t.Run("completeness and findings", func(t *testing.T) {
		open := func(reason, finding string) *catalogdata.Archive {
			t.Helper()
			input := catalogdata.FixtureInput()
			input.Sets[0].CardsComplete = false
			input.Sets[0].IncompleteReason = reason
			input.Findings = append(input.Findings, catalogdata.Finding{
				Code: catalogdata.FindingSetIncomplete, SetID: input.Sets[0].Set.ID, Message: finding,
			})
			var payload bytes.Buffer
			result, err := catalogdata.Build(&payload, input)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := catalogdata.OpenBytes(payload.Bytes(), result.Lock)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { archive.Close() })
			return archive
		}
		before := open("original reason", "original finding")
		for _, scenario := range []struct{ name, reason, finding, want string }{
			{"unchanged", "original reason", "original finding", ""},
			{"incomplete reason", "changed reason", "original finding", "incomplete reason"},
			{"set finding", "original reason", "changed finding", "findings changed"},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				after := open(scenario.reason, scenario.finding)
				summary, err := compareSets(before, after, "fxa", before.Index().Sets[0], after.Index().Sets[0])
				if err != nil || !strings.Contains(summary, scenario.want) || scenario.want == "" && summary != "" {
					t.Fatalf("diff = %q, want %q: %v", summary, scenario.want, err)
				}
			})
		}
	})

	if err := runDiff([]string{"-old", candidate, "-new", previous}); err == nil {
		t.Fatal("diff accepted a changed catalog with an older revision")
	}
	afterReduced, err := openArchive(candidate, candidate+".lock.json")
	if err != nil {
		t.Fatal(err)
	}
	defer afterReduced.Close()
	if summary, err := compareSets(before, afterReduced, "fxa", before.Index().Sets[0], afterReduced.Index().Sets[0]); err != nil || !strings.Contains(summary, "fxa-2") {
		t.Fatalf("count change hid the missing card: %q, %v", summary, err)
	}
	if summary, err := compareSets(before, before, "fxa", before.Index().Sets[0], before.Index().Sets[0]); err != nil || summary != "" {
		t.Fatalf("unchanged set reported a difference: %q, %v", summary, err)
	}
	t.Run("corrupt snapshot with matching archive lock", func(t *testing.T) {
		payload, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range reader.File {
			if file.Name == catalogdata.SourceArchivePath {
				offset, err := file.DataOffset()
				if err != nil {
					t.Fatal(err)
				}
				payload[offset] ^= 0xff
			}
		}
		lockBytes, err := os.ReadFile(candidate + ".lock.json")
		if err != nil {
			t.Fatal(err)
		}
		lock, err := catalogdata.ParseLock(lockBytes)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		lock.ArchiveSHA256 = hex.EncodeToString(digest[:])
		lockBytes, err = catalogdata.MarshalLock(lock)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "corrupt.zip")
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".lock.json", lockBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runDiff([]string{"-old", previous, "-new", path}); err == nil {
			t.Fatal("diff accepted a corrupt source snapshot")
		}
	})

	// A candidate that loses a whole set is an error, not a note.
	empty := t.TempDir()
	layout := map[string]map[string]string{
		"en": {"series.json": testSeries, "sets.json": `[]`, "cards.json": `[]`, "stats.json": `{}`},
	}
	generated := writeGenerated(t, empty, layout)
	snapshot := filepath.Join(empty, "upstream.tar.gz")
	writeSnapshot(t, snapshot, layout, nil)
	assets := filepath.Join(empty, "assets-index.json")
	if err := os.WriteFile(assets, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyOut := filepath.Join(empty, "empty.zip")
	if err := runBuild(buildArgs(prepared{root: empty, generated: generated, snapshot: snapshot, assets: assets}, emptyOut, "-revision", "3")); err != nil {
		t.Fatalf("build empty: %v", err)
	}
	if err := runDiff([]string{"-old", previous, "-new", emptyOut}); err == nil {
		t.Fatal("a candidate missing a whole set was reported as acceptable")
	}
}

func TestFetchStagesAndVerifiesALocalArtifact(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	artifact := filepath.Join(input.root, "catalog.zip")
	if err := runBuild(buildArgs(input, artifact)); err != nil {
		t.Fatalf("build: %v", err)
	}
	lockPath := artifact + ".lock.json"
	staged := filepath.Join(input.root, "staged.zip")

	if err := runFetch([]string{"-lock", lockPath, "-out", staged, "-from", artifact}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	original, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	stagedBytes, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, stagedBytes) {
		t.Fatal("the staged artifact differs from the source")
	}

	// Reusing a verified file works even when the source is unavailable.
	if err := runFetch([]string{"-lock", lockPath, "-out", staged, "-from", filepath.Join(input.root, "absent")}); err != nil {
		t.Fatalf("fetch again: %v", err)
	}

	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := catalogdata.ParseLock(lockBytes)
	if err != nil {
		t.Fatal(err)
	}
	lock.Revision++
	lockBytes, err = catalogdata.MarshalLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, lockBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runFetch([]string{"-lock", lockPath, "-out", staged, "-from", artifact}); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("fetch with mismatched revision = %v", err)
	}

}

func TestFetchRefusesAnArtifactThatDoesNotMatchTheLock(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	artifact := filepath.Join(input.root, "catalog.zip")
	if err := runBuild(buildArgs(input, artifact)); err != nil {
		t.Fatalf("build: %v", err)
	}
	lockPath := artifact + ".lock.json"

	wrong := filepath.Join(input.root, "wrong.zip")
	payload, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), payload...)
	tampered[len(tampered)/2] ^= 0xff
	if err := os.WriteFile(wrong, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(input.root, "staged.zip")
	if err := runFetch([]string{"-lock", lockPath, "-out", staged, "-from", wrong}); err == nil {
		t.Fatal("an artifact that does not match the lock was staged")
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("a failed fetch left a staged artifact behind")
	}

	// A longer artifact must not be truncated into looking right.
	longer := filepath.Join(input.root, "longer.zip")
	if err := os.WriteFile(longer, append(append([]byte(nil), payload...), 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runFetch([]string{"-lock", lockPath, "-out", staged, "-from", longer}); err == nil {
		t.Fatal("an oversized artifact was staged")
	}
}

func TestFetchRefusesAnInsecureArtifactLocation(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, "catalog.lock.json")
	payload := []byte("not an archive")
	sum := sha256.Sum256(payload)
	lock := catalogdata.Lock{
		Kind:          catalogdata.KindProduction,
		Revision:      1,
		FormatVersion: catalogdata.FormatVersion,
		ArtifactURL:   "http://example.invalid/catalog.zip",
		ArchiveSHA256: hex.EncodeToString(sum[:]),
		ArchiveBytes:  int64(len(payload)),
	}
	encoded, err := catalogdata.MarshalLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	err = runFetch([]string{"-lock", lockPath, "-out", filepath.Join(root, "catalog.zip")})
	if err == nil {
		t.Fatal("a plain HTTP artifact location was accepted")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("error = %v", err)
	}

	t.Run("HTTPS redirect to HTTP", func(t *testing.T) {
		originalTransport := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = originalTransport })
		httpRequested := false
		http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			httpRequested = httpRequested || request.URL.Scheme == "http"
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": {"http://artifact.example/download"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		})
		body, err := download("https://artifact.example/catalog.zip")
		if body != nil {
			body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "scheme") {
			t.Fatalf("download error = %v, want HTTP redirect rejection", err)
		}
		if httpRequested {
			t.Fatal("HTTP redirect reached the transport")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestBuildKeepsPreviousOutputsOnFailure(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	out := filepath.Join(input.root, "catalog.zip")
	if err := runBuild(buildArgs(input, out)); err != nil {
		t.Fatal(err)
	}

	for _, flags := range [][]string{
		{"-report", input.root},
		{"-lock", filepath.Join(input.root, "missing", "lock")},
		{"-report", out + ".lock.json"},
		{"-lock", out},
	} {
		before := map[string][]byte{}
		for _, path := range []string{out, out + ".lock.json", out + ".report.txt"} {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			before[path] = data
		}
		args := buildArgs(input, out, "-builder-version", "changed")
		if err := runBuild(append(args, flags...)); err == nil {
			t.Fatalf("build accepted conflicting or unwritable outputs %v", flags)
		}
		for path, want := range before {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("build with %v damaged %s: %v", flags, path, err)
			}
		}
	}
	if err := runBuild(buildArgs(input, out, "-builder-version", "changed")); err != nil {
		t.Fatalf("retry after failed build: %v", err)
	}
}

func TestInstallBuildOutputsRollsBackFailedRename(t *testing.T) {
	dir := t.TempDir()
	outputs := []*buildOutput{
		{path: filepath.Join(dir, "existing")},
		{path: filepath.Join(dir, "new")},
		{path: filepath.Join(dir, "last")},
	}
	if err := os.WriteFile(outputs[0].path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, output := range outputs {
		if err := output.stage(); err != nil {
			t.Fatal(err)
		}
		defer output.clean()
		// Leave the last staged file missing so the third rename fails.
		if i < len(outputs)-1 {
			if err := output.write([]byte("updated")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := installBuildOutputs(outputs); err == nil {
		t.Fatal("installed an incomplete build")
	}
	got, err := os.ReadFile(outputs[0].path)
	if err != nil || string(got) != "old" {
		t.Fatalf("previous file was not restored: %v", err)
	}
	for _, output := range outputs[1:] {
		if _, err := os.Stat(output.path); !os.IsNotExist(err) {
			t.Fatalf("failed build left %s: %v", output.path, err)
		}
	}
}

func TestBuildWritesThroughSidecarSymlink(t *testing.T) {
	input := prepare(t, `[{"id":"fxa-1"},{"id":"fxa-2"}]`)
	out := filepath.Join(input.root, "catalog.zip")
	link := filepath.Join(input.root, "candidate.lock.json")
	target := "actual.lock.json"
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := runBuild(buildArgs(input, out, "-lock", link)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(link); err != nil || got != target {
		t.Fatalf("lock symlink = %q, %v", got, err)
	}
	if err := runInspect([]string{"-archive", out, "-lock", filepath.Join(input.root, target)}); err != nil {
		t.Fatal(err)
	}
}
