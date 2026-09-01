# OmniHarness landing page

Static HTML/CSS/JS — no build step. Vercel project root = this `landing/` directory.

## Dev

```
npx serve .        # or: python -m http.server 3000
```

In Claude Code, run `preview_start` with name `landing`.

## Design tokens

All CSS custom properties live in the `:root` block near the top of `index.html`.
Pull from `npm/src/ui/palette.ts` to keep site and TUI in sync:

| var        | dark hex  | role                         |
|------------|-----------|------------------------------|
| --accent   | #2dd4bf   | teal — primary brand         |
| --info     | #56b6ff   | blue — user/input            |
| --success  | #8fd66f   | green — build mode / done    |
| --warn     | #e6b955   | amber — accept-edits         |
| --error    | #f2637e   | red — crazy mode / bypass    |

Light theme is the `[data-theme=light]` block. OmniRoute brand shift is `[data-brand=omniroute]`.

## WebGL

The hero uses Three.js `0.161.0` loaded from `esm.sh`. The particle network (`N=170` nodes)
animates on a Three.js scene rendered into `#bg-canvas`.
To swap accent color for theme/brand changes: `accentColor()` reads `--accent` from CSS each frame.

## Sections

| id             | what it is                              |
|----------------|-----------------------------------------|
| `#hero`        | WebGL canvas + CTA + demo SVG           |
| `#why`         | Wedge copy + architecture pre           |
| `#modes`       | 4 mode cards + perm-axis note           |
| `#swarm`       | CRAZY differentiator                    |
| `#terminal`    | 8-feature grid                          |
| `#install`     | 5-step quickstart                       |
| `#playground`  | Terminal typewriter demo + asciinema CTA|
| `#architecture`| Repo tree diagram                       |
| `#coming`      | Web/desktop tease + notify form         |

## Adding asciinema

1. Record a session: `asciinema rec demo.cast`
2. Upload to asciinema.org and get the embed URL
3. Replace the `<a href="https://asciinema.org" ...>` in `#playground` with:
   ```html
   <script src="https://asciinema.org/a/<ID>.js" id="asciicast-<ID>" async></script>
   ```
4. Or use the self-hosted `asciinema-player` npm package with a local `.cast` file.

## Deployment (Vercel)

1. In Vercel dashboard: create new project → import the GitHub repo
2. Set **Root Directory** = `landing`
3. Framework preset = **Other** (no build command)
4. Deploy — that's it. Auto-deploys on push to `main`.

To update the version badge: search `ver-badge` in index.html and bump the text.
The npm badge could also be fetched at build time via Vercel edge middleware if needed later.
