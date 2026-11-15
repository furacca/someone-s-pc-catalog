package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

func runCanonicalize(args []string) error {
	flags := flag.NewFlagSet("canonicalize", flag.ExitOnError)
	generated := flags.String("generated", "", "compiler output directory to normalize before snapshotting")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *generated == "" || flags.NArg() != 0 {
		return fmt.Errorf("canonicalize: -generated is required and no positional arguments are accepted")
	}
	count := 0
	err := filepath.WalkDir(*generated, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) != ".json" {
			return nil
		}
		if err := canonicalizeFile(path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("canonicalize: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("canonicalize: no generated JSON files found")
	}
	fmt.Printf("normalized %d generated JSON files\n", count)
	return nil
}

func canonicalizeFile(path string) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	normalizeEntities(value, true)
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

// normalizeEntities sorts catalogs and nested card/set lists because the
// compiler loads entities concurrently. It retains every record, including
// duplicate IDs outside the app scope.
// Variant and attack order is meaningful and must stay as the compiler emitted it.
func normalizeEntities(value any, entities bool) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalizeEntities(child, key == "cards" || key == "sets")
		}
	case []any:
		for _, child := range value {
			normalizeEntities(child, false)
		}
		if !entities {
			return
		}
		for _, child := range value {
			object, ok := child.(map[string]any)
			if !ok {
				return
			}
			if _, ok := object["id"].(string); !ok {
				return
			}
		}
		sort.Slice(value, func(i, j int) bool {
			a := value[i].(map[string]any)["id"].(string)
			b := value[j].(map[string]any)["id"].(string)
			if a != b {
				return a < b
			}
			// Decoded JSON values remain encodable; canonical bytes break ID ties.
			left, _ := json.Marshal(value[i])
			right, _ := json.Marshal(value[j])
			return bytes.Compare(left, right) < 0
		})
	}
}
