# DESIGN.md — Lab Sheet

The console is an operator's lab sheet: white paper, black ink, one lime marker.
Every surface is the page itself; structure comes from ruled lines, not from
cards. It is deliberately not a SaaS dashboard: no tinted canvas, no soft card
shadows, no eyebrow above a heading.

## Colour

Only two chromatic colours exist. Everything else is black at an alpha step, so
the sheet reads as one material.

| Token | Value | Use |
|---|---|---|
| `--paper` | `#FFFFFF` | The sheet, every panel, every input. |
| `--ink` | `#0A0A0A` | Type, rules, filled buttons, the gateway block. |
| `--ink-80/64/48/32/22/14/08` | black at 80…8% | Body, secondary, labels, placeholders, hairlines. |
| `--lime` | `#D4F36A` | The marker: live dot, active nav edge, "next key" tag, OK pills, KPI highlight, traffic edges, focus underline, login card shadow. |

Rules for lime: it marks *what matters now* (live state, the next pick, a
success), never decoration. A surface never gets a lime fill unless it is a
state (active pill, selected voice row wash `#F3FBCE`, secret reveal).

Contrast: `--ink` on `--paper` is 19:1; `--ink-48` is the floor for labels (4.6:1).

## Type

| Role | Face | Size / tracking |
|---|---|---|
| Display (page titles, panel titles, KPI numbers) | Bitter (slab serif) 700 | clamp(30–44px) titles, -0.025em; 19px panel; 22–30px KPI |
| Body | Archivo 400/500/600 | 13–14px, 1.5–1.55 |
| Identifiers, measurements, micro-labels | JetBrains Mono | 11–12px, 0.08–0.14em uppercase tracking for labels |

Labels are uppercase mono **only** when they are short (a field name, a badge),
and never below 11px. Body copy is always sentence case.

All three faces load from Google Fonts and fall back to Rockwell/Georgia,
Helvetica/Arial, Consolas respectively — the page still reads correctly offline.

## Rules, not cards

- A panel has no border, no radius, no background: its title sits on a 2px
  black rule, its table on a 2px top rule with 1px row hairlines.
- The KPI strip is a ledger: one row divided by vertical hairlines, numbers in
  the display face, labels in mono caps.
- The traffic map is a diagram plate: hairline graph paper, a `fig. 01` caption,
  nodes as ruled boxes, the gateway as an inverted black block, edges in olive
  (`#8F9A5B`) at width proportional to share.
- Controls are square-cornered ruled boxes; the primary button is black and
  inverts to lime on hover.
- State is carried by inversion and line style, not hue: `ok` = lime fill,
  `warn` = dashed outline, `off` = 45% opacity, `next` = 2px border with a lime
  offset shadow.

## Motion

One authored moment per interaction: pages fade in 3px over 160ms, edges travel
their dash, the hub breathes with a lime ring. The stream meter is transform-only
and moves on real bytes. `prefers-reduced-motion` stops the dash, the hub and the
page fade.

## Browser surfaces

Selection is lime; focus is a 2px black ring plus a lime inset underline on
inputs; scrollbars are thin black-at-22%; numbers in tables are tabular.
