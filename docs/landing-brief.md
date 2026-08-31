# OmniHarness landing page — build brief

Everything needed to build the site later. Nothing here is built yet.

## 1. Positioning

**One line:** The agent harness built for OmniRoute.

**The wedge:** People who run OmniRoute find generic coding agents slow against it — those tools were built for one provider and bolt routing on afterward. OmniHarness inverts that: OmniRoute is the execution layer, everything above it is native to that model. Route once; plan, build, research, or turn a parallel swarm loose.

**Audience:** developers already using OmniRoute (or evaluating it) who live in a terminal and want an agent that is fast on their gateway, transparent about what it does, and genuinely autonomous when told to be.

**Proof points (all real, shipped):**
- One `provider/model` intent out; routing / quota / failover / provider translation stay in OmniRoute. The only integration point is `internal/gateway` — swap it and the whole test suite runs offline.
- Four working modes on `Ctrl+E`: plan · build · research · crazy.
- Permission axis on `Shift+Tab`, independent of mode: manual · accept edits · bypass.
- CRAZY mode fans an independent plan across parallel worker agents (the swarm rail).
- Premium terminal: native-scrollback history, tear-free streaming (DECSET 2026), route ribbon (which provider answered), per-model context meter, per-tool cards with diffs, scoped-trust approvals, input queued during a run, OSC 52 copy / OSC 9 notify.
- Ships as `omniharness-cli` on npm; Go core + headless CLI in the same repo. Web + desktop front ends are roadmap.

## 2. Page structure (single scroll page)

1. **Hero** — wordmark, one-liner, the animated demo SVG (`.github/assets/omniharness-demo.svg`), primary CTA `npm i -g omniharness-cli` (click-to-copy) + secondary "View on GitHub". Sub-line: "Go core · TypeScript TUI · MIT".
2. **Why** — the wedge, 2–3 sentences, plus the `provider/model` boundary diagram (reuse the ASCII diagram from the README or redraw as SVG).
3. **Modes** — 4 cards (plan / build / research / crazy) with the one-line description + a tiny visual (mode-accent border matching the TUI: plan=blue, build=green, research=teal, crazy=red). Note the `Shift+Tab` permission axis below.
4. **The swarm** — the CRAZY differentiator. Short copy + a still or looping clip of the swarm rail (frame 4 of the demo SVG). "One transcript, many status lines."
5. **The terminal** — a scannable grid of the premium features (6–8 items, one line each) with the "everything on screen makes agent intent, action, and history legible" framing.
6. **Install / quickstart** — the npm one-liner, the three env vars, `omniharness doctor` / `models`. Keep it copy-pasteable.
7. **Architecture** — the two-front-ends-over-one-core diagram; link to `docs/architecture.md`. One paragraph.
8. **Footer** — npm, GitHub, license, "web & desktop coming".

## 3. Design system

Pull straight from `npm/src/ui/palette.ts` (dark truecolor set) so the site and the TUI read as one product:

| token | hex | use |
|---|---|---|
| ground | `#0e1016` | page background |
| surface | `#14171f` | cards, code blocks |
| hairline | `#262b38` | borders |
| ink | `#c8cdd9` | body text |
| muted | `#8b93a7` | secondary text |
| **accent (teal)** | `#2dd4bf` | primary accent, links, CTA |
| info (blue) | `#56b6ff` | user / input |
| success (green) | `#8fd66f` | build mode, done states |
| warn (amber) | `#e6b955` | accept-edits, fallback |
| error (red) | `#f2637e` | crazy mode, bypass, swarm |

- **Dark-first**, single visual world (the terminal). A light theme is optional, not required — if added, derive from `LIGHT_TRUE` in the same file.
- **Type:** UI-monospace for anything that represents the TUI (hero, code, mode/perm chips); a clean humanist sans for prose (system stack or one Google font — Inter is fine here since the rest of the page is deliberately terminal-flavoured, or pick something with more character). One display weight, restrained.
- **Motion:** the hero demo already loops. Elsewhere: scroll-reveal at most, nothing that competes with the demo. Respect `prefers-reduced-motion`.
- **Chrome language:** rounded 1px borders in the relevant accent colour, matching the TUI's `borderStyle="round"` panels. No drop shadows beyond a faint window shadow on the hero.

## 4. Assets on hand

- `.github/assets/omniharness-demo.svg` — animated 4-frame terminal cast (idle → Shift+Tab to bypass / Ctrl+E to crazy → planning → swarm done). Self-contained, inline fills + SMIL. This is the hero.
- Real TUI reference frames: run the capture in the repo (`node --test` harness renders `<TerminalInterface>` to a fake stdout) — see the `swarm-integration` / `modes` tests for the pattern.
- `docs/architecture.md` — topology + package layout + the integration-boundary rationale.
- README copy — reusable for sections 2, 5, 7.

## 5. Tech constraints / choices (to decide)

- **Host:** GitHub Pages from `/docs` or a `gh-pages` branch is the zero-infra option; Vercel/Netlify if a framework is wanted. Static either way.
- **Stack:** plain HTML + one CSS file is enough for a single scroll page and keeps it fast. Astro if component reuse or MDX is wanted. No SPA framework needed.
- **Domain:** none yet — decide (omniharness.dev? subpath on an OmniRoute domain?). `homepageUrl` currently points at the npm page.
- **Analytics:** privacy-preserving only, or none.
- **The npm version badge / install string** should read from `omniharness-cli` at build time so it never goes stale.

## 6. Open questions for the owner

- Domain + hosting preference.
- Is there OmniRoute brand guidance (logo, wordmark, colours) the site should align to, or is the OmniHarness teal palette the identity?
- Light theme: yes/no.
- Should the site link a hosted playground / asciinema, or is the animated SVG enough for v1?
- Roadmap section for web/desktop — tease it, or leave it to the footer line?
