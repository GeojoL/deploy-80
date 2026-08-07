# Changelog

## 1.1.0a — 2026-08-07

Unreviewed release candidate for GeojoLu acceptance.

- Add an exact, read-only `:8082` → `:80` merge preview and require uppercase `M` to apply its one-time token.
- Apply frameworks, labels, artists, songs, variant backfills, audit state, and safe sequence repairs through one guarded production transaction.
- Reject source drift, target-plan drift, duplicate business keys, missing/self/cyclic variants, invalid dates, and unsafe or missing sequence configuration.
- Stream merge and deployment state from durable remote event files without fixed-interval network polling; timers animate only local UI surfaces.
- Keep collection control and task termination in the same Go tool, while disabling the unsafe legacy shell merge path.
- Display complete exact counts with thousands separators and preserve zero versus unknown values.
