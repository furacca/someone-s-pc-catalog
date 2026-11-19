package catalogdata

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

// zipEpoch is the timestamp of every entry, so identical inputs produce
// identical archives.
var zipEpoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

const zipEntryMode = 0o644

// BuildInput is everything needed to produce one archive. The index, the
// application counts and the entry inventory are derived from it.
type BuildInput struct {
	Kind            string
	Revision        int64
	SourceAPI       string
	SourceTag       string
	SourceCommit    string
	BuilderVersion  string
	CompilerImageID string
	AppLanguages    []string

	// SourceArchive is the upstream source snapshot and AssetsIndex the
	// captured CDN index. Both are stored unchanged.
	SourceArchive   []byte
	AssetsIndex     []byte
	SourceDirs      []SourceDirRow
	SourceLanguages []string
	SourceSeries    []string

	Sets     []SetRecord
	Findings []Finding
}

// BuildResult reports the produced archive's identity. The caller sets the
// lock's artifact URL.
type BuildResult struct {
	Manifest Manifest
	Lock     Lock
}

// Build writes one archive and returns its manifest and a matching lock.
//
// Records, languages and entries are sorted here, so the same inputs produce
// the same bytes regardless of the order the caller collected them in.
func Build(w io.Writer, in BuildInput) (BuildResult, error) {
	if len(in.SourceArchive) == 0 {
		return BuildResult{}, fmt.Errorf("build: the source snapshot is empty")
	}
	if len(in.AssetsIndex) == 0 {
		return BuildResult{}, fmt.Errorf("build: the captured assets index is empty")
	}

	catalogEntries, counts, err := buildCatalogEntries(in.Sets)
	if err != nil {
		return BuildResult{}, err
	}

	findings := append([]Finding(nil), in.Findings...)
	sort.SliceStable(findings, func(a, b int) bool {
		if findings[a].SetID != findings[b].SetID {
			return findings[a].SetID < findings[b].SetID
		}
		if findings[a].Code != findings[b].Code {
			return findings[a].Code < findings[b].Code
		}
		return findings[a].Language < findings[b].Language
	})

	sourceDirs := append([]SourceDirRow(nil), in.SourceDirs...)
	sort.Slice(sourceDirs, func(a, b int) bool { return sourceDirs[a].Path < sourceDirs[b].Path })

	manifest := Manifest{
		Kind:            in.Kind,
		Revision:        in.Revision,
		FormatVersion:   FormatVersion,
		SourceAPI:       in.SourceAPI,
		SourceTag:       in.SourceTag,
		SourceCommit:    in.SourceCommit,
		BuilderVersion:  in.BuilderVersion,
		CompilerImageID: in.CompilerImageID,
		AppLanguages:    SortLanguages(in.AppLanguages),
		SourceInventory: SourceInventory{
			ArchiveSHA256:     sha256Hex(in.SourceArchive),
			ArchiveBytes:      int64(len(in.SourceArchive)),
			AssetsIndexSHA256: sha256Hex(in.AssetsIndex),
			AssetsIndexBytes:  int64(len(in.AssetsIndex)),
			Directories:       sourceDirs,
			Languages:         sortedUnique(in.SourceLanguages),
			Series:            sortedUnique(in.SourceSeries),
		},
		AppCounts: counts,
		Findings:  findings,
	}

	// Write order is fixed and the inventory follows it, so an archive is
	// reproducible byte for byte.
	ordered := []archiveEntry{
		{AssetsIndexPath, in.AssetsIndex, false},
		{SourceArchivePath, in.SourceArchive, true},
	}
	ordered = append(ordered, catalogEntries...)
	return writeArchive(w, manifest, ordered)
}

type archiveEntry struct {
	path    string
	payload []byte
	store   bool
}

