# Test Spec: SGC-Library Many-to-Many

Behavioral test spec for the SGC-Library feature (migration 024 + related layers).
Intended for future unit test implementation — not integration tests, though some cases apply to both.

---

## 1. Repository — `ServerGameConfigRepository`

### `AddLibrary`

| # | Behavior | Expected |
|---|----------|----------|
| 1.1 | Add a library to an SGC that has no libraries | Row inserted, no error |
| 1.2 | Add the same library to the same SGC twice | Second call returns nil (ON CONFLICT DO NOTHING) |
| 1.3 | Add a library to a non-existent SGC | Returns error (FK violation) |
| 1.4 | Add a non-existent library to a valid SGC | Returns error (FK violation) |
| 1.5 | `created_at` is populated by the DB default | Timestamp is non-zero after insert |

### `ListLibraries`

| # | Behavior | Expected |
|---|----------|----------|
| 1.6 | SGC with no attached libraries | Returns empty slice (not nil), no error |
| 1.7 | SGC with two libraries attached | Returns both libraries |
| 1.8 | Two SGCs each with different libraries | `ListLibraries(sgc1)` does not include sgc2's libraries |
| 1.9 | Library attached to multiple SGCs | Appears in each SGC's list independently |
| 1.10 | Results are ordered by `library_id` ascending | Order is stable and deterministic |
| 1.11 | Library with a NULL description | `library.Description` is `nil`, not a pointer to empty string |
| 1.12 | SGC is deleted | Cascade removes its rows; `ListLibraries` on another SGC unaffected |
| 1.13 | Library is deleted | Cascade removes the join row; `ListLibraries` for that SGC no longer includes it |

---

## 2. `WorkshopManager.EnsureLibraryAddonsInstalled`

This is the highest-value target for unit tests due to complex branching.
Use a mock for `sgcRepo`, `libraryRepo`, `installationRepo`, and `InstallAddon`.

### Early-exit / no-op cases

| # | Behavior | Expected |
|---|----------|----------|
| 2.1 | SGC has no libraries attached | Returns nil immediately; `InstallAddon` never called |
| 2.2 | SGC has one library with zero addons, no child libraries | Returns nil; `InstallAddon` never called |
| 2.3 | `sgcRepo.ListLibraries` returns error | Returns that error; nothing else called |

### Addon collection — BFS / deduplication

| # | Behavior | Expected |
|---|----------|----------|
| 2.4 | Single library with 3 addons | All 3 addon IDs are collected for install check |
| 2.5 | Library A references library B; B has addons | Addons from both A and B are collected |
| 2.6 | Library A and B both contain the same addon | That addon is collected exactly once (deduplicated) |
| 2.7 | Circular reference: A → B → A | BFS visited set prevents infinite loop; terminates normally |
| 2.8 | Three levels deep (A → B → C, each with addons) | All addons from all three levels collected |
| 2.9 | `libraryRepo.ListAddons` returns error for one library | Logs warning; continues collecting from remaining libraries |
| 2.10 | `libraryRepo.ListReferences` returns error for one library | Logs warning; continues traversal of remaining queue |

### Install triggering

| # | Behavior | Expected |
|---|----------|----------|
| 2.11 | Addon has status `installed` | `InstallAddon` not called for that addon |
| 2.12 | Addon has status `pending` | `InstallAddon` called (not yet terminal) |
| 2.13 | Addon has status `downloading` | `InstallAddon` called (not yet terminal) |
| 2.14 | Addon has status `failed` | `InstallAddon` called (retry) |
| 2.15 | Addon has status `removed` | `InstallAddon` called (treat as needing install) |
| 2.16 | `GetBySGCAndAddon` returns "not found" error | `InstallAddon` called (no existing record) |
| 2.17 | `InstallAddon` returns error for one addon | Logs warning; continues with remaining addons; returns nil |
| 2.18 | All addons already installed | No installs triggered; skips polling; returns nil |

### Polling

| # | Behavior | Expected |
|---|----------|----------|
| 2.19 | All triggered addons reach `installed` before timeout | Returns nil promptly after last addon settles |
| 2.20 | Some triggered addons reach `failed` before timeout | Returns nil (failure is visible in UI, not a blocker) |
| 2.21 | One addon still `downloading` after 90s | Returns nil regardless (timeout is non-blocking) |
| 2.22 | Mixed: one installed, one failed, one downloading at timeout | Returns nil |
| 2.23 | Zero addons triggered (all were already installed) | Polling loop not entered |

