"""Test to verify the fix for temporal workflow activity call issue."""

import inspect

from friendly_computing_machine.src.friendly_computing_machine.temporal.slack.activity import (
    FixSlackTaggingParams,
    fix_slack_tagging_activity,
)


def test_fix_slack_tagging_activity_signature():
    """Test that fix_slack_tagging_activity has the expected signature."""
    sig = inspect.signature(fix_slack_tagging_activity)

    # Should have 1 parameter: params (FixSlackTaggingParams)
    assert len(sig.parameters) == 1

    params = list(sig.parameters.values())

    # First parameter should be 'params' with FixSlackTaggingParams annotation
    assert params[0].name == "params"
    assert params[0].annotation is FixSlackTaggingParams
    assert params[0].default == inspect.Parameter.empty


def test_temporal_activity_call_pattern():
    """Test that the activity can be called with the correct arguments pattern."""
    # This simulates what the temporal framework expects when we pass a params object
    # to workflow.execute_activity

    test_text = "Hello @here everyone!"
    test_add_here_tag = True

    # This should work - the way the fixed code will call it
    params = FixSlackTaggingParams(text=test_text, add_here_tag=test_add_here_tag)

    # Verify we can pass the params object to call the function
    # (this is essentially what temporal framework does internally)
    sig = inspect.signature(fix_slack_tagging_activity)
    bound_args = sig.bind(params)
    bound_args.apply_defaults()

    # Check that the binding worked correctly
    assert bound_args.arguments["params"].text == test_text
    assert bound_args.arguments["params"].add_here_tag == test_add_here_tag


def test_temporal_activity_call_pattern_with_defaults():
    """Test the activity call pattern with default parameter."""
    test_text = "Hello everyone!"

    # Test with only text argument (using default for add_here_tag parameter)
    params = FixSlackTaggingParams(text=test_text)

    sig = inspect.signature(fix_slack_tagging_activity)
    bound_args = sig.bind(params)
    bound_args.apply_defaults()

    # Check that the binding worked correctly with default
    assert bound_args.arguments["params"].text == test_text
    assert bound_args.arguments["params"].add_here_tag is False
