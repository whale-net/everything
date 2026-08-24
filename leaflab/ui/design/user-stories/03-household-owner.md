# LeafLab — Household Owner ("Mom & Dad") User Stories

Source material for `leaflab/ui/design/wireframes/`. This is the "regular user"
tier named in screen 08's note — *"Regular users see a trimmed version (fewer
stats, no reboot/config-push buttons). Gawkers never see this page at all."*
They sit between the gawker (screen 12, read-only, no login) and the tinkerer.

## Persona

**Household Owner** — owns the hardware, paid for it, and lives with the plants
it watches. Someone else set it all up: an adult child or a friend drove over,
plugged each board into a laptop, ran the provisioning tool, and left. Mom &
Dad have never seen a terminal. They think in rooms and plants — "the one in
the kitchen", "the fern", "the greenhouse out back" — never in device ids, and
they will never learn that `leaflab-ccdba79f5fac` is the kitchen. They check
the page maybe twice a week, usually because something looks off with a plant,
and once in a while because they were told over the phone to "push the update."

They are the persona that breaks the system's assumptions. They cannot tell a
dead sensor from a dead router from a dead website — all three look like "the
website is broken", and all three get reported to the tinkerer as the same
sentence. They have no vocabulary for degrees of failure, so the UI must do
that classification for them and state it in one plain sentence, including the
case where the honest answer is *"this is our problem, not yours."* Everything
below is scoped to what they can actually act on: look, understand, press one
safe button, and ask for help. Nothing below implies they configure a sensor.

## Epic A — "Show me *my* plants"

1. As a household owner, I want to log in and land on a page showing only my
   own devices and plants, so that "my data" means something and I never see a
   stranger's greenhouse.
   - Acceptance: the landing view is scoped by ownership, not by "all boards."
     A board with no ownership record is invisible to every household owner.
     Today's `ListBoards` returns every board globally with no filter — see
     blocker 1.

2. As a household owner, I want each device labelled by where it is — "Kitchen
   window", "Back greenhouse" — not by an id, so that I can find the thing I
   care about without knowing what a device id is.
   - Acceptance: every device row leads with a human name the owner (or the
     tinkerer on their behalf) chose. The device id appears in exactly one
     place: a collapsed "details for support" block (story 17).

3. As a household owner, I want to rename a device or room myself when we move
   it, so that I don't have to phone someone to fix a label that's now wrong.
   - Acceptance: rename edits the display name only; it never touches sensor
     configuration, region structure, or anything the device runs. There is no
     rename write path anywhere in the system today — see blocker 2.

4. As a household owner, I want to see plants by name and kind — "Fern (Boston
   fern), back shelf" — rather than sensor names, so that the page matches how
   I actually think about the house.
   - Acceptance: readings are presented under the plant they belong to, using
     the plant-at-that-time join that `v_sensor_reading_with_plant` already
     does. `BH1750-A1` is never the primary label on this persona's screens.

## Epic B — "Is everything okay?"

5. As a household owner, I want one sentence at the top of the page telling me
   whether everything is fine, so that a two-second glance is a complete visit
   on a normal day.
   - Acceptance: a single status line with three states — everything is
     reporting / something needs attention / we're having trouble on our end —
     rendered above any table, chart, or per-device detail.

6. As a household owner, when something has stopped reporting, I want to be
   told **which** thing broke in plain words, so that I know whether to check
   the plant, check the router, or wait for someone to fix the website.
   - Acceptance: the UI distinguishes and separately words at least four
     cases: (a) one sensor silent while its board still reports — "The light
     sensor in the kitchen isn't responding; the box itself is fine"; (b) the
     whole board silent — "The kitchen sensor stopped reporting 2 hours ago —
     it may have lost Wi-Fi or power"; (c) every board silent at once — "Your
     internet or our service may be down"; (d) the site itself degraded — "We
     can't reach your sensors right now. This is our problem, not yours."
     Today the only signal is a stale `board.last_seen_at`, and MQTT last-will
     is never persisted, so (a)/(b)/(c) cannot be told apart — blocker 3.

