"""Adaptive source weights: compute decisions (write-free) and apply revisions."""

import pytest

from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    source_weights,
)

_PENDING = pytest.mark.skip(reason="filled in by the Testing phase")


def test_module_exposes_compute_and_apply():
    assert callable(source_weights.compute_weight_changes)
    assert callable(source_weights.apply_weight_changes)


@_PENDING
def test_outperform_steps_up_underperform_steps_down():
    pass


@_PENDING
def test_below_minimum_leaves_weight_unchanged_with_reason():
    pass


@_PENDING
def test_only_overlapping_days_are_compared():
    pass


@_PENDING
def test_bounds_clamp():
    pass


@_PENDING
def test_ceiling_breach_not_applied():
    pass


@_PENDING
def test_tie_break_when_both_rise():
    pass


@_PENDING
def test_failed_run_writes_no_weight_revision():
    pass


@_PENDING
def test_weight_history_visible_to_operator():
    pass
