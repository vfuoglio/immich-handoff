package syncer

import "testing"

func TestAFailedPassIsToldInWordsNotAsAStruct(t *testing.T) {
	got := passSummary(&RunInfo{Counts: Counts{Errors: 2, Refused: 1, Imported: 5}, Error: "immich said no"})
	want := "A hand-over pass ended with problems: 2 failed, 1 refused, 5 handed over. immich said no"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := passSummary(&RunInfo{Error: "x"}); got != "A hand-over pass ended with problems. x" {
		t.Errorf("got %q", got)
	}
}
