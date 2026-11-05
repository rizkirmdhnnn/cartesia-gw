# PRODUCT.md

## What this is

`cartesia-gw` is a single-binary gateway that puts an OpenAI-compatible
`/v1/audio/speech` endpoint in front of the Cartesia text-to-speech API. It is
built and run by one operator (internal or self-hosted) on a small VPS, usually
behind aaPanel + Nginx or Caddy, inside a Docker container.

## Who uses it, and where

- **The operator** (the customer): a developer or small-team lead who owns a few
  Cartesia API keys and needs one stable endpoint for their apps (n8n workflows,
  bots, products). They check it a few times a day from a laptop browser, often
  at night after a deploy. They need to answer three questions fast: *is it
  serving, which key is being used right now, and what is failing?*
- **The clients**: programs, never people. They authenticate with a client key
  (`dk_…`) and call `/v1/*`.

## The job the dashboard does

1. Prove the gateway is alive and how traffic is distributed across upstream
   Cartesia keys (including which key the rotation strategy will use next).
2. Let the operator add/rotate/disable Cartesia keys and choose the rotation
   strategy (LRU, round-robin, random, sticky, priority).
3. Issue and revoke client keys for the programs that call it.
4. Audit what happened: every request's model, voice, characters, key, status,
   latency, and error.
5. Let the operator try a voice end-to-end (Synthesize page) with streaming
   playback and a decodable download.
6. Protect all of the above with one operator account and force a password
   change off the shipped default.

## Truth and constraints

- Single operator, single tenant. No roles, no teams, no multi-user UI.
- The UI is embedded in the Go binary (no build step, no CDN). Any redesign must
  stay hand-written HTML/CSS/vanilla JS.
- Palette constraint from the product owner: **only black and `#D4F36A`** as
  chromatic colours (neutral greys allowed as black at reduced opacity).
- Runs on modest hardware; avoid heavy assets, avoid client-side frameworks.
- Copy is Indonesian, terse, and technical: it names real API fields and real
  failure modes. Never marketing language inside the console.
- Data shown is real request history; never invent usage figures in the UI.

## Surfaces

| Surface | Mode | Purpose |
|---|---|---|
| Console (six pages: Overview, Synthesize, Riwayat, Cartesia Keys, Client Keys, Akun) | Operate | Run and audit the gateway |
| Login gate (sign in, forced password change) | Operate | Access control with a clear first-run path |
| README / docs | Read | Deploy and understand |

## What "good" looks like

The operator opens the console and in under five seconds knows: requests are
flowing, which key they land on, whether anything is parked in cooldown, and
whether a client is being refused. Nothing decorative competes with those facts.