7. As a household owner, I want "stopped reporting" phrased as elapsed time in
   my own timezone, so that I can judge whether it's serious without reading a
   timestamp.
   - Acceptance: staleness renders as "2 hours ago" with an absolute local
     time on hover; never a UTC timestamp as the primary text, and never a raw
     `last_seen_at` column.

8. As a household owner, I want an obviously wrong number to be flagged rather
   than shown as fact, so that I don't repot a healthy fern because a sensor
   glitched.
   - Acceptance: a reading outside its sensor type's plausible range is shown
     as "this reading looks wrong" instead of as a value, and the plant is not
     marked unhealthy on the strength of it. Screen 19 already imagines this
     ("Reported value 99,999 lux"); no evaluator exists — blocker 4.

## Epic C — "How's the fern doing?"

9. As a household owner, I want the current light, temperature, and humidity
   for each plant, with a word saying whether that's good, so that a number I
   don't have a feel for still tells me something.
   - Acceptance: each reading shows value, plain-language unit, and a
     qualitative band (low / fine / high) for that plant type; the band comes
     from `plant_type`, not from anything the owner configures.

10. As a household owner, I want a simple picture of the last day and the last
    week, so that I can see "it's been getting colder in there all week" —
    which is the actual question I have.
    - Acceptance: one chart per measurement with day/week presets only, no
      custom range picker, no sensor-line toggles. Screen 07 is the tinkerer's
      version of this screen; this persona's version drops the filter panel.
      No read path for `sensor_reading` exists in the API — blocker 5.

11. As a household owner, I want the greenhouse's overnight low and daytime
    high called out, so that I can act on the number that actually matters for
    a cold snap without reading a chart.
    - Acceptance: per-region daily min/max/avg summarised in words above the
      chart, derived server-side from `v_sensor_reading_enriched`.

12. As a household owner, I want to be told when it gets too cold or too dark
    without having to remember to check, so that I find out before the plant
    dies rather than after.
    - Acceptance: opt-in notification per plant or per room, with thresholds
      preset from `plant_type` and expressed as "tell me if the greenhouse
      drops below 5 °C" — never as a rule expression. No alert table,
      evaluator, or delivery path exists — blocker 4.

## Epic D — "Push the update"

