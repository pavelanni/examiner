package views

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// msgIDCall matches a translation call whose message ID is a literal: t, td
// and tp in views and appI18n.T/Td and i18n.T/Td in handlers take the
// context first, then the ID; httpError takes w, r and the status first.
// IDs may contain spaces: such IDs ("Create Questions") once rendered raw.
var msgIDCall = regexp.MustCompile(`(?:(?:\bt|\btd|\btp|[Ii]18n\.Td?)\(\s*[A-Za-z_.]+(?:\(\))?\s*,|\bhttpError\(\s*w\s*,\s*r\s*,\s*[^,()]+,)\s*"([A-Za-z][A-Za-z0-9_ ]*)"`)

func localeIDs(t *testing.T, lang string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "i18n", "locales", "active."+lang+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &msgs); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		ids[m.ID] = true
	}
	return ids
}

// TestMessageIDsExist fails on a literal message ID used in internal/handler
// that a locale lacks: a missing ID is rendered as the ID itself.
func TestMessageIDsExist(t *testing.T) {
	used := map[string][]string{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_templ.go") ||
			!strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".templ") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range msgIDCall.FindAllStringSubmatch(string(src), -1) {
			used[m[1]] = append(used[m[1]], filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) < 100 {
		t.Fatalf("found only %d message IDs; the pattern no longer matches the code", len(used))
	}
	for _, lang := range []string{"en", "ru"} {
		ids := localeIDs(t, lang)
		var missing []string
		for id, files := range used {
			if !ids[id] {
				missing = append(missing, id+" ("+strings.Join(files, ", ")+")")
			}
		}
		sort.Strings(missing)
		for _, m := range missing {
			t.Errorf("active.%s.json lacks %s", lang, m)
		}
	}
}
