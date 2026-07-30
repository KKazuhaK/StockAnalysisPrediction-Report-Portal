package app

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/ssorules"
)

// The SSO group rules, stored as one ordered JSON list in `meta`.
//
// They were a table, and the table earned nothing. Every write already replaced the whole list in a
// transaction — the admin edits it as one ordered thing — so the rows only ever moved together, and
// reading them meant a query, a scan loop and an index to keep the order stable. One value holds
// the same list, is written with one statement, and cannot end up half-applied.
//
// The Passwall panel reaches the same place from the other direction: it keeps its role rules as a
// JSON column on the provider config. The difference here is that a rule may be GLOBAL (pinned to
// no provider), so the list belongs to the portal rather than to one provider — hence `meta` rather
// than a column on sso_providers.

const setSSORules = "sso_group_rules"

// storedRule is one rule as persisted. It carries ProviderID, which ssorules.Rule deliberately does
// not: the rule engine is a pure function over rules that already apply, and which provider a rule
// belongs to is a storage concern the engine must not have to know about.
type storedRule struct {
	ID          int64  `json:"id"`
	ProviderID  int64  `json:"provider_id"`
	Ord         int    `json:"ord"`
	Enabled     bool   `json:"enabled"`
	Attr        string `json:"attr"`
	Value       string `json:"value"`
	TargetRole  string `json:"target_role"`
	TargetGroup int64  `json:"target_group"`
	KeepOnMiss  bool   `json:"keep_on_miss"`
	CI          bool   `json:"ci"`
	Note        string `json:"note"`
}

// SSORules returns the whole stored list in display order.
func (s *Store) SSORules() []storedRule {
	raw := strings.TrimSpace(s.GetSetting(setSSORules, ""))
	if raw == "" {
		return nil
	}
	var out []storedRule
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ord < out[j].Ord })
	return out
}

// SaveSSORules replaces the list. Ord and ID are assigned here from the submitted order, so the
// caller cannot store a list whose order depends on values it chose — the array IS the order.
func (s *Store) SaveSSORules(rules []storedRule) error {
	for i := range rules {
		rules[i].Ord = i
		rules[i].ID = int64(i + 1)
		rules[i].Attr = strings.TrimSpace(rules[i].Attr)
	}
	if len(rules) == 0 {
		return s.SetSetting(setSSORules, "")
	}
	b, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	return s.SetSetting(setSSORules, string(b))
}

// DeleteRulesOfProvider drops the rules pinned to one provider, leaving the global ones. Called when
// a provider is removed, so its rules do not linger to be matched against a provider id that a
// later one could reuse.
func (s *Store) DeleteRulesOfProvider(id int64) error {
	all := s.SSORules()
	kept := make([]storedRule, 0, len(all))
	for _, r := range all {
		if r.ProviderID != id {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(all) {
		return nil
	}
	return s.SaveSSORules(kept)
}

// rulesFor narrows the stored list to the ones that apply to a provider — its own plus the global
// ones — and converts them to the engine's type.
func (s *Store) rulesFor(providerID int64) []ssorules.Rule {
	var out []ssorules.Rule
	for _, r := range s.SSORules() {
		if r.ProviderID != 0 && r.ProviderID != providerID {
			continue
		}
		out = append(out, ssorules.Rule{
			ID: r.ID, Ord: r.Ord, Enabled: r.Enabled, Attr: r.Attr, Value: r.Value,
			TargetRole: r.TargetRole, TargetGroup: r.TargetGroup,
			KeepOnMiss: r.KeepOnMiss, CaseInsensitive: r.CI, Note: r.Note,
		})
	}
	return out
}