func buildCatalogEntries(records []SetRecord) ([]archiveEntry, AppCounts, error) {
	sets := append([]SetRecord(nil), records...)
	for setIndex := range sets {
		sets[setIndex] = normalizeSetRecord(sets[setIndex])
	}
	sort.Slice(sets, func(a, b int) bool { return sets[a].Set.ID < sets[b].Set.ID })

	index := Index{Sets: make([]IndexEntry, 0, len(sets))}
	counts := AppCounts{}
	coverage := map[string]*LanguageCounts{}
	entries := make([]archiveEntry, 1, 1+len(sets)) // The index precedes the set entries.

	for _, record := range sets {
		entry := indexEntryFor(record)
		if err := ValidateSetRecord(record, entry); err != nil {
			return nil, AppCounts{}, fmt.Errorf("build: %w", err)
		}
		encoded, err := MarshalCanonical(record)
		if err != nil {
			return nil, AppCounts{}, fmt.Errorf("build: encode set %q: %w", entry.SetID, err)
		}
		entries = append(entries, archiveEntry{entry.Path, encoded, false})
		index.Sets = append(index.Sets, entry)

		counts.Sets++
		counts.Cards += len(record.Cards)
		counts.SetLocalizations += len(entry.Languages)
		for _, language := range entry.Languages {
			if coverage[language] == nil {
				coverage[language] = &LanguageCounts{Language: language}
			}
			coverage[language].Sets++
		}
		for _, card := range record.Cards {
			counts.Printings += len(card.Card.VariantsDetailed)
			counts.CardLocalizations += 1 + len(card.Localizations)
			coverage["en"].Cards++
			for _, localization := range card.Localizations {
				coverage[localization.Language].Cards++
			}
		}
	}
	if err := ValidateIndex(index); err != nil {
		return nil, AppCounts{}, fmt.Errorf("build: %w", err)
	}
	for _, language := range SortLanguages(slices.Collect(maps.Keys(coverage))) {
		counts.Coverage = append(counts.Coverage, *coverage[language])
	}

	indexBytes, err := MarshalCanonical(index)
	if err != nil {
		return nil, AppCounts{}, fmt.Errorf("build: encode index: %w", err)
	}

	entries[0] = archiveEntry{IndexPath, indexBytes, false}
	return entries, counts, nil
}

func writeArchive(w io.Writer, manifest Manifest, ordered []archiveEntry) (BuildResult, error) {
	var expanded int64
	for _, entry := range ordered {
		if int64(len(entry.payload)) > MaxEntryBytes {
			return BuildResult{}, fmt.Errorf("build: entry %q is %d bytes, over the %d byte entry limit", entry.path, len(entry.payload), MaxEntryBytes)
		}
		expanded += int64(len(entry.payload))
		manifest.Entries = append(manifest.Entries, EntryInfo{
			Path:   entry.path,
			Bytes:  int64(len(entry.payload)),
			SHA256: sha256Hex(entry.payload),
		})
	}
	if err := ValidateManifest(manifest); err != nil {
		return BuildResult{}, fmt.Errorf("build: %w", err)
	}
	manifestBytes, err := MarshalCanonical(manifest)
	if err != nil {
		return BuildResult{}, fmt.Errorf("build: encode manifest: %w", err)
	}
	if int64(len(manifestBytes)) > MaxManifestBytes {
		return BuildResult{}, fmt.Errorf("build: manifest is %d bytes, over the %d byte limit", len(manifestBytes), MaxManifestBytes)
	}

	expanded += int64(len(manifestBytes))
	if expanded > MaxExpandedBytes {
		return BuildResult{}, fmt.Errorf("build: entries expand to %d bytes, over the %d byte limit", expanded, MaxExpandedBytes)
	}

	digest := sha256.New()
	counting := &countingWriter{w: io.MultiWriter(w, digest)}
	zipWriter := zip.NewWriter(counting)
	if err := writeZipEntry(zipWriter, ManifestPath, manifestBytes, false); err != nil {
		return BuildResult{}, err
	}
	for _, entry := range ordered {
		if err := writeZipEntry(zipWriter, entry.path, entry.payload, entry.store); err != nil {
			return BuildResult{}, err
		}
	}
	if err := zipWriter.Close(); err != nil {
		return BuildResult{}, fmt.Errorf("build: close archive: %w", err)
	}
	if counting.n > MaxArchiveBytes {
		return BuildResult{}, fmt.Errorf("build: archive is %d bytes, over the %d byte limit", counting.n, MaxArchiveBytes)
	}

	return BuildResult{
		Manifest: manifest,
		Lock: Lock{
			Kind:          manifest.Kind,
			Revision:      manifest.Revision,
			FormatVersion: manifest.FormatVersion,
			ArchiveSHA256: hex.EncodeToString(digest.Sum(nil)),
			ArchiveBytes:  counting.n,
		},
	}, nil
}