---

## 3. Workshop gRPC Handler

### `AddLibraryToSGC`

| # | Behavior | Expected |
|---|----------|----------|
| 3.1 | `sgc_id == 0` | Returns `codes.InvalidArgument` |
| 3.2 | `library_id == 0` | Returns `codes.InvalidArgument` |
| 3.3 | Both IDs valid, `sgcRepo.AddLibrary` succeeds | Returns empty response, no error |
| 3.4 | `sgcRepo.AddLibrary` returns error | Returns `codes.Internal` |

### `ListSGCLibraries`

| # | Behavior | Expected |
|---|----------|----------|
| 3.5 | `sgc_id == 0` | Returns `codes.InvalidArgument` |
| 3.6 | SGC with no libraries | Returns `ListSGCLibrariesResponse{Libraries: []}`, no error |
| 3.7 | Library has nil description | Proto field `description` is `""` (zero value, not panic) |
| 3.8 | `sgcRepo.ListLibraries` returns error | Returns `codes.Internal` |
| 3.9 | Two libraries returned | Response has both; order matches repo result |

---

## 4. Session Handler — `StartSession`

| # | Behavior | Expected |
|---|----------|----------|
| 4.1 | `workshopManager` is non-nil, `EnsureLibraryAddonsInstalled` succeeds | Session start proceeds normally |
| 4.2 | `EnsureLibraryAddonsInstalled` returns an error | Warning is logged; session start **still proceeds** (error is non-fatal) |
| 4.3 | `workshopManager` is nil (e.g., constructed without it) | Nil check prevents panic; session start proceeds normally |
| 4.4 | `EnsureLibraryAddonsInstalled` is called with the SGC ID from the request | Verify the correct `sgcID` is passed, not session ID or server ID |

---

## 5. UI Handlers

### `handleSGCDetail`

| # | Behavior | Expected |
|---|----------|----------|
| 5.1 | Missing or non-numeric SGC ID in URL | 400 Bad Request |
| 5.2 | SGC not found (gRPC 404) | 404 Not Found |
| 5.3 | Server fetch fails | `Server` is nil; page still renders (graceful degradation) |
| 5.4 | Game config fetch fails | `GameConfig` is nil; page still renders |
| 5.5 | `PendingCount` is 0 | Pending banner not shown |
| 5.6 | `PendingCount > 0` | Pending banner shown with correct count |
| 5.7 | No sessions for this SGC | Sessions table shows empty state |
| 5.8 | No libraries attached | Libraries card shows empty state |

### `handleAddLibraryToSGC`

| # | Behavior | Expected |
|---|----------|----------|
| 5.9 | Non-POST request | 405 Method Not Allowed |
| 5.10 | Missing or invalid `sgc_id` | 400 Bad Request |
| 5.11 | Missing or invalid `library_id` | 400 Bad Request |
| 5.12 | Valid inputs, gRPC call succeeds | Redirect to `/sgc/{sgc_id}` |
| 5.13 | Valid inputs, gRPC call fails | 500 Internal Server Error |
| 5.14 | HTMX request | Response uses `HX-Redirect` header instead of 303 redirect |

### `handleSGCAvailableLibraries`

| # | Behavior | Expected |
|---|----------|----------|
| 5.15 | Missing or invalid `sgc_id` | 400 Bad Request |
| 5.16 | Library already attached to the SGC | Not included in available list |
| 5.17 | Library not attached to the SGC | Included in available list |
| 5.18 | Query param `q` matches library name (case-insensitive) | Only matching libraries returned |
| 5.19 | Query param `q` does not match any library | Returns empty list |
| 5.20 | `ListLibraries` gRPC call fails | 500 Internal Server Error |

---

## Notes for Implementers

- **Mocking**: The manager and repo layers accept interfaces everywhere. Prefer hand-written fakes over generated mocks for readability.
- **Polling tests**: Use a fake `time.Sleep` / inject a clock interface so tests don't have a 90s wall time.
- **BFS cycle test** (2.7): Construct mock `ListReferences` responses that would be infinite without the visited set.
- **Template tests** (5.x): Can be validated with `html/template` execution against mock data — no HTTP server needed for the rendering logic; reserve HTTP-level tests for routing and status codes.
- **Priority order**: Cases 2.4–2.23 (EnsureLibraryAddonsInstalled) and 4.1–4.4 (StartSession integration) are highest value since they govern the session pre-flight behavior and are difficult to verify manually.
