// Package catalogdata defines the catalog archive format: its types, its
// manifest and the reader that validates an archive.
//
// The same package is kept in the catalog repository, which builds archives,
// and in SOMEONE'S PC, which reads them. It has no HTTP or database
// dependency.
package catalogdata

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// FormatVersion is the archive format version. A reader rejects any other
// value.
const FormatVersion = 1

// Archive kinds. A production archive carries a real catalog; a fixture
// carries the small synthetic catalog used by development builds and tests.
const (
	KindProduction = "production"
	KindFixture    = "fixture"
)

// Entry paths inside the archive.
const (
	ManifestPath      = "manifest.json"
	SourceArchivePath = "source/upstream.tar.gz"
	AssetsIndexPath   = "source/assets-index.json"
	IndexPath         = "catalog/index.json"
	SetsPrefix        = "catalog/sets/"
)

// Archive size bounds, enforced when building and when reading.
const (
	MaxArchiveBytes  int64 = 256 << 20
	MaxExpandedBytes int64 = 1 << 30
	MaxEntryBytes    int64 = 64 << 20
	MaxManifestBytes int64 = 1 << 20
)

// AppLanguages is the application scope. Other languages stay in the preserved
// source snapshot without being projected into the compiled catalog.
var AppLanguages = []string{"en", "it", "fr", "de", "es", "pt"}

// PocketSeriesID identifies the Pokémon TCG Pocket series. It is filtered out
// of the projection only; the source snapshot keeps it.
const PocketSeriesID = "tcgp"

// AssetHost is the only host an image reference may name.
const AssetHost = "assets.tcgdex.net"

// Lock identifies one archive by revision, size and checksum. It is kept in
// version control and holds no credentials.
type Lock struct {
	Kind          string `json:"kind"`
	Revision      int64  `json:"revision"`
	FormatVersion int    `json:"format_version"`
	ArtifactURL   string `json:"artifact_url"`
	ArchiveSHA256 string `json:"archive_sha256"`
	ArchiveBytes  int64  `json:"archive_bytes"`
}

// Manifest is the archive's identity, provenance and inventory. Its entry
// inventory covers every ZIP entry except the manifest itself; the external
// lock hashes the complete archive, which avoids a checksum cycle.
type Manifest struct {
	Kind            string          `json:"kind"`
	Revision        int64           `json:"revision"`
	FormatVersion   int             `json:"format_version"`
	SourceAPI       string          `json:"source_api"`
	SourceTag       string          `json:"source_tag"`
	SourceCommit    string          `json:"source_commit"`
	BuilderVersion  string          `json:"builder_version"`
	CompilerImageID string          `json:"compiler_image_id"`
	AppLanguages    []string        `json:"app_languages"`
	SourceInventory SourceInventory `json:"source_inventory"`
	AppCounts       AppCounts       `json:"app_counts"`
	Findings        []Finding       `json:"findings"`
	Entries         []EntryInfo     `json:"entries"`
}

// SourceInventory summarizes the preserved upstream snapshot. It counts files
// and bytes per top-level source directory instead of listing every source
// file: the source archive hash already covers their bytes.
type SourceInventory struct {
	ArchiveSHA256     string         `json:"archive_sha256"`
	ArchiveBytes      int64          `json:"archive_bytes"`
	AssetsIndexSHA256 string         `json:"assets_index_sha256"`
	AssetsIndexBytes  int64          `json:"assets_index_bytes"`
	Directories       []SourceDirRow `json:"directories"`
	Languages         []string       `json:"languages"`
	Series            []string       `json:"series"`
}

