-- App Registry — drop domain_adoption (AR-5 cutover)
--
-- domain_adoption (migration 001) gated the per-domain rollout of
-- AllocateVersion (AR-5), CheckChartHermeticity's compose-time enforcement
-- (AR-7f), and RecordArtifact's pre-AR-7 direct-create fallback. The AR-5
-- cutover makes every domain unconditionally "allocate" -- there is no
-- longer a per-domain stage to track, so the table is dropped rather than
-- left holding a value nothing reads.
DROP TABLE domain_adoption;
