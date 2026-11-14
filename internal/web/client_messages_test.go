package web

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
)

// What the scripts ask t() and n() for must be exactly what the page carries for them (clientMessages), or a script
// would show English in another language, or the page would carry messages nobody reads.
func TestThePageCarriesExactlyTheMessagesTheScriptsAskFor(t *testing.T) {
	files, _ := filepath.Glob("ts/*.ts")
	call := regexp.MustCompile("\\b(?:[tn]|forms)\\(\\s*(\"(?:[^\"\\\\]|\\\\.)*\")")
	found := map[string]bool{}
	for _, f := range files {
		if filepath.Base(f) == "i18n.ts" || filepath.Ext(f) != ".ts" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllSubmatch(src, -1) {
			id, err := strconv.Unquote(string(m[1]))
			if err != nil {
				t.Fatalf("%s: %s: %v", f, m[1], err)
			}
			found[id] = true
		}
	}
	if len(found) < 30 {
		t.Fatalf("only %d script messages found; the scan is broken", len(found))
	}
	for id := range found {
		if !slices.Contains(clientMessages, id) {
			t.Errorf("a script asks for %q but the page does not carry it", id)
		}
	}
	for _, id := range clientMessages {
		if !found[id] {
			t.Errorf("the page carries %q but no script asks for it", id)
		}
	}
	for _, l := range i18n.All[1:] {
		for _, id := range clientMessages {
			if i18n.Text(l, id) == "" && i18n.Forms(l, id) == nil {
				t.Errorf("%s has no translation for the script message %q", l, id)
			}
		}
	}
}