func writeZipEntry(w *zip.Writer, path string, payload []byte, store bool) error {
	header := &zip.FileHeader{Name: path, Modified: zipEpoch}
	header.SetMode(zipEntryMode)
	if store {
		// The upstream snapshot is already compressed; deflating it again only
		// costs build time.
		header.Method = zip.Store
	} else {
		header.Method = zip.Deflate
	}
	entry, err := w.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("build: create entry %q: %w", path, err)
	}
	if _, err := entry.Write(payload); err != nil {
		return fmt.Errorf("build: write entry %q: %w", path, err)
	}
	return nil
}

func indexEntryFor(record SetRecord) IndexEntry {
	languages := []string{"en"}
	for _, localization := range record.Localizations {
		languages = append(languages, localization.Language)
	}
	return IndexEntry{
		SetID:             record.Set.ID,
		Path:              SetEntryPath(record.Set.ID),
		SeriesID:          record.Set.SeriesID,
		Name:              record.Set.Name,
		ReleaseDate:       record.Set.ReleaseDate,
		CardCountOfficial: record.Set.CardCountOfficial,
		CardCountTotal:    record.Set.CardCountTotal,
		ListedCardCount:   len(record.Cards),
		Languages:         SortLanguages(languages),
		CardsComplete:     record.CardsComplete,
	}
}

func normalizeSetRecord(record SetRecord) SetRecord {
	localizations := append([]SetLocalization(nil), record.Localizations...)
	for index := range localizations {
		localizations[index].Language = strings.ToLower(strings.TrimSpace(localizations[index].Language))
	}
	sort.Slice(localizations, func(a, b int) bool { return localizations[a].Language < localizations[b].Language })
	record.Localizations = localizations

	cards := append([]CardRecord(nil), record.Cards...)
	for cardIndex := range cards {
		cardLocalizations := append([]CardLocalization(nil), cards[cardIndex].Localizations...)
		for index := range cardLocalizations {
			cardLocalizations[index].Language = strings.ToLower(strings.TrimSpace(cardLocalizations[index].Language))
		}
		sort.Slice(cardLocalizations, func(a, b int) bool {
			return cardLocalizations[a].Language < cardLocalizations[b].Language
		})
		cards[cardIndex].Localizations = cardLocalizations
	}
	sort.Slice(cards, func(a, b int) bool { return cards[a].Card.ID < cards[b].Card.ID })
	record.Cards = cards
	return record
}

// Archive is a validated, open catalog archive. Set entries are read one at a
// time; the source snapshot and the whole catalog are never loaded into
// memory.
type Archive struct {
	closer    io.Closer
	manifest  Manifest
	index     Index
	entries   map[string]*zip.File
	inventory map[string]EntryInfo
	indexBy   map[string]IndexEntry
	sha256    string
	bytes     int64
}

// OpenFile opens an archive from disk and validates it against its lock.
func OpenFile(path string, lock Lock) (*Archive, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	archive, err := Open(file, info.Size(), lock)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	archive.closer = file
	return archive, nil
}

