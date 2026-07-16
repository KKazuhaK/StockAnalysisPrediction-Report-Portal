package app

import "strings"

// displayTitle composes the human-facing report title used for the on-screen heading,
// the PDF <h1>, and the MD/PDF download filenames. Ingest stores a bare title like
// "001696 投资决策建议" (symbol + descriptor, no company name), so this folds the company
// name in to read "001696 宗申动力 投资决策建议". name is a snapshot (rep.Name) so history
// stays correct after a rename / backdoor listing. It is idempotent and a no-op when
// there is no name to add (thematic reports, or a pre-snapshot report with no name).
func displayTitle(title, symbol, name string) string {
	title = strings.TrimSpace(title)
	symbol = strings.TrimSpace(symbol)
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(title, name) {
		return title // nothing to add, or the title already carries the name
	}
	if symbol != "" && strings.HasPrefix(title, symbol) {
		// Insert the name between the leading symbol and the rest of the descriptor,
		// normalizing whatever spacing the ingested title used.
		rest := strings.TrimSpace(strings.TrimPrefix(title, symbol))
		if rest == "" {
			return symbol + " " + name
		}
		return symbol + " " + name + " " + rest
	}
	// No leading symbol to anchor on: just prefix the name.
	return strings.TrimSpace(name + " " + title)
}

// repDisplayTitle resolves a report's as-of company name — the ingest-time snapshot,
// falling back to the current name for pre-snapshot reports — and folds it into the
// title via displayTitle. Nil-safe on s.names so unit tests can call it on a bare Server.
func (s *Server) repDisplayTitle(rep *Rep) string {
	name := rep.Name
	if name == "" && s.names != nil {
		name = s.names.Get(rep.Symbol)
	}
	return displayTitle(rep.Title, rep.Symbol, name)
}
