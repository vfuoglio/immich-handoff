package web

import "strings"

func initial(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	return strings.ToUpper(string([]rune(name)[0]))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func roleLabel(role string) string {
	if role == "sender" {
		return "You send"
	}
	return "You receive"
}

func roleClass(role string) string {
	if role == "sender" {
		return ""
	}
	return "ok"
}

func countLabel(role string) string {
	if role == "sender" {
		return "waiting to be handed over"
	}
	return "in your inbox"
}

func warnIf(b bool) string {
	if b {
		return "warn"
	}
	return ""
}

func badIf(b bool) string {
	if b {
		return "bad"
	}
	return ""
}

func badClass(b bool) string {
	if b {
		return "bad"
	}
	return ""
}

// joinConfirm tells the person exactly what joining does before they agree.
func joinConfirm(j JoinView) string {
	return "Joining creates an API key named \"handoff: " + j.Name + "\" in your Immich account. " +
		"Immich Handoff uses it to read and download the photos you tag \"" + j.Tag + "\" and to delete the originals " +
		"after they have been handed over. Nobody else can see the key. You can stop at any time with Leave, " +
		"or by deleting that key in Immich."
}
