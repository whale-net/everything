# Rejected alternatives (issue #558)

| Approach | Why rejected |
|---|---|
| `AssertApps` upsert alone, keeping `app`'s mutable metadata | Fixes ordering 1 only, and forces the divergent-ref trade (#547) instead of removing it: some ref must author mutable metadata, and every choice of ref is wrong. Moving metadata to per-build snapshots deletes the question. |
| Infer the missing image artifact from the chart lockfile at chart-record time | Makes the registry *reconstruct* a record it never observed, so "was this published or inferred?" becomes a permanent question on every row. `publishing → published` means the record was always there — less machinery, stronger claim. |
| One atomic `RecordRelease` RPC (build + assertions + images + charts + links in one transaction) | A release spans real GHCR pushes; it cannot be one database transaction. All-or-nothing would also discard expensive partial progress. The run log gives the same "a re-run is a complete repair" property, as *resume*. |
| Registry orchestrates the release saga (inbound Temporal workflow) | Breaks "Record, don't act" — the registry would become a deployment system. CI orchestrates; the registry logs. Temporal stays on the outbound writeback path only. |
| SCD2 on `app` for "what did this app look like at time T" | The append-only per-build manifest snapshot already *is* the history and matches the rest of the schema; SCD2 on identity would add a second history mechanism. Consistent with #553. |
| A `build_target` table declaring the run's intended children up front | Rejected: the plan step writes `allocated` rows at every adoption stage (see "The run log"), so that state already *is* the declaration. A second table would restate it in a shape that can disagree with the run. |
| Enforce "charts may only pin registry-known images" at chart **compose** time, repo-wide | Rejected as a repo-wide switch, kept as a per-domain one (**built as AR-7f**): gated on `domain_adoption.stage = 'allocate'`, exactly like every other tightening here. Repo-wide, no chart could build until every member app had released through the registry once; per-domain, a domain only meets the strict rule after it has been releasing through the registry anyway, and chart builds fail before anything is pushed instead of after. |

