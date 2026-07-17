# ADR 0020 — Chart rendering (mermaid on the reading page + PDF via client-render, server-splice)

**Status: accepted, partially implemented.** Reading-page rendering is implemented and browser-verified.
The PDF portion remains gated on the production wkhtmltopdf SVG probe described below; no SVG upload,
cache, sanitizer, or splice is merged until that probe passes.

## Context

Report bodies (ingested from the external Dify workflow) contain ` ```mermaid ` fences — the user's real
content is `xychart-beta` stock K-line charts and RSI/KD indicator plots. None of the three read/export
surfaces render them today, and the failure is silent:

- **Reading page** (`web/src/components/Markdown.tsx`) renders markdown with react-markdown +
  remark-gfm + remark-math + rehype-katex. There is no mermaid dependency; a ` ```mermaid ` fence falls
  through to the default code block and prints its source.
- **PDF export** (`/report/{id}/pdf` and the bulk `/report/day.zip`) renders markdown with goldmark
  GFM-only (`internal/app/md.go:13`). Verified empirically: a fence becomes
  `<pre><code class="language-mermaid">`, then `pdfBodyPolicy` (bluemonday, `md.go:18`) strips **every**
  attribute except `colspan`/`rowspan`, so even the `language-mermaid` class is gone before wkhtmltopdf
  sees it. The PDF engine is wkhtmltopdf 0.12.6.1 (Qt 4.8 WebKit) invoked with `--disable-javascript`
  `--no-images`, so a JS charting library could not run there regardless.
- **MD download** (`/report/{id}/md`) writes `rep.MD` verbatim — the fence survives and a mermaid-aware
  local editor renders it. This is the only surface that "works" today.

The same investigation surfaced a **pre-existing, unrelated bug**: goldmark has no math extension, so
`$$...$$` passes through to the PDF as a literal string. KaTeX math has been **silently broken in PDF
export since math shipped** — nobody noticed because the reading page renders it correctly. This ADR does
not fix math (see ADR 0021 and the follow-up note) but records the finding so it stops being invisible.

Root cause across all of this: the project runs **two divergent markdown pipelines** — react-markdown +
KaTeX in the browser, goldmark in Go — that have already drifted (math). mermaid is the second symptom.
ADR 0021 owns collapsing them; this ADR ships charts on the existing architecture without waiting for it.

Design constraints that eliminated the obvious alternatives (all measured, not assumed):

- **No server-side mermaid renderer is lightweight.** mermaid needs a real DOM for text metrics and
  layout, so "render mermaid on the server" is physically "run a headless browser." mermaid-cli peaks at
  **369 MiB RSS for one 6-point xychart — 1.44× the 256 MiB container limit** (`docker-compose.yml`) —
  and needs ~608 MB of CJK fonts (the family the maintainer already rejected in `Dockerfile.release` as
  "overkill"). Kroki is a 442 MiB companion plus a load-bearing 1145 MiB gateway. Both also blow the 90 s
  `pdfRenderTimeout` on `day_export.go`'s serial loop (~230 s of mermaid for a 100-chart day). A headless
  Chromium sidecar is the same class of cost and, rendering untrusted Dify bodies under the wide-open
  `img-src ... https: http:` CSP, an SSRF surface — that trade is ADR 0021's to make, not v1's.
- **A pure-Go xychart renderer produces wrong charts.** Reimplementing mermaid's layout engine
  (nice-number ticks, band scaling, label collision) yields a *plausible-but-wrong* chart (a prototype
  emitted 2 y-ticks where mermaid emits 9, and dropped the axis title) — the worst outcome for a research
  report — and manufactures a permanent screen≠PDF divergence by drawing the two surfaces with two
  different renderers.
- **The `<img src=data:...>` route is dead four ways**: goldmark blanks a `data:image/svg+xml` src by
  design, bluemonday drops `<img>` (not in `AllowElements`), a PNG route would force `--no-images` off
  (re-opening remote-image SSRF against an ingested body), and attributes are stripped anyway. Inline
  `<svg>` is the only embedding route that keeps `--no-images` **on** — `--no-images` maps only to
  `QWebSettings::AutoLoadImages` (the resource loader), not the SVG render tree.

## Decisions