// OpenBytes opens an archive held in memory.
func OpenBytes(data []byte, lock Lock) (*Archive, error) {
	return Open(bytes.NewReader(data), int64(len(data)), lock)
}

// Open validates an archive and returns a reader for it.
//
// The lock checksum covers every byte of the archive, so it is verified first
// and before anything is read from the ZIP structure. Per-entry inventory
// hashes are checked as each entry is read, and VerifyEntries checks them all
// including the preserved source bytes.
func Open(r io.ReaderAt, size int64, lock Lock) (*Archive, error) {
	if err := ValidateLock(lock); err != nil {
		return nil, err
	}
	if size <= 0 {
		return nil, fmt.Errorf("archive is empty")
	}
	if size > MaxArchiveBytes {
		return nil, fmt.Errorf("archive is %d bytes, over the %d byte limit", size, MaxArchiveBytes)
	}
	if size != lock.ArchiveBytes {
		return nil, fmt.Errorf("archive is %d bytes, the lock expects %d", size, lock.ArchiveBytes)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.NewSectionReader(r, 0, size)); err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	sum := hex.EncodeToString(digest.Sum(nil))
	if sum != lock.ArchiveSHA256 {
		return nil, fmt.Errorf("archive checksum %s does not match the locked %s", sum, lock.ArchiveSHA256)
	}

	zipReader, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	archive := &Archive{
		entries: make(map[string]*zip.File, len(zipReader.File)),
		sha256:  sum,
		bytes:   size,
	}
	var expanded int64
	for _, file := range zipReader.File {
		if err := checkEntryPath(file.Name); err != nil {
			return nil, err
		}
		if _, exists := archive.entries[file.Name]; exists {
			return nil, fmt.Errorf("archive contains entry %q twice", file.Name)
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || mode.IsDir() || !mode.IsRegular() {
			return nil, fmt.Errorf("archive entry %q is not a regular file", file.Name)
		}
		if file.UncompressedSize64 > uint64(MaxEntryBytes) {
			return nil, fmt.Errorf("archive entry %q declares %d bytes, over the %d byte limit", file.Name, file.UncompressedSize64, MaxEntryBytes)
		}
		expanded += int64(file.UncompressedSize64)
		if expanded > MaxExpandedBytes {
			return nil, fmt.Errorf("archive entries expand to over the %d byte limit", MaxExpandedBytes)
		}
		archive.entries[file.Name] = file
	}

	if err := archive.readManifest(lock); err != nil {
		return nil, err
	}
	if err := archive.readIndex(); err != nil {
		return nil, err
	}
	return archive, nil
}

func (a *Archive) readManifest(lock Lock) error {
	manifestBytes, err := a.readRaw(ManifestPath, MaxManifestBytes)
	if err != nil {
		return err
	}
	if err := decodeStrict(manifestBytes, &a.manifest); err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	if err := ValidateManifest(a.manifest); err != nil {
		return err
	}
	if a.manifest.Kind != lock.Kind {
		return fmt.Errorf("archive kind %q does not match the locked %q", a.manifest.Kind, lock.Kind)
	}
	if a.manifest.Revision != lock.Revision {
		return fmt.Errorf("archive revision %d does not match the locked %d", a.manifest.Revision, lock.Revision)
	}

	a.inventory = make(map[string]EntryInfo, len(a.manifest.Entries))
	for _, entry := range a.manifest.Entries {
		a.inventory[entry.Path] = entry
		file, exists := a.entries[entry.Path]
		if !exists {
			return fmt.Errorf("archive is missing inventoried entry %q", entry.Path)
		}
		if int64(file.UncompressedSize64) != entry.Bytes {
			return fmt.Errorf("archive entry %q is %d bytes, the inventory reports %d", entry.Path, file.UncompressedSize64, entry.Bytes)
		}
	}
	for name := range a.entries {
		if name == ManifestPath {
			continue
		}
		if _, exists := a.inventory[name]; !exists {
			return fmt.Errorf("archive entry %q is not in the manifest inventory", name)
		}
	}
	if a.manifest.SourceInventory.ArchiveSHA256 != a.inventory[SourceArchivePath].SHA256 {
		return fmt.Errorf("archive source snapshot hash disagrees with the entry inventory")
	}
	if a.manifest.SourceInventory.AssetsIndexSHA256 != a.inventory[AssetsIndexPath].SHA256 {
		return fmt.Errorf("archive assets index hash disagrees with the entry inventory")
	}
	return nil
}

func (a *Archive) readIndex() error {
	indexBytes, err := a.readEntry(IndexPath)
	if err != nil {
		return err
	}
	if err := decodeStrict(indexBytes, &a.index); err != nil {
		return fmt.Errorf("read catalog index: %w", err)
	}
	if err := ValidateIndex(a.index); err != nil {
		return err
	}
	a.indexBy = make(map[string]IndexEntry, len(a.index.Sets))
	for _, entry := range a.index.Sets {
		if _, exists := a.entries[entry.Path]; !exists {
			return fmt.Errorf("archive is missing set entry %q", entry.Path)
		}
		a.indexBy[entry.SetID] = entry
	}
	for name := range a.entries {
		if !strings.HasPrefix(name, SetsPrefix) {
			continue
		}
		setID := strings.TrimSuffix(strings.TrimPrefix(name, SetsPrefix), ".json")
		if _, exists := a.indexBy[setID]; !exists {
			return fmt.Errorf("archive set entry %q is not in the catalog index", name)
		}
	}
	if a.manifest.AppCounts.Sets != len(a.index.Sets) {
		return fmt.Errorf("manifest counts %d sets, the index lists %d", a.manifest.AppCounts.Sets, len(a.index.Sets))
	}
	return nil
}

// Manifest returns the validated archive identity and provenance.
func (a *Archive) Manifest() Manifest { return a.manifest }

// Index returns the validated catalog index.
func (a *Archive) Index() Index { return a.index }

// SHA256 returns the checksum of the whole archive.
func (a *Archive) SHA256() string { return a.sha256 }

// Bytes returns the archive size.
func (a *Archive) Bytes() int64 { return a.bytes }

// Close releases the underlying file, if the archive owns one.
func (a *Archive) Close() error {
	if a.closer == nil {
		return nil
	}
	return a.closer.Close()
}

// Set reads and validates one compiled set. The entry is released before the
// call returns, so iterating the catalog costs one set at a time.
func (a *Archive) Set(setID string) (SetRecord, error) {
	entry, exists := a.indexBy[setID]
	if !exists {
		return SetRecord{}, fmt.Errorf("set %q is not in the catalog index", setID)
	}
	payload, err := a.readEntry(entry.Path)
	if err != nil {
		return SetRecord{}, err
	}
	var record SetRecord
	if err := decodeStrict(payload, &record); err != nil {
		return SetRecord{}, fmt.Errorf("read set %q: %w", setID, err)
	}
	if err := ValidateSetRecord(record, entry); err != nil {
		return SetRecord{}, err
	}
	return record, nil
}

// EachSet visits every set in index order. It stops at the first error.
func (a *Archive) EachSet(visit func(IndexEntry, SetRecord) error) error {
	for _, entry := range a.index.Sets {
		record, err := a.Set(entry.SetID)
		if err != nil {
			return err
		}
		if err := visit(entry, record); err != nil {
			return err
		}
	}
	return nil
}

// OpenSource returns the preserved upstream snapshot as a byte stream.
func (a *Archive) OpenSource() (io.ReadCloser, error) {
	file, exists := a.entries[SourceArchivePath]
	if !exists {
		return nil, fmt.Errorf("archive is missing %q", SourceArchivePath)
	}
	return file.Open()
}

// VerifyEntries hashes every inventoried entry, including the preserved
// source. Open skips this pass because the lock checksum already covers every
// byte; building and inspecting an archive run it.
func (a *Archive) VerifyEntries() error {
	for _, entry := range a.manifest.Entries {
		file, exists := a.entries[entry.Path]
		if !exists {
			return fmt.Errorf("archive is missing inventoried entry %q", entry.Path)
		}
		reader, err := file.Open()
		if err != nil {
			return fmt.Errorf("read archive entry %q: %w", entry.Path, err)
		}
		digest := sha256.New()
		written, err := copyBounded(digest, reader, entry.Bytes)
		closeErr := reader.Close()
		if err != nil {
			return fmt.Errorf("read archive entry %q: %w", entry.Path, err)
		}
		if closeErr != nil {
			return fmt.Errorf("read archive entry %q: %w", entry.Path, closeErr)
		}
		if written != entry.Bytes {
			return fmt.Errorf("archive entry %q produced %d bytes, the inventory reports %d", entry.Path, written, entry.Bytes)
		}
		if sum := hex.EncodeToString(digest.Sum(nil)); sum != entry.SHA256 {
			return fmt.Errorf("archive entry %q hashes to %s, the inventory reports %s", entry.Path, sum, entry.SHA256)
		}
	}
	return nil
}

// readEntry reads an inventoried entry and verifies its recorded hash.
func (a *Archive) readEntry(path string) ([]byte, error) {
	info, exists := a.inventory[path]
	if !exists {
		return nil, fmt.Errorf("archive entry %q is not in the manifest inventory", path)
	}
	payload, err := a.readRaw(path, info.Bytes)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != info.Bytes {
		return nil, fmt.Errorf("archive entry %q is %d bytes, the inventory reports %d", path, len(payload), info.Bytes)
	}
	if sum := sha256Hex(payload); sum != info.SHA256 {
		return nil, fmt.Errorf("archive entry %q hashes to %s, the inventory reports %s", path, sum, info.SHA256)
	}
	return payload, nil
}

// readRaw reads an entry with an enforced ceiling. The limit is applied to the
// decompressed stream, not to the ZIP header, so a header that understates its
// entry cannot make the reader expand an unbounded amount of data.
func (a *Archive) readRaw(path string, limit int64) ([]byte, error) {
	file, exists := a.entries[path]
	if !exists {
		return nil, fmt.Errorf("archive is missing %q", path)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("read archive entry %q: %w", path, err)
	}
	defer reader.Close()
	var buffer bytes.Buffer
	if _, err := copyBounded(&buffer, reader, limit); err != nil {
		return nil, fmt.Errorf("read archive entry %q: %w", path, err)
	}
	return buffer.Bytes(), nil
}

var errEntryTooLarge = errors.New("entry exceeds its declared size")

// copyBounded copies at most limit bytes and fails if the source has more.
func copyBounded(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, limit))
	if err != nil {
		return written, err
	}
	var probe [1]byte
	switch n, err := src.Read(probe[:]); {
	case n > 0:
		return written, errEntryTooLarge
	case err == nil || errors.Is(err, io.EOF):
		return written, nil
	default:
		return written, err
	}
}

// decodeStrict rejects unknown fields: an archive written by a newer builder
// must raise the format version instead of being read with fields ignored.
func decodeStrict(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

// ParseLock decodes a lock file and validates it.
func ParseLock(payload []byte) (Lock, error) {
	var lock Lock
	if err := decodeStrict(payload, &lock); err != nil {
		return Lock{}, fmt.Errorf("read catalog lock: %w", err)
	}
	if err := ValidateLock(lock); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

// MarshalLock encodes a lock file for the repository, indented so its diffs
// stay readable.
func MarshalLock(lock Lock) ([]byte, error) {
	if err := ValidateLock(lock); err != nil {
		return nil, err
	}
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(lock); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	written, err := c.w.Write(p)
	c.n += int64(written)
	return written, err
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
