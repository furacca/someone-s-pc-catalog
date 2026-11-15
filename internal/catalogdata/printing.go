package catalogdata

import (
	"sort"
	"strings"
)

// PrintingIdentity is the normalized physical printing key used by archive
// validation and by the application database. First Edition is a property of
// the owned copy, not of the printing.
type PrintingIdentity struct {
	Type    string
	Subtype string
	Size    string
	Stamp   string
	Foil    string
}

// Identity applies the same defaults and stamp normalization at build and import.
func (v Variant) Identity() PrintingIdentity {
	size := strings.TrimSpace(v.Size)
	if size == "" {
		size = "standard"
	}
	return PrintingIdentity{
		Type:    strings.TrimSpace(v.Type),
		Subtype: strings.TrimSpace(v.Subtype),
		Size:    size,
		Stamp:   stampValue(v.Stamp),
		Foil:    strings.TrimSpace(v.Foil),
	}
}

func stampValue(stamp []string) string {
	seen := make(map[string]struct{}, len(stamp))
	canonical := make([]string, 0, len(stamp))
	for _, value := range stamp {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "1st-edition") {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		canonical = append(canonical, value)
	}
	sort.Strings(canonical)
	var encoded strings.Builder
	encoded.WriteByte('[')
	for index, value := range canonical {
		if index > 0 {
			encoded.WriteByte(',')
		}
		appendSQLiteJSONString(&encoded, value)
	}
	encoded.WriteByte(']')
	return encoded.String()
}

// appendSQLiteJSONString encodes value as SQLite's json_group_array does: it
// emits valid Unicode directly, while json.Marshal escapes HTML characters and
// line separators. The database trigger compares against SQLite's canonical
// bytes because stamp participates in a UNIQUE identity.
func appendSQLiteJSONString(encoded *strings.Builder, value string) {
	const hex = "0123456789abcdef"
	encoded.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			encoded.WriteByte('\\')
			encoded.WriteRune(r)
		case '\b':
			encoded.WriteString(`\b`)
		case '\f':
			encoded.WriteString(`\f`)
		case '\n':
			encoded.WriteString(`\n`)
		case '\r':
			encoded.WriteString(`\r`)
		case '\t':
			encoded.WriteString(`\t`)
		default:
			if r < 0x20 {
				encoded.WriteString(`\u00`)
				encoded.WriteByte(hex[byte(r)>>4])
				encoded.WriteByte(hex[byte(r)&0x0f])
			} else {
				encoded.WriteRune(r)
			}
		}
	}
	encoded.WriteByte('"')
}
