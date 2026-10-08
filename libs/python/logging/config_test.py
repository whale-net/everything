"""Tests for tracing setup in configure_logging."""

from unittest import mock

import pytest
from opentelemetry import propagate, trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.util._once import Once

from libs.python.logging import config


@pytest.fixture
def reset_otel(monkeypatch):
    """Let each test install a fresh global TracerProvider and propagator."""
    monkeypatch.delenv("OTEL_SDK_DISABLED", raising=False)
    monkeypatch.delenv("OTEL_TRACES_DISABLED", raising=False)
    saved_propagator = propagate.get_global_textmap()
    trace._TRACER_PROVIDER_SET_ONCE = Once()
    trace._TRACER_PROVIDER = None
    yield
    provider = trace.get_tracer_provider()
    if isinstance(provider, TracerProvider):
        provider.shutdown()
    trace._TRACER_PROVIDER_SET_ONCE = Once()
    trace._TRACER_PROVIDER = None
    propagate.set_global_textmap(saved_propagator)


def _configure(**kwargs):
    exporter = InMemorySpanExporter()
    with mock.patch.object(config, "OTLPSpanExporter", return_value=exporter):
        config.configure_logging(
            service_name="tracing-test",
            enable_otlp=False,
            force_reconfigure=True,
            **kwargs,
        )
    return exporter


def test_enable_tracing_records_spans_and_injects_traceparent(reset_otel):
    _configure(enable_tracing=True)

    provider = trace.get_tracer_provider()
    assert isinstance(provider, TracerProvider)
    assert provider.resource.attributes["service.name"] == "tracing-test"

    with trace.get_tracer(__name__).start_as_current_span("op") as span:
        ctx = span.get_span_context()
        assert span.is_recording()
        assert ctx.is_valid
        assert ctx.trace_id != 0

        carrier = {}
        propagate.inject(carrier)

    assert carrier["traceparent"].split("-")[1] == format(ctx.trace_id, "032x")


def test_tracing_off_by_default(reset_otel):
    _configure()

    with trace.get_tracer(__name__).start_as_current_span("op") as span:
        assert not span.is_recording()
        assert not span.get_span_context().is_valid


@pytest.mark.parametrize("env", ["OTEL_SDK_DISABLED", "OTEL_TRACES_DISABLED"])
def test_env_disables_tracing(reset_otel, monkeypatch, env):
    monkeypatch.setenv(env, "true")
    _configure(enable_tracing=True)

    assert not isinstance(trace.get_tracer_provider(), TracerProvider)
