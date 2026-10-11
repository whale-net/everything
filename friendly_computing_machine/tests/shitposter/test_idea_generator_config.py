"""Idea-generator config tunables: defaults, overrides, "off", and invalid fallbacks."""

import logging

import pytest

from friendly_computing_machine.src.friendly_computing_machine import (
    shitposter_config as cfg,
)

_ENV_VARS = (
    "FCM_SHITPOSTER_THEME_GENERATOR_AGENT_ID",
    "FCM_SHITPOSTER_NOVEL_GENERATOR_AGENT_ID",
    "FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP",
    "FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS",
    "FCM_SHITPOSTER_THEME_TARGET_WEIGHT",
    "FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT",
)


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch):
    for name in _ENV_VARS:
        monkeypatch.delenv(name, raising=False)


def test_defaults():
    assert cfg.load_theme_generator_agent_id() == "shitposter-theme-generator"
    assert cfg.load_novel_generator_agent_id() == "shitposter-novel-generator"
    assert cfg.idea_generator_run_cap() == 2
    assert cfg.idea_generator_cadence_hours() == 24
    assert cfg.theme_target_weight() == pytest.approx(0.15)
    assert cfg.novel_target_weight() == pytest.approx(0.10)


def test_agent_ids_are_distinct_by_default():
    assert cfg.load_theme_generator_agent_id() != cfg.load_novel_generator_agent_id()


def test_valid_overrides(monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_GENERATOR_AGENT_ID", " theme-x ")
    monkeypatch.setenv("FCM_SHITPOSTER_NOVEL_GENERATOR_AGENT_ID", "novel-y")
    monkeypatch.setenv("FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP", "5")
    monkeypatch.setenv("FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS", "6")
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_TARGET_WEIGHT", "0.3")
    monkeypatch.setenv("FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT", "1")
    assert cfg.load_theme_generator_agent_id() == "theme-x"
    assert cfg.load_novel_generator_agent_id() == "novel-y"
    assert cfg.idea_generator_run_cap() == 5
    assert cfg.idea_generator_cadence_hours() == 6
    assert cfg.theme_target_weight() == pytest.approx(0.3)
    assert cfg.novel_target_weight() == pytest.approx(1.0)


def test_blank_agent_id_uses_default(monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_GENERATOR_AGENT_ID", "   ")
    assert cfg.load_theme_generator_agent_id() == "shitposter-theme-generator"


@pytest.mark.parametrize("raw", ["off", "OFF", " Off "])
def test_cadence_off_disables_schedule(monkeypatch, raw):
    monkeypatch.setenv("FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS", raw)
    assert cfg.idea_generator_cadence_hours() is None


@pytest.mark.parametrize(
    ("name", "raw", "getter", "default"),
    [
        ("FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP", "0", cfg.idea_generator_run_cap, 2),
        ("FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP", "-3", cfg.idea_generator_run_cap, 2),
        ("FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP", "two", cfg.idea_generator_run_cap, 2),
        ("FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS", "0", cfg.idea_generator_cadence_hours, 24),
        ("FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS", "daily", cfg.idea_generator_cadence_hours, 24),
        ("FCM_SHITPOSTER_THEME_TARGET_WEIGHT", "0", cfg.theme_target_weight, 0.15),
        ("FCM_SHITPOSTER_THEME_TARGET_WEIGHT", "1.5", cfg.theme_target_weight, 0.15),
        ("FCM_SHITPOSTER_THEME_TARGET_WEIGHT", "nan", cfg.theme_target_weight, 0.15),
        ("FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT", "-0.1", cfg.novel_target_weight, 0.10),
        ("FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT", "lots", cfg.novel_target_weight, 0.10),
    ],
)
def test_invalid_value_falls_back_with_warning(monkeypatch, caplog, name, raw, getter, default):
    monkeypatch.setenv(name, raw)
    with caplog.at_level(logging.WARNING, logger=cfg.logger.name):
        assert getter() == pytest.approx(default)
    warnings = [r for r in caplog.records if r.levelno == logging.WARNING]
    assert len(warnings) == 1
    assert name in warnings[0].getMessage()
