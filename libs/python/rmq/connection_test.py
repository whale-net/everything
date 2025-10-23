"""Tests for RabbitMQ connection initialization."""

from libs.python.rmq.connection import init_rabbitmq_from_config, __GLOBALS


def test_init_rabbitmq_from_config_default_vhost_with_suffix():
    """When vhost is default '/', suffix should be applied."""
    config = {
        'host': 'localhost',
        'port': 5672,
        'username': 'guest',
        'password': 'guest',
        'vhost': '/',
    }
    
    init_rabbitmq_from_config(config, vhost_suffix='dev')
    
    # Check that the stored vhost is just 'dev' (not '/')
    assert __GLOBALS['rmq_parameters']['virtual_host'] == 'dev'


def test_init_rabbitmq_from_config_custom_vhost_with_suffix():
    """When vhost is custom, it should be preserved even with suffix."""
    config = {
        'host': 'localhost',
        'port': 5672,
        'username': 'guest',
        'password': 'guest',
        'vhost': 'my-custom-vhost',
    }
    
    init_rabbitmq_from_config(config, vhost_suffix='dev')
    
    # Check that the stored vhost has suffix appended
    assert __GLOBALS['rmq_parameters']['virtual_host'] == 'my-custom-vhost-dev'


def test_init_rabbitmq_from_config_custom_vhost_no_suffix():
    """When vhost is custom and no suffix, vhost should be used as-is."""
    config = {
        'host': 'localhost',
        'port': 5672,
        'username': 'guest',
        'password': 'guest',
        'vhost': 'my-custom-vhost',
    }
    
    init_rabbitmq_from_config(config, vhost_suffix=None)
    
    # Check that the stored vhost is preserved
    assert __GLOBALS['rmq_parameters']['virtual_host'] == 'my-custom-vhost'


def test_init_rabbitmq_from_config_default_vhost_no_suffix():
    """When vhost is default '/' and no suffix, default should be used."""
    config = {
        'host': 'localhost',
        'port': 5672,
        'username': 'guest',
        'password': 'guest',
        'vhost': '/',
    }
    
    init_rabbitmq_from_config(config, vhost_suffix=None)
    
    # Check that the stored vhost is the default
    assert __GLOBALS['rmq_parameters']['virtual_host'] == '/'


def test_init_rabbitmq_from_config_missing_vhost_with_suffix():
    """When vhost is not in config, it should default to '/' and apply suffix."""
    config = {
        'host': 'localhost',
        'port': 5672,
        'username': 'guest',
        'password': 'guest',
    }
    
    init_rabbitmq_from_config(config, vhost_suffix='dev')
    
    # Check that the stored vhost is just 'dev' (default '/' + suffix = suffix)
    assert __GLOBALS['rmq_parameters']['virtual_host'] == 'dev'
