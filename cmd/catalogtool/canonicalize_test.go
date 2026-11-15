package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalizePreservesDataAndMeaningfulOrder(t *testing.T) {
	inputs := []string{
		`[{"id":"b","cards":[{"id":"2"},{"id":"1"}],"variants_detailed":[{"variantId":"z"},{"variantId":"a"}],"attacks":[{"id":"z"},{"id":"a"}],"number":9007199254740993},{"id":"a","name":"second"},{"name":"first","id":"a"}]`,
		`[{"name":"first","id":"a"},{"id":"a","name":"second"},{"number":9007199254740993,"attacks":[{"id":"z"},{"id":"a"}],"variants_detailed":[{"variantId":"z"},{"variantId":"a"}],"cards":[{"id":"1"},{"id":"2"}],"id":"b"}]`,
	}
	var previous []byte
	for _, input := range inputs {
		root := t.TempDir()
		path := filepath.Join(root, "cards.json")
		if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runCanonicalize([]string{"-generated", root}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, got) {
			t.Fatalf("entity ordering changed canonical bytes: %s", got)
		}
		for _, expected := range []string{
			`"number":9007199254740993`,
			`"attacks":[{"id":"z"},{"id":"a"}]`,
			`"variants_detailed":[{"variantId":"z"},{"variantId":"a"}]`,
			`"cards":[{"id":"1"},{"id":"2"}]`,
			`{"id":"a","name":"first"},{"id":"a","name":"second"}`,
		} {
			if !bytes.Contains(got, []byte(expected)) {
				t.Fatalf("canonical output lost %s: %s", expected, got)
			}
		}
		if err := runCanonicalize([]string{"-generated", root}); err != nil {
			t.Fatal(err)
		}
		again, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, again) {
			t.Fatalf("canonicalization was not idempotent: %v", err)
		}
		previous = got
	}
	for _, input := range []string{`[{"id":"a"}`, `[] {}`} {
		path := filepath.Join(t.TempDir(), "cards.json")
		if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := canonicalizeFile(path); err == nil {
			t.Fatal("malformed generated JSON was accepted")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != input {
			t.Fatal("invalid input was overwritten")
		}
	}
}
