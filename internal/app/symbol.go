package app

import (
	"regexp"
	"strings"
)

// A stock code as this portal stores it: the six digits an A-share listing is identified by, with no
// exchange suffix. `symbol` is part of a report's identity key, so whatever lands here is what the
// report is filed under forever — a value that is not a code files the report under a company that
// does not exist, and puts a ghost entry in the per-stock list.
//
// Nothing upstream guarantees the shape. Ingest is a machine surface fed by workflows whose symbol
// is produced by an LLM, and a free-text extractor answers with whatever it likes when it cannot
// answer properly: an ellipsis, a restatement of the format it was asked for, the name of the
// variable it was asked to fill, two codes joined by a comma, a five-digit Hong Kong code. All of
// those were stored.
var (
	reSymbolDigits = regexp.MustCompile(`^[0-9]{6}$`)
	reSymbolSuffix = regexp.MustCompile(`(?i)\.(SH|SZ|BJ)$`)
	// Full-width digits reach us from Chinese-language models often enough to be worth folding.
	symbolFullWidth = strings.NewReplacer(
		"０", "0", "１", "1", "２", "2", "３", "3", "４", "4",
		"５", "5", "６", "6", "７", "7", "８", "8", "９", "9")
)

// normalizeSymbol folds a symbol into the form reports are stored under and reports whether an
// unusable one was discarded.
//
// An EMPTY symbol is not a failure and never sets dropped: a thematic or multi-stock report has no
// single holding, and those are meant to stand on their own. Dropping a malformed symbol puts the
// report in exactly that state, which is the honest one — far better than filing it under a company
// invented by a truncated model answer. Guessing is never an option here: two codes joined together
// say the report is about both, and picking one would be a fabrication, not a repair.
func normalizeSymbol(raw string) (symbol string, dropped bool) {
	s := strings.TrimSpace(symbolFullWidth.Replace(raw))
	s = strings.TrimSpace(reSymbolSuffix.ReplaceAllString(s, ""))
	switch {
	case s == "":
		return "", false
	case reSymbolDigits.MatchString(s):
		return s, false
	default:
		return "", true
	}
}
