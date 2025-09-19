from manman.src.repository.rabbitmq.util import (
    add_routing_key_prefix,
    add_routing_key_suffix,
)


def test_add_routing_key_suffix_with_suffix():
    routing_key = "test.route"
    suffix = "suffix"
    expected_routing_key = "test.route.suffix"
    assert add_routing_key_suffix(routing_key, suffix) == expected_routing_key


def test_add_routing_key_suffix_without_suffix():
    routing_key = "test.route"
    suffix = None
    expected_routing_key = "test.route"
    assert add_routing_key_suffix(routing_key, suffix) == expected_routing_key


def test_add_routing_key_suffix_with_suffix_and_dot():
    routing_key = "test.route."
    suffix = "suffix"
    expected_routing_key = "test.route.suffix"
    assert add_routing_key_suffix(routing_key, suffix) == expected_routing_key


def test_add_routing_key_suffix_empty_routing_key():
    routing_key = ""
    suffix = "suffix"
    expected_routing_key = ""
    assert add_routing_key_suffix(routing_key, suffix) == expected_routing_key


def test_add_routing_key_suffix_empty_suffix():
    routing_key = "test.route"
    suffix = ""
    expected_routing_key = "test.route"
    assert add_routing_key_suffix(routing_key, suffix) == expected_routing_key


def test_add_routing_key_prefix_with_prefix():
    routing_key = "test.route"
    prefix = "prefix"
    expected_routing_key = "prefix.test.route"
    assert add_routing_key_prefix(routing_key, prefix) == expected_routing_key


def test_add_routing_key_prefix_without_prefix():
    routing_key = "test.route"
    prefix = None
    expected_routing_key = "test.route"
    assert add_routing_key_prefix(routing_key, prefix) == expected_routing_key


def test_add_routing_key_prefix_with_prefix_and_dot():
    routing_key = ".test.route"
    prefix = "prefix"
    expected_routing_key = "prefix.test.route"
    assert add_routing_key_prefix(routing_key, prefix) == expected_routing_key


def test_add_routing_key_prefix_empty_routing_key():
    routing_key = ""
    prefix = "prefix"
    expected_routing_key = ""
    assert add_routing_key_prefix(routing_key, prefix) == expected_routing_key


def test_add_routing_key_prefix_empty_prefix():
    routing_key = "test.route"
    prefix = ""
    expected_routing_key = "test.route"
    assert add_routing_key_prefix(routing_key, prefix) == expected_routing_key