1. **Reading page renders mermaid client-side, lazily.** A `MermaidBlock` component overrides fenced
   renderer in `Markdown.tsx` for the `language-mermaid` fence and mounts a real mermaid SVG. mermaid
   (~157 KB gzip) is loaded with a lazy `import()` **inside the component** (via the existing
   `lib/lazyRetry.ts` chunk-reload wrapper), never a static import — a static import would hoist mermaid
   into a shared chunk across all ~5 `Markdown` call sites and tax first paint on pages with no charts.
   The fence stays a plain code block until mermaid resolves.

2. **PDF gets the *same* mermaid output, rendered once in the browser and spliced in server-side.** When
   `MermaidBlock` renders, it flattens its SVG to cascade-free geometry (computed styles inlined,
   `<style>`/`class`/`<foreignObject>` removed — `web/src/lib/flattenSvg.ts`) and POSTs it to a
   server-side cache keyed by `sha256(user \x00 mermaidVersion \x00 fenceSource)`. On PDF render,
   `renderPDFHTML` (`internal/app/server.go:911` — the **single** choke point both `/report/{id}/pdf` and
   `day_export.go:126` flow through) replaces the fence with the cached, re-sanitized inline SVG. Both PDF
   surfaces are therefore **structurally incapable of diverging**, and the PDF chart is byte-for-byte the
   screen chart (same mermaid.js), not a re-render.

3. **The splice happens *past* the existing sanitizer, via a nonce handshake — `pdfBodyPolicy` and every
   wkhtmltopdf flag stay byte-identical.** goldmark emits an unguessable nonce placeholder where the fence
   was; `sanitizePDFBody` runs on that (the SVG is not present yet, so it never reaches bluemonday); the
   SVG is spliced in afterward. Consequence: the existing `pdf_test.go` SSRF-boundary assertions
   (including `TestSanitizePDFBodyDropsAllFetchCapableAttributes`) and `md_test.go` continue to pass
   verbatim. A body containing literal placeholder-looking text cannot forge a slot (the nonce is
   per-render and server-minted).

4. **The SVG is sanitized by *re-serialization*, not filtering — `svgsan.go`, deny-by-default.** Untrusted
   client-POSTed SVG is parsed to a tree and **our own bytes are re-emitted** from an allowlist of shape
   elements (`g`/`rect`/`path`/`line`/`text`/`polyline`/`circle`/`tspan`/…) and non-URL geometric/paint
   attributes only. The allowlist contains **zero URL-bearing attributes** — no `href`/`xlink:href`,
   `style`, `<use>`, `<image>`, `<foreignObject>`, `<filter>`, `<pattern>`, `on*`, or `url(...)`. Any
   denied token fails **closed** (the chart degrades to a code block; it never emits attacker bytes). DoS
   caps bound series/point count and output size; `NaN`/`Inf`/float values are clamped. Keys are
   **user-scoped**, so the worst case of a novel bypass is an attacker corrupting *their own* PDF.

5. **The cache is per-process in-memory (LRU) for v1 — no schema change.** The client handshake refills
   any miss, so persistence is an optimization, not correctness. Per the CLAUDE.md hard rule, a durable
   cache table is **not** built without first asking how to structure it; it is deferred (ADR 0021 / a
   later ASK). `mermaidVersion` is folded into the key so a mermaid bump cleanly invalidates stale SVGs
   rather than serving a chart from a different renderer than the reading page now shows.

6. **`day.zip` degrades loudly, not silently, on a cold cache (Option B).** The bulk export has no browser
   to warm misses, and building client-side pre-warming for it (~30–60 s of un-Workerable main-thread
   mermaid for a 100-chart day) would be **throwaway** once ADR 0021's server-side renderer lands — which
   is the correct home for "all-server-side day.zip." So a report whose charts were never viewed exports
   with a **labelled** code block (an i18n `t()` string, not a silent drop). The single-report and
   `day.zip` paths still produce byte-identical body HTML for the same warmed report.

7. **Chart rendering is host-core — an explicit, one-time exception to ADR 0002's "never code-in."** ADR
   0002 says extend by API-in / webhook-out / embed-in. Rendering the report body is not an *extension*;
   it is the portal's core job, exactly as markdown and KaTeX already are in-process. This ADR blesses
   chart rendering as host-core on the same footing — **not** a precedent for future code-in features.

