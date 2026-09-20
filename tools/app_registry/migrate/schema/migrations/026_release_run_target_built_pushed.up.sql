-- App Registry — release_run_target: add built/pushed intra-build states
--
-- The release page's live status only distinguished "queued" from
-- "building" for the whole batch-wide GHA build job -- no visibility
-- into whether an individual image target's build had actually finished
-- producing an image ("built") vs. finished pushing it to the registry
-- ("pushed"). These are optional progress markers a target may pass
-- through on its way from 'building' to 'publishing' (see
-- repository.ReleaseRunTargetState's doc comment) -- a target that never
-- reports them (chart targets always; an image target on an older
-- release_helper_go) still walks 'building' -> 'publishing' directly, which
-- is why that direct edge stays legal below alongside the new stepwise one.
ALTER TABLE release_run_target DROP CONSTRAINT release_run_target_state_check;
ALTER TABLE release_run_target ADD CONSTRAINT release_run_target_state_check CHECK (
    state IN ('queued', 'building', 'built', 'pushed', 'publishing', 'recording', 'succeeded', 'failed')
);
