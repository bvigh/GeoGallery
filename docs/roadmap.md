# GeoGallery roadmap

As of 2026-11-16. This replaces the phase list in `PRD.md`. Every item has a GitHub issue, under a milestone of the same name. The issue holds the details; this page holds the order and the reasons for it. Items marked **design** need a short design note (like `docs/sharing-albums.md`) before they can be estimated.

Sizes: **XS** < 2 h, **S** ≤ 1 day, **M** 2–4 days, **L** a week or more.

## Where the PRD phases stand

- **Phase 1 (MLP):** done.
- **Phase 2 (sharing with friends):** done, beyond its scope: invite-only OIDC instead of a shared password, albums with members instead of a read-only view, a layout that works on phones.
- **Phase 3 (public release):** partly done. Marker clustering and cursor pagination (#36) are in. The rest is under "Public release" below.
- **Phase 4 (hosted multi-user):** multi-tenancy and social login (through the IdP) are done. Billing is not planned.
- **F6.R1 (trip GPS track overlay):** not built; it is #32.

## Ranking rules

1. Bugs that get in the way of daily use come first. They are cheap.
2. Metadata correctness next. It is PRD priority 1, and a wrong time or place spreads into trips, interpolation and the game.
3. Then browsing (PRD priority 2), then the game (priority 3).
4. New social features after that.
5. Heavy media processing last, each with a design note first.

## M1: Papercuts

Mostly frontend. Small enough to ship as two or three bundled PRs.

| Issue | Item | Size |
|---|---|---|
| #11 | Keep the current-item (green) marker above the other markers | XS |
| #12 | Escape in the album dialog closes the whole trip gallery | S |
| #13 | Space does not toggle a video after clicking play or pause | S |
| #14 | Toggle mute with the M key | XS |
| #15 | Open the 360° view and keyframe editor from the Trips and Albums galleries | S |
| #16 | Open the Game's guess map on the region of the filtered media | S |
| #17 | Highlight the last viewed trip or album | S |
| #18 | Jump to a position or date inside a trip or album gallery | S |
| #19 | Filter panel: "Select all matching" and "Clear" | S |
| #20 | Integration test: a manual location re-places nearby interpolated items | XS |
| #21 | Fix `npm run lint` | S |

#20 answers an open question. Setting a location by hand does move nearby items, by design. The new manual track point re-places every interpolated item of the owner within `GPS_MATCH_WINDOW_HOURS`. Items with an EXIF or manual location are left alone. An integration test now pins it (`media_location_integration_test.go`).

## M2: Time and place correctness

| Issue | Item | Size |
|---|---|---|
| #22 | Matcher: use KML and GeoJSON points only when no recorded source is in range | S |
| #23 | Track parsers: skip far-future points, decide on times without an offset | S |
| #24 | Per-user camera clock rules, defined in Settings, and port `server retime` | M |
| #25 | Per-user camera time zone, with an override per upload batch | M |
| #26 | Edit and shift capture times in the UI | M |
| #27 | Let the owner mark an item as 360° or flat | M |
| #28 | Make 360° auto-detection more reliable | S |
| #29 | Spike: does GPS survive uploads from a phone? | S |
| #30 | Capture time from the filename as a last fallback | S |
| #61 | "Why here?": explain one item's position and capture time | L |
| #62 | Checks page: find suspect track files, formats and items | M |
| #63 | Clock check: test camera clocks against GPS data | L |

#61, #62, #32 and #63 are the four steps of `docs/gps-debugging.md`, in that order. That note also keeps everything the design left for later.

#24 grew beyond its issue: instead of one built-in rule for the Insta360 ONE RS in 2022, each user defines rules for their own cameras in Settings (a make and model, the UTC offset its clock showed, an optional period), and `server retime` applies them to stored items. Make-wide facts stay in `backend/camera-rules.json`.

360° is the owner's decision (#27). Ingest still makes a first guess, and #28 makes that guess better for new files, but a manual choice always wins.

Data repairs on the existing deployments are operator work, not issues: the far dashcam rows, the Insta360 photos and videos, and checking Hetzner. The esoko week rollover is repaired, by correcting the two source logs and importing them again. They are tracked in local notes kept out of this repository, because they describe personal location history.

## M3: Browsing and organisation

| Issue | Item | Size |
|---|---|---|
| #31 | Keep navigation state in the URL | M |
| #32 | Trip GPS track overlay (PRD F6.R1), scope in `docs/gps-debugging.md` Step 3 | M |
| #33 | Keyboard navigation in searchable lists | M |
| #34 | Labels | M, design |
| #35 | Search by name, date, label and place | L, design |
| #36 | Cursor pagination for `GET /api/media` | M |
| #37 | Comments on media | M, design |
| #38 | Default mute setting for videos | S |

## M4: Sharing and social

| Issue | Item | Size |
|---|---|---|
| #39 | Game challenge: five fixed items with high scores | M |
| #40 | Album members suggest a location, the owner approves | M, design |
| #41 | Album members suggest a 360° view and keyframes | S |
| #42 | Admin: storage use per user, trip and album | M |
| #43 | Share media by link | L, design |
| #44 | No-login demo | M, design |
| #81 | Challenge dialog: offer only the albums every chosen player is in | S |
| #82 | Challenge results: open a round's photo or video full size | M |
| #83 | Challenge pool: only items in albums every player is in | S, design |
| #84 | Challenge dialog: show how many items every player can see before creating | M |
| #85 | Game: spread free rounds over places and periods like a challenge's | M, design |
| #86 | Challenge draw: keep an item's use count after its challenge is deleted | S, design |
| #87 | Challenge results: spread pins that land on one spot | S |

#39 is built for people with an account: a challenge's players are picked from the user list, its five items are drawn from what all of them can already see, and its link (`/game/<id>`) opens it for them alone. Its last point, challenges for people without an account, is share links (#43) and waits for that decision.

#81 to #87 are what #39's pull request (#80) left. #81 and #83 are one rule seen from the dialog and from the server; #83 closes the limit #80 accepted. #86 waits for evidence that deleting finished challenges brings repeats. #82 is built: the results open a round's photo or video full size.

## Public release

What is left of PRD Phase 3.

| Issue | Item | Size |
|---|---|---|
| #45 | Refresh the README and add contribution guidelines | S |
| #46 | Publish container images | M |
| #47 | Continuous integration | M |
| #48 | Version the API under `/api/v1` | M |
| #49 | Presigned URLs from docker compose point to an internal host | S |

## Ideas

Each needs a design note before it can be estimated.

| Issue | Item |
|---|---|
| #50 | Explore motion photos (Samsung Motion Photos, Apple Live Photos) |
| #51 | Rotate media without changing the file (the first step of media editing) |
| #52 | Subtitles for video audio |
| #53 | Help choose the best photo among near-duplicates |
| #54 | Face recognition (parked: biometric data) |
| #55 | Trip video |

## Dependencies

- URL state (#31) → challenge links (#39, built), share links (#43) → challenges for people without an account. It did not absorb #17: a tab switch opens a new page entry, so the last viewed trip or album is kept in `sessionStorage` instead.
- `retime` port (#24) → editing capture times (#26).
- Per-user settings (#25) → default mute (#38).
- Labels (#34) → search (#35).
- Location suggestions (#40) → keyframe suggestions (#41).
- Albums in the challenge dialog (#81, built) and the pool's album rule (#83) go together; the count before creating (#84) follows the pool's rule.
- Lint fix (#21) → CI (#47).
- Track overlay (#32) → trip video (#55), perhaps.
- "Why here?" (#61) → Checks page (#62), track overlay (#32), clock check (#63).
- Clock check (#63) finds what #24 and #26 repair.

## Next PRs

1. #11, #12, #20: map and gallery fixes.
2. #13, #14: video keys.
3. #15: the view editor in the galleries.
4. #22, #23: matcher and parser guards.
5. #24: per-user camera clock rules and the `retime` port, then the local repair with `server retime`.
6. #61: "Why here?", as one pull request. Its design planned four, but the parts share files.
