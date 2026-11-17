package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// stackWords says what a stacking rule does, in the person's language.
func stackWords(ctx context.Context, c store.RuleCond) string {
	var what []string
	for _, s := range c.Signals {
		switch s {
		case hub.SignalPair:
			what = append(what, t(ctx, "a RAW with its JPEG"))
		case hub.SignalEdit:
			what = append(what, t(ctx, "an edit with its original"))
		case hub.SignalBurst:
			what = append(what, t(ctx, "a burst"))
		}
	}
	out := t(ctx, "Stack {things}", "things", strings.Join(what, ", "))
	if c.Folder != "" {
		out += " " + t(ctx, "in {folder}", "folder", c.Folder)
	}
	return out
}

// stackViews are the person's rules that stack, for their page.
func (w *Server) stackViews(ctx context.Context, me *member) StacksView {
	if w.Hub == nil || !w.Hub.SelfService() {
		return StacksView{}
	}
	v := StacksView{Available: true}
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, "")
	if err != nil {
		return v
	}
	l := loc(ctx)
	for _, r := range rules {
		if r.Kind != store.RuleKindStack {
			continue
		}
		sv := StackRuleView{ID: r.ID, Words: stackWords(ctx, r.Cond), Mode: r.Mode, Always: r.Always, Paused: r.Paused, CanAuto: hub.CanAuto(r), Problem: r.Problem,
			Undoable: w.Hub.StackUndoable(ctx, me.user.ID, r.ID)}
		if !r.LastLook.IsZero() {
			sv.Last = l.Stamp(r.LastLook.Local())
		}
		v.Rules = append(v.Rules, sv)
	}
	return v
}

// stackSignalsOf says, in words, how many waiting groups each kind has.
func stackSignalsOf(ctx context.Context, groups []hub.StackWaiting) string {
	var pair, edit, burst int
	for _, g := range groups {
		switch g.Signal {
		case hub.SignalPair:
			pair++
		case hub.SignalEdit:
			edit++
		case hub.SignalBurst:
			burst++
		}
	}
	var parts []string
	if pair > 0 {
		parts = append(parts, n(ctx, "{n} RAW with its JPEG", "{n} RAWs with their JPEGs", pair))
	}
	if edit > 0 {
		parts = append(parts, n(ctx, "{n} edit with its original", "{n} edits with their originals", edit))
	}
	if burst > 0 {
		parts = append(parts, n(ctx, "{n} burst", "{n} bursts", burst))
	}
	return strings.Join(parts, ", ")
}

// stackParams reads the form of a stacking rule.
func stackParams(get func(string) string) hub.StackRuleParams {
	return hub.StackRuleParams{Folder: get("folder"), Pair: get("pair") == "1", Edit: get("edit") == "1", Burst: get("burst") == "1", Cover: get("cover")}
}

// stackCheck tells the form how many groups of each kind the rule would find among the person's photos now.
func (w *Server) stackCheck(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	ctx := r.Context()
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	counts, photos, err := w.Hub.StackCounts(ctx, me.sess, me.user, stackParams(r.URL.Query().Get))
	switch {
	case err != nil && hub.IsUserError(err):
		StackCheck(t(ctx, err.Error())).Render(ctx, rw)
	case err != nil:
		w.Log.Warn("could not look for stacks", "user", me.user.Email, "err", err)
		StackCheck(t(ctx, "Could not look at your photos")).Render(ctx, rw)
	default:
		var groups []hub.StackWaiting
		for sig, c := range counts {
			for i := 0; i < c; i++ {
				groups = append(groups, hub.StackWaiting{Signal: sig})
			}
		}
		words := stackSignalsOf(ctx, groups)
		if words == "" {
			StackCheck(t(ctx, "Looked at {n} of your photos and found nothing to stack.", "n", photos)).Render(ctx, rw)
			return
		}
		StackCheck(t(ctx, "Looked at {n} of your photos and found: {found}.", "n", photos, "found", words)).Render(ctx, rw)
	}
}

// followStack makes a rule that stacks.
func (w *Server) followStack(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "stacking rule made", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.FollowStacking(ctx, me.sess, me.user, stackParams(r.PostFormValue))
		return err
	})
}

// undoStack takes apart the stacks the app made for a rule in the last 15 minutes.
func (w *Server) undoStack(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "stacks taken apart", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.UndoStacks(ctx, me.user, r.PostFormValue("rule"))
		return err
	})
}
