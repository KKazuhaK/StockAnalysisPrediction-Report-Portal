# ADR 0021 — PDF engine migration evaluation (retire wkhtmltopdf; one renderer for screen and PDF)

**Status: evaluation / proposed.** This ADR frames a bet, not a shipped decision. ADR 0020 ships
reading-page charts and defines a separately gated PDF bridge on the current engine; this ADR evaluates
replacing that engine. Nothing here is built until the bet is explicitly accepted.

## Context

Three findings converged to force this evaluation:

1. **wkhtmltopdf is end-of-life.** It uses a patched Qt 4.8 WebKit (a ~2012 engine), is archived upstream,
   and Homebrew has **removed the formula and cask entirely** — verified: `brew install --cask
   wkhtmltopdf` now errors "No Cask with this name exists", making the "want it locally? install it" hint
   in `server.go:937` a dead instruction. Upstream explicitly warns against feeding it untrusted HTML,
   which is exactly what the portal does (Dify-ingested bodies).

2. **The portal has two divergent markdown pipelines and they have already drifted.** react-markdown +
   remark-math + rehype-katex in the browser vs goldmark GFM-only in Go (`md.go:13`). Consequence, found
   during ADR 0020's work: **KaTeX math has been silently broken in PDF export since math shipped** — the
   server pipeline never had a math extension, so `$$...$$` reaches the PDF as literal text. ADR 0020's
   chart splice is, honestly, a *second* patch bridging the same gap rather than closing it.

3. **"Render mermaid server-side" — the user's stated preference for `day.zip` and the only path that also
   serves non-browser consumers — is physically "run a headless browser."** mermaid needs a real DOM;
   there is no DOM-free renderer that is not either Chromium-in-disguise (mermaid-cli/Kroki: 369 MiB /
   442+1145 MiB, over the 256 MiB cap and the 90 s timeout — see ADR 0020) or a wrong-output pure-Go
   reimplementation. So "all server-side charts" and "replace the PDF engine with a real browser" are the
   same project.

ADR 0020's planned incremental path is intended to satisfy "charts in both surfaces," but at a named
cost: the PDF is not a pure function of the stored report (it needs a browser to warm the cache), so
machine-API / webhook / scheduled-email consumers get code blocks forever, `day.zip` degrades on a cold
cache, math stays broken in PDF, and a hand-rolled SVG sanitizer feeds an EOL engine. Its PDF phase is
still gated on an engine probe. This ADR evaluates paying those costs down at once.

## Options under evaluation

**Option H — headless-Chrome sidecar rendering the real SPA.** A `chromedp/headless-shell` sidecar
(~143 MiB compressed, measured — vs 663 MiB gotenberg, 1188 MiB browserless) navigates to a token-gated
print variant of the actual React SPA and prints to PDF. One renderer for screen and PDF **by
construction**: mermaid, KaTeX math, and every diagram type work identically to the screen, the goldmark
pipeline / `pdf.html` / `sanitizePDFBody` / the ADR 0020 splice + `svgsan` all **delete**, and `day.zip`
plus machine consumers render server-side with no client warming. This is the "all server-side" endgame.

**Option W — WeasyPrint (or similar HTML/CSS-to-PDF, no JS).** Fixes math (server-rendered MathML/SVG)
and gives clean CSS pagination off the EOL engine, at a fraction of Chrome's footprint. **But WeasyPrint
does not run JavaScript**, so it cannot run mermaid.js — it can only render a *pre-made* SVG. It therefore
does **not** deliver server-side mermaid on its own; it would still consume ADR 0020's client-rendered,
flattened SVG. Viable as a math-and-typography upgrade that keeps ADR 0020's chart mechanism, not as the
"all server-side charts" answer.

## Decisions (to be ratified before any code)

1. **The endgame target is Option H** — it is the only option that renders mermaid server-side and
   collapses the two pipelines into one. Option W is recorded as a lighter fallback that fixes math but
   leaves charts on the ADR 0020 client-render mechanism.

2. **ADR 0020 is forward-compatible with either target and blocks neither.** Its flattened, cascade-free
   SVG renders fine in Chrome and in WeasyPrint; adopting a server engine later *removes* the client
   splice rather than fighting it. Shipping 0020 now is not wasted work against 0021.

3. **The two disqualifiers that kept Option H out of ADR 0020 are the evaluation's gating criteria** — the
   bet is accepted only if both are answered:
   - **SSRF.** A server-side, network-capable Chromium rendering untrusted Dify markdown under the SPA's
     `img-src 'self' data: blob: https: http:` CSP (`spa.go:123`) is a textbook SSRF/metadata-exfil
     surface (`![](http://169.254.169.254/...)`). Mitigation to prove out: an isolated egress-denied
     network for the sidecar, a print-CSP that forbids remote fetches, the CDP port never exposed, and a
     token-gated print route. The residual tension — the sidecar must still reach the portal on the
     internal network — must be closed, not hand-waved.
   - **Ops.** CDP is unauthenticated by design, so one compose mistake can expose browser/RCE inside the
     trust boundary. The CDP port must be structurally unpublishable rather than relying on an operator
     to remember a firewall rule. Non-Docker self-hosters also lose PDF unless a graceful "no sidecar →
     degrade" path exists. Both must have a concrete answer.

4. **Deploy posture must respect the maintainer's size discipline.** A permanent Chromium container is a
   real cost against a project that stripped a 215 MB font as "overkill." The evaluation must show the
   sidecar is optional (portal degrades cleanly without it), isolated, and memory-bounded separately from
   the 256 MiB portal container — not bundled into the portal image.

## Consequences

- If accepted: math renders in PDF for the first time; charts and all diagram types render server-side for
  every consumer including machine API and `day.zip`; the EOL engine, the second markdown pipeline,
  `pdf.html`, `sanitizePDFBody`, and the ADR 0020 `svgsan`/splice are all retired. Cost: a permanent,
  isolated renderer container, a new auth/isolation surface to get right, and RAM the size discipline
  resists.
- If deferred after ADR 0020's PDF gate passes: its client-render bridge stands as the durable answer;
  the named limits (non-pure PDF, cold-cache `day.zip`, broken PDF math, hand-rolled sanitizer) persist
  as known, documented trades. If the gate fails, PDF charts continue to degrade to code blocks.
- Either way this ADR ends the current invisibility of the two-pipeline problem: the divergence is now a
  tracked architectural decision with a chosen endgame, not silent drift.
- A durable chart/asset cache table (deferred by ADR 0020) is revisited here if Option H does not
  subsume it — and, per the CLAUDE.md hard rule, only after explicitly asking how to structure it.
