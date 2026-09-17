package kiro

import "strings"

// dsmlMarkers are the internal Kiro DSL sentinels that leak into the
// assistant text stream. Kiro emits them between "Say" segments of a
// tool-calling turn (for example "Voy a revisar...\n\n<｜DSML｜function_calls"),
// and they must never reach the Telegram user. The full-width vertical
// line is U+FF5C; the ASCII variant is handled defensively.
var dsmlMarkers = []string{
	"<\uFF5CDSML\uFF5Cfunction_calls",
	"<|DSML|function_calls",
	"<\uFF5CDSML\uFF5C",
	"<|DSML|",
}

// stripAssistantSentinels removes the leaked DSML markers from an
// assistant reply while preserving any real text that follows them
// (e.g. a trailing "understood"), then normalizes the whitespace the
// markers leave behind.
func stripAssistantSentinels(text string) string {
	if text == "" {
		return ""
	}
	for _, marker := range dsmlMarkers {
		text = strings.ReplaceAll(text, marker, "")
	}
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimRight(text, " \t\r\n")
}
