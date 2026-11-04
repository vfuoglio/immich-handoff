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