// SourceDirRow is one top-level directory of the preserved source checkout.
type SourceDirRow struct {
	Path  string `json:"path"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// AppCounts describes the compiled application catalog, which is a filtered
// projection of the source and is counted separately from it.
type AppCounts struct {
	Sets              int              `json:"sets"`
	Cards             int              `json:"cards"`
	Printings         int              `json:"printings"`
	SetLocalizations  int              `json:"set_localizations"`
	CardLocalizations int              `json:"card_localizations"`
	Coverage          []LanguageCounts `json:"coverage"`
}

// LanguageCounts reports localization coverage for one application language.
type LanguageCounts struct {
	Language string `json:"language"`
	Sets     int    `json:"sets"`
	Cards    int    `json:"cards"`
}

// Finding records upstream omissions and incomplete card or localization coverage.
type Finding struct {
	Code     string `json:"code"`
	SetID    string `json:"set_id,omitempty"`
	Language string `json:"language,omitempty"`
	Message  string `json:"message"`
}

// Finding codes. Findings describe upstream data that is missing or left out;
// build failures are errors instead.
const (
	FindingSetIncomplete       = "set_incomplete"
	FindingLocalizationMissing = "localization_missing"
	FindingSourceUnprojected   = "source_unprojected"
	FindingSourceDuplicate     = "source_duplicate"
)

// EntryInfo is one row of the manifest entry inventory.
type EntryInfo struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Index is catalog/index.json: every English physical-card set in the
// application scope, sorted by provider set ID.
type Index struct {
	Sets []IndexEntry `json:"sets"`
}

// IndexEntry is the index row for one set. Path is its own archive entry, so a
// reader opens one set at a time instead of loading the whole catalog.
type IndexEntry struct {
	SetID             string   `json:"set_id"`
	Path              string   `json:"path"`
	SeriesID          string   `json:"series_id"`
	Name              string   `json:"name"`
	ReleaseDate       string   `json:"release_date"`
	CardCountOfficial int      `json:"card_count_official"`
	CardCountTotal    int      `json:"card_count_total"`
	ListedCardCount   int      `json:"listed_card_count"`
	Languages         []string `json:"languages"`
	CardsComplete     bool     `json:"cards_complete"`
}

// SetRecord contains English data and non-English localizations, plus cards and source
// completeness.
type SetRecord struct {
	Set              Set               `json:"set"`
	Localizations    []SetLocalization `json:"localizations"`
	Cards            []CardRecord      `json:"cards"`
	CardsComplete    bool              `json:"cards_complete"`
	IncompleteReason string            `json:"incomplete_reason,omitempty"`
}

// SetLocalization is one non-English set translation.
type SetLocalization struct {
	Language string `json:"language"`
	Set      Set    `json:"set"`
}

// CardRecord is one compiled card with its non-English translations.
type CardRecord struct {
	Card          Card               `json:"card"`
	Localizations []CardLocalization `json:"localizations"`
}

// CardLocalization is one non-English card translation.
type CardLocalization struct {
	Language string `json:"language"`
	Card     Card   `json:"card"`
}

// Set mirrors the provider set fields the application maps. Logo and Symbol
// are remote references; local asset paths are never stored in the archive.
type Set struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Logo              string `json:"logo"`
	Symbol            string `json:"symbol"`
	ReleaseDate       string `json:"release_date"`
	SeriesID          string `json:"series_id"`
	SeriesName        string `json:"series_name"`
	CardCountOfficial int    `json:"card_count_official"`
	CardCountTotal    int    `json:"card_count_total"`
}

// Card mirrors the provider card fields the application maps. Image is the
// remote scan base without an extension, as TCGdex publishes it.
type Card struct {
	ID             string `json:"id"`
	LocalID        string `json:"local_id"`
	Name           string `json:"name"`
	Image          string `json:"image"`
	Category       string `json:"category"`
	Rarity         string `json:"rarity"`
	Illustrator    string `json:"illustrator"`
	RegulationMark string `json:"regulation_mark"`
	Description    string `json:"description"`
	Effect         string `json:"effect"`

	HP          *int        `json:"hp"`
	Types       []string    `json:"types"`
	EvolveFrom  string      `json:"evolve_from"`
	Weight      string      `json:"weight"`
	Stage       string      `json:"stage"`
	Suffix      string      `json:"suffix"`
	DexIDs      []int       `json:"dex_ids"`
	Level       string      `json:"level"`
	Item        *TextBlock  `json:"item"`
	Abilities   []TextBlock `json:"abilities"`
	Attacks     []TextBlock `json:"attacks"`
	Weaknesses  []TypeValue `json:"weaknesses"`
	Resistances []TypeValue `json:"resistances"`
	Retreat     *int        `json:"retreat"`
	TrainerType string      `json:"trainer_type"`
	EnergyType  string      `json:"energy_type"`

	SetID   string `json:"set_id"`
	SetName string `json:"set_name"`

	// VariantsDetailedPresent records whether the upstream field existed.
	// Absent means unknown, so existing printings are kept; an empty array
	// means the card has none. The flag is stored explicitly so the
	// distinction survives a round trip.
	VariantsDetailed        []Variant `json:"variants_detailed"`
	VariantsDetailedPresent bool      `json:"variants_detailed_present"`

	// VariantsFirstEdition is the raw provider variants.firstEdition flag.
	// A 1st-edition stamp also implies First Edition; the application
	// applies that rule, the archive keeps the source value.
	VariantsFirstEdition bool `json:"variants_first_edition"`
}

// CollectorNumber is the card's number within its set: local_id, or the
// provider ID suffix when local_id is absent.
func (c Card) CollectorNumber() string {
	if number := strings.TrimSpace(c.LocalID); number != "" {
		return number
	}
	_, number, _ := strings.Cut(c.ID, "-")
	return strings.TrimSpace(number)
}

// UnmarshalJSON requires the archive's explicit unknown-versus-empty distinction.
func (c *Card) UnmarshalJSON(payload []byte) error {
	type cardData Card
	var decoded struct {
		cardData
		Present *bool `json:"variants_detailed_present"`
	}
	if err := decodeStrict(payload, &decoded); err != nil {
		return err
	}
	if decoded.Present == nil {
		return fmt.Errorf("card %q has no variants_detailed_present flag", decoded.ID)
	}
	decoded.cardData.VariantsDetailedPresent = *decoded.Present
	*c = Card(decoded.cardData)
	return nil
}

// TextBlock mirrors a provider ability, attack or trainer item. Damage is a
// number or a string upstream, so it keeps the decoded JSON value.
type TextBlock struct {
	Type   string   `json:"type,omitempty"`
	Name   string   `json:"name,omitempty"`
	Effect string   `json:"effect,omitempty"`
	Damage any      `json:"damage,omitempty"`
	Cost   []string `json:"cost,omitempty"`
}

// TypeValue mirrors a provider weakness or resistance.
type TypeValue struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

// Variant mirrors one entry of the provider variants_detailed array, which is
// the source of printing identity.
type Variant struct {
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	Size      string   `json:"size"`
	Stamp     []string `json:"stamp"`
	Foil      string   `json:"foil"`
	VariantID string   `json:"variant_id"`
}

// HasFirstEditionStamp identifies variants whose edition belongs to the copy;
// their provider IDs do not identify a separate printing.
func (v Variant) HasFirstEditionStamp() bool {
	for _, stamp := range v.Stamp {
		if strings.EqualFold(strings.TrimSpace(stamp), "1st-edition") {
			return true
		}
	}
	return false
}

// safeSetID restricts a provider set ID to characters that are safe both as an
// archive entry name and as a lookup key. Upstream IDs such as "sv03.5" pass.
var safeSetID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SetEntryPath returns the archive entry holding one set record.
func SetEntryPath(setID string) string {
	return SetsPrefix + setID + ".json"
}

// IsAppLanguage reports whether a language is in the application scope.
func IsAppLanguage(language string) bool {
	language = strings.ToLower(strings.TrimSpace(language))
	for _, candidate := range AppLanguages {
		if candidate == language {
			return true
		}
	}
	return false
}

// IsPocketSeries reports whether a series is the digital-only Pocket catalog.
func IsPocketSeries(seriesID string) bool {
	return strings.EqualFold(strings.TrimSpace(seriesID), PocketSeriesID)
}

// ValidateLock checks a lock file's internal consistency. It does not read the
// artifact it identifies.
func ValidateLock(lock Lock) error {
	switch lock.Kind {
	case KindProduction, KindFixture:
	default:
		return fmt.Errorf("lock kind %q is not supported", lock.Kind)
	}
	if lock.FormatVersion != FormatVersion {
		return fmt.Errorf("lock format version %d is not supported", lock.FormatVersion)
	}
	if err := validateRevision(lock.Kind, lock.Revision); err != nil {
		return fmt.Errorf("lock %w", err)
	}
	if !hex64.MatchString(lock.ArchiveSHA256) {
		return fmt.Errorf("lock archive_sha256 must be 64 lowercase hex characters")
	}
	if lock.ArchiveBytes <= 0 {
		return fmt.Errorf("lock archive_bytes must be positive")
	}
	if lock.ArchiveBytes > MaxArchiveBytes {
		return fmt.Errorf("lock archive_bytes %d exceeds the %d byte limit", lock.ArchiveBytes, MaxArchiveBytes)
	}
	if strings.Contains(lock.ArtifactURL, "@") {
		return fmt.Errorf("lock artifact_url must not embed credentials")
	}
	return nil
}

func validateRevision(kind string, revision int64) error {
	if revision < 0 {
		return fmt.Errorf("revision must not be negative")
	}
	if kind == KindProduction && revision < 1 {
		return fmt.Errorf("revision must be at least 1 for a production catalog")
	}
	// A fixture may carry any revision, so upgrades and downgrades can be
	// tested.
	return nil
}

// ValidateManifest checks identity, provenance and inventory consistency. It
// does not look at the entries themselves.
func ValidateManifest(manifest Manifest) error {
	if manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("archive format version %d is not supported", manifest.FormatVersion)
	}
	switch manifest.Kind {
	case KindProduction, KindFixture:
	default:
		return fmt.Errorf("archive kind %q is not supported", manifest.Kind)
	}
	if err := validateRevision(manifest.Kind, manifest.Revision); err != nil {
		return fmt.Errorf("archive %w", err)
	}
	if strings.TrimSpace(manifest.SourceAPI) == "" {
		return fmt.Errorf("archive source_api is missing")
	}
	if strings.TrimSpace(manifest.BuilderVersion) == "" {
		return fmt.Errorf("archive builder_version is missing")
	}
	if manifest.Kind == KindProduction {
		// A production catalog must name an exact upstream commit.
		if len(manifest.SourceCommit) != 40 || !isHex(manifest.SourceCommit) {
			return fmt.Errorf("archive source_commit must be a full 40 character commit")
		}
	}
	if len(manifest.AppLanguages) == 0 {
		return fmt.Errorf("archive app_languages is empty")
	}
	for _, language := range manifest.AppLanguages {
		if !IsAppLanguage(language) {
			return fmt.Errorf("archive app_language %q is outside the application scope", language)
		}
	}
	if !hex64.MatchString(manifest.SourceInventory.ArchiveSHA256) {
		return fmt.Errorf("archive source inventory archive_sha256 must be 64 lowercase hex characters")
	}
	if !hex64.MatchString(manifest.SourceInventory.AssetsIndexSHA256) {
		return fmt.Errorf("archive source inventory assets_index_sha256 must be 64 lowercase hex characters")
	}
	if len(manifest.Entries) == 0 {
		return fmt.Errorf("archive entry inventory is empty")
	}
	seen := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.Path == ManifestPath {
			return fmt.Errorf("archive entry inventory must not list the manifest itself")
		}
		if err := checkEntryPath(entry.Path); err != nil {
			return fmt.Errorf("archive entry inventory: %w", err)
		}
		if _, exists := seen[entry.Path]; exists {
			return fmt.Errorf("archive entry inventory lists %q twice", entry.Path)
		}
		seen[entry.Path] = struct{}{}
		if !hex64.MatchString(entry.SHA256) {
			return fmt.Errorf("archive entry %q has no valid sha256", entry.Path)
		}
		if entry.Bytes < 0 || entry.Bytes > MaxEntryBytes {
			return fmt.Errorf("archive entry %q size %d is out of bounds", entry.Path, entry.Bytes)
		}
	}
	for _, required := range []string{SourceArchivePath, AssetsIndexPath, IndexPath} {
		if _, exists := seen[required]; !exists {
			return fmt.Errorf("archive entry inventory is missing %q", required)
		}
	}
	return nil
}

// checkEntryPath rejects absolute, unnormalized and backslash entry names.
func checkEntryPath(path string) error {
	if path == "" {
		return fmt.Errorf("entry path is empty")
	}
	if strings.ContainsAny(path, "\\\x00") {
		return fmt.Errorf("entry path %q contains an unsupported character", path)
	}
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("entry path %q is not a relative file path", path)
	}
	if len(path) > 1 && path[1] == ':' {
		return fmt.Errorf("entry path %q is not a relative file path", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("entry path %q is not a normalized relative path", path)
		}
	}
	return nil
}

// ValidateIndex checks the catalog index for ordering, duplicates and entry
// paths that match their set IDs.
func ValidateIndex(index Index) error {
	seen := make(map[string]struct{}, len(index.Sets))
	previous := ""
	for _, entry := range index.Sets {
		if !safeSetID.MatchString(entry.SetID) {
			return fmt.Errorf("index set id %q is not a supported provider identifier", entry.SetID)
		}
		if _, exists := seen[entry.SetID]; exists {
			return fmt.Errorf("index lists set %q twice", entry.SetID)
		}
		seen[entry.SetID] = struct{}{}
		if previous != "" && entry.SetID <= previous {
			return fmt.Errorf("index is not sorted by set id at %q", entry.SetID)
		}
		previous = entry.SetID
		if entry.Path != SetEntryPath(entry.SetID) {
			return fmt.Errorf("index set %q points at %q", entry.SetID, entry.Path)
		}
		if IsPocketSeries(entry.SeriesID) {
			return fmt.Errorf("index set %q belongs to the Pocket series", entry.SetID)
		}
		if strings.TrimSpace(entry.Name) == "" {
			return fmt.Errorf("index set %q has no name", entry.SetID)
		}
		if entry.CardCountOfficial < 0 || entry.CardCountTotal < 0 || entry.ListedCardCount < 0 {
			return fmt.Errorf("index set %q has a negative count", entry.SetID)
		}
		if err := validateLanguages(entry.Languages); err != nil {
			return fmt.Errorf("index set %q: %w", entry.SetID, err)
		}
		if len(entry.Languages) == 0 || entry.Languages[0] != "en" {
			return fmt.Errorf("index set %q must list en first", entry.SetID)
		}
	}
	return nil
}

func validateLanguages(languages []string) error {
	seen := make(map[string]struct{}, len(languages))
	previous := ""
	for position, language := range languages {
		if !IsAppLanguage(language) {
			return fmt.Errorf("language %q is outside the application scope", language)
		}
		if _, exists := seen[language]; exists {
			return fmt.Errorf("language %q is listed twice", language)
		}
		seen[language] = struct{}{}
		// en is pinned first as the fallback; the rest are sorted.
		if position > 1 && language <= previous {
			return fmt.Errorf("languages are not sorted at %q", language)
		}
		previous = language
	}
	return nil
}

// ValidateSetRecord checks one compiled set against the index row that
// announced it: identity, parent relationships, duplicates, language scope and
// image references.
func ValidateSetRecord(record SetRecord, entry IndexEntry) error {
	if record.Set.ID != entry.SetID {
		return fmt.Errorf("set record %q was announced as %q", record.Set.ID, entry.SetID)
	}
	if record.CardsComplete != entry.CardsComplete {
		return fmt.Errorf("set %q disagrees with the index about cards_complete", entry.SetID)
	}
	if !record.CardsComplete && strings.TrimSpace(record.IncompleteReason) == "" {
		return fmt.Errorf("set %q is incomplete without a reason", entry.SetID)
	}
	if record.CardsComplete && strings.TrimSpace(record.IncompleteReason) != "" {
		return fmt.Errorf("set %q is complete but carries an incomplete reason", entry.SetID)
	}
	if len(record.Cards) != entry.ListedCardCount {
		return fmt.Errorf("set %q lists %d cards, the index reports %d", entry.SetID, len(record.Cards), entry.ListedCardCount)
	}
	if err := validateSet(record.Set); err != nil {
		return fmt.Errorf("set %q: %w", entry.SetID, err)
	}
	if IsPocketSeries(record.Set.SeriesID) {
		return fmt.Errorf("set %q belongs to the Pocket series", entry.SetID)
	}

	announced := make(map[string]struct{}, len(entry.Languages))
	for _, language := range entry.Languages {
		announced[language] = struct{}{}
	}
	seenLanguage := make(map[string]struct{}, len(record.Localizations))
	for _, localization := range record.Localizations {
		if localization.Language == "en" {
			return fmt.Errorf("set %q stores an en localization; English is the record itself", entry.SetID)
		}
		if !IsAppLanguage(localization.Language) {
			return fmt.Errorf("set %q has localization %q outside the application scope", entry.SetID, localization.Language)
		}
		if _, exists := seenLanguage[localization.Language]; exists {
			return fmt.Errorf("set %q has localization %q twice", entry.SetID, localization.Language)
		}
		seenLanguage[localization.Language] = struct{}{}
		if _, exists := announced[localization.Language]; !exists {
			return fmt.Errorf("set %q has unannounced localization %q", entry.SetID, localization.Language)
		}
		if localization.Set.ID != record.Set.ID {
			return fmt.Errorf("set %q localization %q has set id %q", entry.SetID, localization.Language, localization.Set.ID)
		}
		if err := validateSet(localization.Set); err != nil {
			return fmt.Errorf("set %q localization %q: %w", entry.SetID, localization.Language, err)
		}
	}

	seenCard := make(map[string]struct{}, len(record.Cards))
	seenNumber := make(map[string]string, len(record.Cards))
	previous := ""
	for _, card := range record.Cards {
		if err := validateCard(card.Card, record.Set.ID); err != nil {
			return fmt.Errorf("set %q: %w", entry.SetID, err)
		}
		if _, exists := seenCard[card.Card.ID]; exists {
			return fmt.Errorf("set %q lists card %q twice", entry.SetID, card.Card.ID)
		}
		seenCard[card.Card.ID] = struct{}{}
		number := card.Card.CollectorNumber()
		if previousID, exists := seenNumber[number]; exists {
			return fmt.Errorf("set %q cards %q and %q share collector number %q", entry.SetID, previousID, card.Card.ID, number)
		}
		seenNumber[number] = card.Card.ID
		if previous != "" && card.Card.ID <= previous {
			return fmt.Errorf("set %q cards are not sorted at %q", entry.SetID, card.Card.ID)
		}
		previous = card.Card.ID

		seenCardLanguage := make(map[string]struct{}, len(card.Localizations))
		for _, localization := range card.Localizations {
			if localization.Language == "en" {
				return fmt.Errorf("card %q stores an en localization; English is the record itself", card.Card.ID)
			}
			if _, exists := announced[localization.Language]; !exists {
				return fmt.Errorf("card %q has unannounced localization %q", card.Card.ID, localization.Language)
			}
			if _, exists := seenCardLanguage[localization.Language]; exists {
				return fmt.Errorf("card %q has localization %q twice", card.Card.ID, localization.Language)
			}
			seenCardLanguage[localization.Language] = struct{}{}
			if localization.Card.ID != card.Card.ID {
				return fmt.Errorf("card %q localization %q has card id %q", card.Card.ID, localization.Language, localization.Card.ID)
			}
			if err := validateCard(localization.Card, record.Set.ID); err != nil {
				return fmt.Errorf("card %q localization %q: %w", card.Card.ID, localization.Language, err)
			}
			// Translations must use the English collector number.
			if strings.TrimSpace(localization.Card.LocalID) != strings.TrimSpace(card.Card.LocalID) {
				return fmt.Errorf("card %q localization %q has local id %q, expected %q", card.Card.ID, localization.Language, localization.Card.LocalID, card.Card.LocalID)
			}
		}
	}
	return nil
}

func validateSet(set Set) error {
	if !safeSetID.MatchString(set.ID) {
		return fmt.Errorf("set id %q is not a supported provider identifier", set.ID)
	}
	if strings.TrimSpace(set.Name) == "" {
		return fmt.Errorf("set name is missing")
	}
	if strings.TrimSpace(set.SeriesID) == "" {
		return fmt.Errorf("set series id is missing")
	}
	if set.CardCountOfficial < 0 || set.CardCountTotal < 0 {
		return fmt.Errorf("set card counts cannot be negative")
	}
	if err := ValidateImageReference(set.Logo); err != nil {
		return fmt.Errorf("set logo: %w", err)
	}
	if err := ValidateImageReference(set.Symbol); err != nil {
		return fmt.Errorf("set symbol: %w", err)
	}
	return nil
}

func validateCard(card Card, expectedSetID string) error {
	if strings.TrimSpace(card.ID) == "" {
		return fmt.Errorf("card id is missing")
	}
	if strings.TrimSpace(card.Name) == "" {
		return fmt.Errorf("card %q has no name", card.ID)
	}
	if card.CollectorNumber() == "" {
		return fmt.Errorf("card %q has no collector number", card.ID)
	}
	if card.SetID != "" && card.SetID != expectedSetID {
		return fmt.Errorf("card %q belongs to set %q, expected %q", card.ID, card.SetID, expectedSetID)
	}
	if !card.VariantsDetailedPresent && len(card.VariantsDetailed) > 0 {
		return fmt.Errorf("card %q has variants while variants_detailed_present is false", card.ID)
	}
	seenVariantID := make(map[string]PrintingIdentity, len(card.VariantsDetailed))
	claimedIdentity := make(map[PrintingIdentity]string, len(card.VariantsDetailed))
	for _, variant := range card.VariantsDetailed {
		if strings.TrimSpace(variant.Type) == "" {
			return fmt.Errorf("card %q printing type is required", card.ID)
		}
		variantID := strings.TrimSpace(variant.VariantID)
		if variantID == "" || variantID == "generated" || variant.HasFirstEditionStamp() {
			continue
		}
		identity := variant.Identity()
		if previous, exists := seenVariantID[variantID]; exists {
			if previous != identity {
				return fmt.Errorf("card %q reuses printing variant id %q for different identities", card.ID, variantID)
			}
			continue
		}
		seenVariantID[variantID] = identity
		if previousID, exists := claimedIdentity[identity]; exists {
			return fmt.Errorf("card %q variants %q and %q claim the same printing identity", card.ID, previousID, variantID)
		}
		claimedIdentity[identity] = variantID
	}
	if err := ValidateImageReference(card.Image); err != nil {
		return fmt.Errorf("card %q image: %w", card.ID, err)
	}
	return nil
}

// ValidateImageReference applies the production asset URL policy: HTTPS on the
// TCGdex asset host, default port, no credentials. An empty reference means
// the image is unavailable, which is allowed.
func ValidateImageReference(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("image reference is not a URL: %w", err)
	}
	if parsed.User != nil {
		return fmt.Errorf("image reference must not embed credentials")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("image reference scheme %q is not allowed", parsed.Scheme)
	}
	if !strings.EqualFold(parsed.Hostname(), AssetHost) {
		return fmt.Errorf("image reference host %q is not allowed", parsed.Host)
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return fmt.Errorf("image reference port %q is not allowed", port)
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		return fmt.Errorf("image reference must name the asset host, not a literal address")
	}
	return nil
}

// SortLanguages returns the canonical language order: en first as the
// fallback, the rest sorted.
func SortLanguages(languages []string) []string {
	seen := map[string]bool{}
	rest := make([]string, 0, len(languages))
	english := false
	for _, language := range languages {
		language = strings.ToLower(strings.TrimSpace(language))
		if language == "" || seen[language] {
			continue
		}
		seen[language] = true
		if language == "en" {
			english = true
			continue
		}
		rest = append(rest, language)
	}
	sort.Strings(rest)
	out := make([]string, 0, len(rest)+1)
	if english {
		out = append(out, "en")
	}
	return append(out, rest...)
}

func isHex(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return len(value) > 0
}

// MarshalCanonical encodes a value as the archive stores it: compact, without
// HTML escaping, so the bytes depend only on the value.
func MarshalCanonical(value any) ([]byte, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}
