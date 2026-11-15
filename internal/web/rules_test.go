package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/web"
)

func TestASuggestionCardShowsThePhotosAndTheThreeAnswers(t *testing.T) {
	var sb strings.Builder
	web.NeedCard(web.NeedView{Tone: "warn", Chip: "Suggestion", Title: "3 new photos for Family", Text: "Found by your rule", More: 2,
		Thumbs: []web.NeedThumb{{URL: "rules/photo/r1/a1", Why: "Nora in it"}},
		Actions: []web.NeedAction{{Label: "Send", Path: "rules/answer", Vals: `{"rule":"r1","do":"send"}`, Primary: true}, {Label: "Not these", Path: "rules/answer", Vals: `{"rule":"r1","do":"decline"}`}, {Label: "Always", Path: "rules/answer", Vals: `{"rule":"r1","do":"always"}`}},
	}).Render(context.Background(), &sb)
	got := sb.String()
	for _, want := range []string{`src="rules/photo/r1/a1"`, `title="Nora in it"`, "+2", ">Send<", ">Not these<", ">Always<"} {
		if !strings.Contains(got, want) {
			t.Errorf("the card lacks %q: %s", want, got)
		}
	}
}