13. As a household owner, I want one clearly-labelled button that re-sends the
    settings my device is supposed to be running, so that when I'm told over
    the phone to "push the update" I can do it without understanding it.
    - Acceptance: one button per device, labelled in outcome terms ("Re-send
      settings to the Kitchen sensor"). It re-sends the last *accepted*
      configuration for that device and nothing else — it is not a firmware
      update (no OTA exists), it never opens an editor, and it can never
      change what the device is configured to do.

14. As a household owner, I want the button to tell me what happened, in one
    sentence, without me refreshing anything, so that I know whether to call
    for help.
    - Acceptance: three terminal outcomes, each a full sentence — "Done. The
      Kitchen sensor is running the right settings." / "The Kitchen sensor
      hasn't answered yet. We'll keep trying — check back in a few minutes." /
      "That didn't work. We've noted it; here's how to get help." The version
      number is never shown. The API cannot report this today: acks arrive
      over MQTT and the caller must poll `GetDeviceConfig` and compare version
      integers — blocker 6.

15. As a household owner, I want the button to be unavailable, with a reason,
    when pressing it can't help, so that I don't press it four times at a
    device that's simply unplugged.
    - Acceptance: when the device is offline the action is disabled with the
      reason inline — "The Kitchen sensor is offline, so it can't receive
      settings right now" — not a greyed-out button with no explanation, and
      not an error after the fact.

16. As a household owner, I want pressing it twice to be harmless, so that my
    instinct when nothing seems to happen doesn't make things worse.
    - Acceptance: re-sending an unchanged configuration is idempotent and does
      not accumulate rows the owner will later be asked about. `device_config`
      is an append-only log and `PushDeviceConfig` always mints a new version
      — blocker 7.

## Epic E — "Who do I call?"

17. As a household owner, I want a "get help" button that hands over
    everything a helper needs, so that I never have to read an id down the
    phone or describe what I'm seeing.
    - Acceptance: one action produces a support reference and shows the device
      id, board type, and current status in a copyable block — the only place
      in this persona's UI where an id appears.

18. As a household owner, I want to grant a helper temporary permission to look
    at my devices, and to see plainly when that permission ends, so that
    getting help doesn't mean permanently handing over my house.
    - Acceptance: a grant is explicit, time-boxed, visible while active with
      the helper's name and expiry, revocable in one click, and expires on its
      own. No user table, ownership column, or grant record exists in any of
      the 12 migrations — blocker 1.

19. As a household owner, I want to see what a helper did while they had
    access, in plain sentences, so that I can trust the arrangement.
    - Acceptance: an activity list scoped to my devices — "Alex changed the
      Kitchen sensor's settings on 12 March" — with no proto, table, or column
      names in the text.

## Epic F — Signing in

20. As a household owner, I want to sign in the way I sign in to everything
    else, and stay signed in on the tablet in the kitchen, so that checking the
    plants isn't a chore.
    - Acceptance: browser auth-code login (the `libs/go/htmxauth` flow), with a
      long-lived session on a trusted device and a plainly-worded sign-out.
      Sessions survive a browser restart; expiry returns to a login page, never
      to a blank screen or a raw error.

21. As a household owner, I want a page that never shows me a technical error,
    so that a fault on the server side doesn't read to me as "you broke it."
    - Acceptance: no unhandled gRPC status codes, stack traces, or proto field
      names reach this persona's UI. Every failure resolves to one of the
      Epic B sentences.

## Plain-language mapping

| System concept | What this persona is shown |
|---|---|
| `board.device_id` (eFuse MAC-derived) | "Kitchen window" — id only inside the support block (story 17) |
| `board.last_seen_at` stale > threshold | "The Kitchen sensor stopped reporting 2 hours ago" |
| No `sensor_reading` rows for one sensor, board still fresh | "The light sensor in the kitchen isn't responding — the box itself is fine" |
| Every board's `last_seen_at` stale at once | "Your internet or our service may be down — your sensors are probably fine" |
| API / RabbitMQ / DB unreachable | "We can't reach your sensors right now. This is our problem, not yours." |
| `device_config.version` bump + `acked_at` set | "Done. The Kitchen sensor is running the right settings." |
| `device_config.accepted = false` + `rejection_reason` | "That didn't work. We've noted it; here's how to get help." |
| Config pushed, no ack yet | "The Kitchen sensor hasn't answered yet. We'll keep trying." |
| Re-push of last accepted config | "Re-send settings to the Kitchen sensor" |
| `v_region_path` | "Greenhouse › Back shelf" |
| `v_sensor_reading_with_plant` | "Fern (Boston fern), back shelf — 423 lux" |
| `sensor_type` range + `plant_type` preference | "Plenty of light" / "A bit dark for a fern" |
| Reading outside the sensor type's range | "This reading looks wrong — ignoring it for now" |
| SCD2 `valid_from` / `valid_to`, `sensor_name_history` | Nothing. Only the current name is ever shown. |
| `sensor_chip`, I2C address, mux path | Nothing. Never rendered for this persona. |
| Support grant nearing expiry | "Alex can see your sensors until 4:30 today. End now." |

## What this persona must NOT be able to do

- Edit a sensor configuration: no I2C addresses, mux paths, chip types, poll
  intervals, or anything resembling screen 04 or screen 13.
- Add, remove, or provision a device — provisioning requires physically
  plugging the board into a computer running Bazel.
- Reboot a device, or trigger anything destructive or physical.
- Delete or decommission a board, sensor, plant, or region.
- Edit the region tree, or move a sensor between regions.
- See any board, reading, plant, or region they don't own or haven't been
  granted access to — including inside aggregate counts and averages.
- See other households' existence at all: no global totals, no "3 setups"
  stat that counts someone else's hardware.
- Push an arbitrary configuration — the only write is "re-send what's already
  accepted."
- Grant another person permanent access, or grant anyone admin rights.
- Reach admin screens: screen 08's dashboard, screen 11 registration, screen
  18's config diff.

## API surface this persona needs

All of it over HTTP/JSON — this persona is a browser, and there is no
grpc-gateway or grpc-web today (blocker 8). Paths are illustrative.

| Endpoint | Purpose | Auth requirement |
|---|---|---|
| `GET /api/v1/me/overview` | The one-sentence status line + per-place summary | Session; scoped to owned boards |
| `GET /api/v1/me/devices` | My devices by friendly name, with health state | Session; owner-scoped `ListBoards` |
| `GET /api/v1/devices/{id}` | One device: its plants, sensors, current values | Session; owner or active grant |
| `PATCH /api/v1/devices/{id}` | Rename a device / set its room label | Session; owner only |
| `GET /api/v1/devices/{id}/health` | Classified fault: sensor / board / network / us | Session; owner or active grant |
| `GET /api/v1/plants/{id}/readings` | Day and week series for one plant, banded | Session; owner or active grant |
| `GET /api/v1/regions/{id}/summary` | Overnight low, daytime high, averages | Session; owner or active grant |
| `POST /api/v1/devices/{id}/config:resend` | The one button — re-send last accepted config | Session; owner only; idempotent |
| `GET /api/v1/devices/{id}/config/status` | Ack outcome as a state, not a version number | Session; owner or active grant |
| `PUT /api/v1/me/notifications` | Opt in to "tell me if it drops below 5 °C" | Session; owner only |
| `POST /api/v1/support-grants` | Grant a helper time-boxed access | Session; owner only |
| `DELETE /api/v1/support-grants/{id}` | Revoke that access immediately | Session; owner only |
| `GET /api/v1/me/activity` | What a helper did, in plain sentences | Session; owner only |
| `GET /api/v1/system/health` | Distinguishes "our fault" from "your fault" | Public |

## Top blockers

1. **No ownership anywhere.** No user table, no `board.owner_sub`, no
   `board_acl`, no tenant column in any of the 12 migrations, and no auth
   interceptor on the gRPC server at all. `ListBoards` is global and
   unfiltered. Without ownership, every story in Epic A and Epic E is
   meaningless — "my plants" cannot be expressed. `libs/go/grpcauth` and
   `libs/go/htmxauth` supply subject and realm roles only; neither supplies
   ownership. This is the single blocker that gates the whole persona.

2. **No friendly names, and no write path for them.** Boards carry
   `device_id` / `board_id`; nothing stores "Kitchen window". `region` and
   `plant` exist as tables with **no code path at all** — they are populated
   by manual SQL. Stories 2 and 4 need a name to read; story 3 needs a write
   that does not exist.

3. **Faults are indistinguishable.** `board.last_seen_at` is the only
   online/offline proxy and MQTT last-will is never persisted, so "sensor
   broken", "board offline", "Wi-Fi down", and "our service is down" all
   collapse into one stale timestamp. Epic B's whole premise — telling this
   persona *which* thing broke — cannot be built on that alone.

4. **Alerts do not exist.** No table, no RPC, no evaluator, no delivery
   channel, no range validation. Stories 8 and 12 have nothing to sit on;
   screen 19 is entirely aspirational.

5. **No readings read path.** `sensor_reading` is a populated Timescale
   hypertable and seven analytical views already exist — and the API reads
   none of them. Every number on every screen in Epic C is unreachable today.

6. **Config outcome is not observable through the API.** Acks arrive over
   MQTT; the API never learns them. A caller must poll `GetDeviceConfig` and
   compare version integers to guess. `device_config` already carries
   `accepted` / `acked_at` / `rejection_reason` — they simply aren't exposed,
   so story 14 needs a status read path rather than new plumbing.

7. **`PushDeviceConfig` is not idempotent and has no "re-send" mode.** It
   takes a full sensor list and mints a new version on every call against an
   append-only log. The one safe button (Epic D) needs a distinct server-side
   "re-send last accepted, no-op if unchanged" operation.

8. **A browser cannot call this API.** gRPC on :50051 with reflection on, no
   HTTP/JSON, no grpc-gateway, no grpc-web. Every story here presupposes a
   transport that does not exist.

9. **No support/consent primitive.** Time-boxed grants, revocation, expiry,
   and a per-owner activity log are all new — and they depend on blocker 1,
   since a grant is an exception to an ownership rule nobody has written yet.

10. **No structured errors.** Everything surfaces as a raw gRPC status. Story
    21 needs error classes the UI can map to sentences, not codes it has to
    guess the meaning of.
