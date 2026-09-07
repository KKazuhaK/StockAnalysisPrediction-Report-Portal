// What to show for a report version (ADR 0024).
//
// A version's display name is an admin's own text and is shown verbatim — translating it would be
// inventing words on their behalf. But the DEFAULT version is seeded with no label at all, and the
// server falls back to the internal identifier, so every ordinary report was filed under a literal
// "default" wherever versions are listed: the reading page's switcher, and the browse filter.
// That identifier is a product concept rather than anybody's data, so it gets a translated name.
//
// The manual version is deliberately NOT handled here. It is seeded WITH a label, so it is
// indistinguishable from one an admin typed, and guessing by comparing against the seeded string
// would be a hardcoded Chinese literal in the client. Giving the reserved versions a flag of their
// own on the wire is the real fix, and it is a schema-shaped change rather than a display one.
export function versionLabel(name: string, label: string, t: (k: string) => string): string {
  if (label && label !== name) return label
  if (name === 'default') return t('versions.default')
  return name
}