8. **Theming follows the app.** `mermaid.initialize` is a module-global singleton while theme is
   document state; `MermaidBlock` observes the host's `data-theme` attribute and re-renders existing
   blocks with mermaid's matching `default` / `dark` theme. This also covers `auto` mode changes without
   coupling the markdown renderer to `PrefsProvider`.

9. **The streaming (SSE) render path is guarded.** `ChatPage` re-invokes `Markdown` per SSE token, so a
   fence is repeatedly seen half-open; `MermaidBlock` guards on a closed fence + `mermaid.parse()`
   try/catch and stays a code block until the fence completes — mermaid must never throw per-frame or
   inject error DOM into the streaming path.

## Consequences

- When the gated PDF phase is complete, both halves of the ask are met on the existing architecture:
  live themed charts on every reading surface, and real (not re-drawn) charts in the single-report PDF
  for any report that was viewed. Image +0 MB, +0 containers, and the PDF integration remains confined
  to the existing `renderPDFHTML` choke point.
- **The PDF is no longer a pure function of the stored report** — it depends on a browser having warmed
  the cache. Every non-browser consumer (machine `/api/v1` clients, and any future webhook / scheduled-
  email export) gets code blocks by construction. This is an **accepted trade of the incremental path**,
  and the reason ADR 0021 exists; it is named here so it is not mistaken for a bug.
- `svgsan.go` is new, hand-rolled attack surface parsing untrusted-derived input for an EOL engine that
  upstream forbids feeding untrusted HTML. Mitigated by deny-by-default re-serialization, user-scoped
  keys, fail-closed, and a URL-free allowlist — but bespoke SVG sanitizers are historically bypass-prone
  (mXSS, namespace confusion, Go-xml-vs-QtWebKit parser differential). It gets the hardest review, and a
  QtWebKit SVG parser bug reachable by an allowlist-conformant `<path d=...>` is a residual an allowlist
  cannot fully rule out — a standing argument for the ADR 0021 migration.
- **One go/no-go probe remains before PDF implementation.** wkhtmltopdf is absent locally, Homebrew
  removed the formula, and no Docker/Podman/VM runtime is installed:
  - **wk-svg probe** — does Qt 4.8 WebKit actually paint an inline `<svg>` with CJK `<text>` under
    `pdf.go:84`'s exact flags? Inference is strong (`--no-images` gates only `AutoLoadImages`; xychart
    emits no `foreignObject`) but unobserved. Run it in a `Dockerfile.release` build **before** Phase 2.
    A reproducible probe is checked in: `docker build -f scripts/Dockerfile.wkhtmltopdf-svg-probe .`.
    It installs the exact release `.deb`, renders SVG and blank controls through the production flags,
    rasterizes both PDFs, and fails if their pixels are identical.
    If it fails, the PDF half is not viable on wkhtmltopdf and both PDF surfaces degrade uniformly to code
    blocks until ADR 0021 lands.
  - **mermaid-eval probe — PASSED (2026-07-17).** Mermaid 11.16.0 rendered a real `xychart-beta` in a
    production Vite build under `spa.go:122`'s exact `script-src 'self'` with no `unsafe-eval`; the chart
    had a `700×500` viewBox, axis title, and expected half-point ticks. Changing `data-theme` re-rendered
    it with dark-theme text (`#e0dfdf`). There were no CSP or mermaid console errors. The build emitted
    native dynamic imports and kept xychart in an on-demand chunk; no CSP weakening is needed.
- **`xychart-beta` content limits (author-side, unfixable by any renderer choice):** *legend* is already
  merged upstream and arrives free on a mermaid bump (because both surfaces use real mermaid); *dual
  y-axis* (the user's RSI/KD-over-price overlay) is genuinely unsupported and no renderer here can add it.
  Report authors should express such overlays as two stacked xychart fences sharing an x-axis, or a single
  normalized-scale plot. This is a Dify-authoring decision, documented so it is not mistaken for a
  rendering bug.
- The flatten pass discards mermaid's `<style>` block; xychart carries inline `fill=`/`stroke=`, but any
  appearance living only in that block shifts between screen and PDF. Requires a visual diff on a real
  K-line before shipping, not a code review.
