# Capability map

Part of the [LeafLab product brief](../PRODUCT.md). One line per capability, phrased as *a persona can do a thing*. Capability ids are permanent — an FR in a milestone plan cites the `Cn` it serves, so numbers are never reused or renumbered. New capabilities are appended to `Later` with the next free number.

## Now — already working end-to-end (baseline; see M0)

- **C1** — A board owner's board runs unattended for months, publishing readings that land in the database whenever the board and the broker can reach each other. *(A statement about board uptime, not data durability: there is no device-side buffering, so a network or broker outage loses that window rather than backfilling it.)*
- **C2** — An operator can push a whole-board config that names, enables, disables, and sets the poll interval of that board's sensors without reflashing it. *(The push replaces the board's entire sensor complement — see LB3.)*
- **C3** — An operator can query a reading alongside the sensor, board, and the location that sensor was in at the moment the reading was recorded. *(Placement is point-in-time; names resolve to their current values, not the names in use at the time.)*
- **C4** — An operator can rename a sensor without breaking the continuity of that sensor's reading history.

## Next — the custom CRUD UI

- **C5** — A user can sign in to LeafLab and stay signed in across sessions, using the auth library already shared in the monorepo.
- **C6** — A user can see every board and whether each one is currently reporting.
- **C7** — A user can open a board and see its sensors with each sensor's latest reading.
- **C8** — A user can see one sensor's readings over a time range they choose.
- **C9** — A user can rename a board or a sensor from the UI without losing its reading history.
- **C10** — A user can create, rename, and nest the locations that describe their space, and see roll-ups reflect where things were at the time rather than where they are now.
- **C11** — A user can place a sensor in a location and move it later, and every reading stays attributed to the location that sensor was in when the reading was recorded.
- **C12** — A user can record a plant with a type and a location, as an entity distinct from the location it sits in.
- **C13** — A user can move a plant between locations and still see which readings were taken where that plant was living at the time.
- **C14** — A tinkerer can add a sensor to an existing board and have it show up in the UI without reflashing.
- **C15** — A user can edit, move, rename, and reconfigure the boards, sensors, locations, and plants they own, and cannot change anyone else's; everything is readable by every signed-in user.
- **C19** — A tinkerer can rewire a sensor to different hardware and keep that sensor's reading history continuous, with the hardware change recorded.
- **C21** — A user can record and update the location a board physically sits in, as bookkeeping that never drives a reading's attribution.
- **C24** — A signed-in user can claim an unowned board and become its owner. *(Whether the claim challenge is the board's device ID, a claim code, or something else is a design-round choice, not a product one — the product requirement is that the person holding the board can do it themselves, with no operator step.)*

## Later

- **C16** — A board owner can flip a physical switch to put a board into provisioning mode and tell which mode it's in from the onboard LED.
- **C17** — A board owner can enter Wi-Fi credentials and connection secrets on a page the board itself serves, without ever flashing firmware.
- **C18** — A tinkerer can lay out a board's sensors visually to match how the hardware is physically wired.
- **C20** — A board owner can be notified when a sensor's reading crosses a threshold they set.
- **C22** — An operator can scope reads so a user sees only their own boards, sensors, plants, and readings. *(Deliberately not wanted at ~10 mutually-trusting users — recorded so it is not re-litigated each milestone.)*
- **C23** — An admin can grant and revoke a LeafLab role for another user from the UI. *(Until then, grants are seeded operationally.)*
- **C25** — An owner can release a board they own so the next person to hold it can claim it. *(Until then, a re-hand-off is an operational `UPDATE`. Deliberately not in M2: the first claim is what unblocks the milestone; the second one is a hand-off that has not happened yet at ~10 users.)*
