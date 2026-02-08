"""Unit tests for Tilt MCP Server."""

import json
from unittest.mock import patch

import pytest

from server import (
    ANSI_ESCAPE,
    run_tilt_command,
    strip_ansi,
    _tilt_get_resources as tilt_get_resources,
    _tilt_logs as tilt_logs,
    _tilt_status as tilt_status,
    _tilt_trigger as tilt_trigger,
)


class TestHelperFunctions:
    """Test helper functions."""

    def test_strip_ansi(self):
        """Test ANSI escape code removal."""
        # Test basic color codes
        text_with_ansi = "\x1b[31mRed text\x1b[0m"
        assert strip_ansi(text_with_ansi) == "Red text"

        # Test complex ANSI sequences
        text_with_ansi = "\x1b[1;32mBold green\x1b[0m normal \x1b[4munderline\x1b[0m"
        assert strip_ansi(text_with_ansi) == "Bold green normal underline"

        # Test text without ANSI codes
        text_without_ansi = "Plain text"
        assert strip_ansi(text_without_ansi) == "Plain text"

        # Test empty string
        assert strip_ansi("") == ""

    def test_ansi_regex_pattern(self):
        """Test ANSI regex pattern matches common sequences."""
        # Common ANSI sequences
        sequences = [
            "\x1b[0m",      # Reset
            "\x1b[31m",     # Red
            "\x1b[1;32m",   # Bold green
            "\x1b[4m",      # Underline
            "\x1b[K",       # Clear line
            "\x1b[2J",      # Clear screen
        ]
        for seq in sequences:
            assert ANSI_ESCAPE.search(seq) is not None

    @patch('server.subprocess.run')
    def test_run_tilt_command_success(self, mock_run):
        """Test successful tilt command execution."""
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"test": "output"}'
        mock_run.return_value.stderr = ''

        result = run_tilt_command(['get', 'session'])

        assert result['success'] is True
        assert result['output'] == '{"test": "output"}'
        assert result['error'] == ''

    @patch('server.subprocess.run')
    def test_run_tilt_command_failure(self, mock_run):
        """Test failed tilt command execution."""
        mock_run.return_value.returncode = 1
        mock_run.return_value.stdout = ''
        mock_run.return_value.stderr = 'Error: command failed'

        result = run_tilt_command(['get', 'session'])

        assert result['success'] is False
        assert result['output'] == ''
        assert result['error'] == 'Error: command failed'

    @patch('server.subprocess.run')
    def test_run_tilt_command_not_found(self, mock_run):
        """Test tilt command not found error."""
        mock_run.side_effect = FileNotFoundError()

        result = run_tilt_command(['get', 'session'])

        assert result['success'] is False
        assert result['error'] == 'tilt command not found. Is Tilt installed?'

    @patch('server.subprocess.run')
    def test_run_tilt_command_timeout(self, mock_run):
        """Test tilt command timeout."""
        from subprocess import TimeoutExpired
        mock_run.side_effect = TimeoutExpired('tilt', 10)

        result = run_tilt_command(['get', 'session'], timeout=10)

        assert result['success'] is False
        assert 'timed out after 10 seconds' in result['error']


class TestTiltStatus:
    """Test tilt_status tool."""

    @patch('server.run_tilt_command')
    def test_tilt_status_success(self, mock_run):
        """Test successful status retrieval."""
        mock_run.return_value = {
            'success': True,
            'output': json.dumps({
                'items': [{
                    'metadata': {
                        'name': 'Tiltfile',
                        'creationTimestamp': '2024-01-01T00:00:00Z'
                    },
                    'status': {
                        'ready': True,
                        'targets': ['api', 'postgres']
                    }
                }]
            }),
            'error': ''
        }

        result = tilt_status()

        assert 'error' not in result
        assert result['name'] == 'Tiltfile'
        assert result['creationTimestamp'] == '2024-01-01T00:00:00Z'
        assert result['status']['ready'] is True
        assert result['targets'] == ['api', 'postgres']

    @patch('server.run_tilt_command')
    def test_tilt_status_with_resource(self, mock_run):
        """Test status retrieval with specific resource included."""
        # First call: session status
        mock_run.side_effect = [
            {
                'success': True,
                'output': json.dumps({
                    'items': [{
                        'metadata': {
                            'name': 'Tiltfile',
                            'creationTimestamp': '2024-01-01T00:00:00Z'
                        },
                        'status': {
                            'ready': True,
                            'targets': ['api', 'postgres']
                        }
                    }]
                }),
                'error': ''
            },
            # Second call: uiresources
            {
                'success': True,
                'output': json.dumps({
                    'items': [
                        {
                            'metadata': {'name': 'postgres-dev'},
                            'status': {
                                'runtimeStatus': 'ok',
                                'updateStatus': 'ready',
                                'conditions': []
                            }
                        }
                    ]
                }),
                'error': ''
            }
        ]

        result = tilt_status(resource='postgres-dev')

        assert 'error' not in result
        assert result['name'] == 'Tiltfile'
        assert result['resource'] == 'postgres-dev'
        assert result['resourceStatus']['name'] == 'postgres-dev'
        assert result['resourceStatus']['runtimeStatus'] == 'ok'

    @patch('server.run_tilt_command')
    def test_tilt_status_with_missing_resource(self, mock_run):
        """Test status retrieval when requested resource is not found."""
        mock_run.side_effect = [
            {
                'success': True,
                'output': json.dumps({
                    'items': [{
                        'metadata': {
                            'name': 'Tiltfile',
                            'creationTimestamp': '2024-01-01T00:00:00Z'
                        },
                        'status': {
                            'ready': True,
                            'targets': ['api', 'postgres']
                        }
                    }]
                }),
                'error': ''
            },
            {
                'success': True,
                'output': json.dumps({
                    'items': [
                        {
                            'metadata': {'name': 'postgres-dev'},
                            'status': {
                                'runtimeStatus': 'ok',
                                'updateStatus': 'ready',
                                'conditions': []
                            }
                        }
                    ]
                }),
                'error': ''
            }
        ]

        result = tilt_status(resource='missing-resource')

        assert 'error' not in result
        assert result['resource'] == 'missing-resource'
        assert result['resourceStatus'] is None

    @patch('server.run_tilt_command')
    def test_tilt_status_no_session(self, mock_run):
        """Test status when no session exists."""
        mock_run.return_value = {
            'success': True,
            'output': json.dumps({'items': []}),
            'error': ''
        }

        result = tilt_status()

        assert 'error' in result
        assert result['error'] == 'No Tilt session found'

    @patch('server.run_tilt_command')
    def test_tilt_status_command_failure(self, mock_run):
        """Test status when tilt command fails."""
        mock_run.return_value = {
            'success': False,
            'output': '',
            'error': 'connection refused'
        }

        result = tilt_status()

        assert 'error' in result
        assert result['error'] == 'Failed to get Tilt session status'
        assert 'connection refused' in result['details']


class TestTiltGetResources:
    """Test tilt_get_resources tool."""

    @patch('server.run_tilt_command')
    def test_get_resources_success(self, mock_run):
        """Test successful resource retrieval."""
        mock_run.return_value = {
            'success': True,
            'output': json.dumps({
                'items': [
                    {
                        'metadata': {'name': 'postgres-dev'},
                        'status': {
                            'runtimeStatus': 'ok',
                            'updateStatus': 'ready',
                            'conditions': []
                        }
                    },
                    {
                        'metadata': {'name': 'manmanv2-api'},
                        'status': {
                            'runtimeStatus': 'pending',
                            'updateStatus': 'updating',
                            'conditions': [{'type': 'Ready', 'status': 'False'}]
                        }
                    }
                ]
            }),
            'error': ''
        }

        result = tilt_get_resources()

        assert 'error' not in result
        assert result['count'] == 2
        assert len(result['resources']) == 2
        assert result['resources'][0]['name'] == 'postgres-dev'
        assert result['resources'][0]['runtimeStatus'] == 'ok'
        assert result['resources'][1]['name'] == 'manmanv2-api'
        assert result['resources'][1]['runtimeStatus'] == 'pending'

    @patch('server.run_tilt_command')
    def test_get_resources_empty(self, mock_run):
        """Test resource retrieval with no resources."""
        mock_run.return_value = {
            'success': True,
            'output': json.dumps({'items': []}),
            'error': ''
        }

        result = tilt_get_resources()

        assert 'error' not in result
        assert result['count'] == 0
        assert result['resources'] == []

    @patch('server.run_tilt_command')
    def test_get_resources_failure(self, mock_run):
        """Test resource retrieval failure."""
        mock_run.return_value = {
            'success': False,
            'output': '',
            'error': 'tilt not running'
        }

        result = tilt_get_resources()

        assert 'error' in result
        assert result['error'] == 'Failed to get Tilt resources'


class TestTiltLogs:
    """Test tilt_logs tool."""

    @patch('server.run_tilt_command')
    def test_logs_success(self, mock_run):
        """Test successful log retrieval."""
        mock_run.return_value = {
            'success': True,
            'output': '\x1b[32mINFO\x1b[0m Starting server\n\x1b[33mWARN\x1b[0m Port in use',
            'error': ''
        }

        result = tilt_logs('postgres-dev', lines=50)

        assert 'error' not in result
        assert result['resource'] == 'postgres-dev'
        assert result['lines'] == 50
        assert 'INFO Starting server' in result['logs']
        assert 'WARN Port in use' in result['logs']
        assert '\x1b[' not in result['logs']  # No ANSI codes

    @patch('server.run_tilt_command')
    def test_logs_default_lines(self, mock_run):
        """Test log retrieval with default line count."""
        mock_run.return_value = {
            'success': True,
            'output': 'Log line 1\nLog line 2',
            'error': ''
        }

        result = tilt_logs('manmanv2-api')

        assert result['lines'] == 100  # Default value

    @patch('server.run_tilt_command')
    def test_logs_failure(self, mock_run):
        """Test log retrieval failure."""
        mock_run.return_value = {
            'success': False,
            'output': '',
            'error': 'resource not found'
        }

        result = tilt_logs('invalid-resource')

        assert 'error' in result
        assert 'Failed to get logs for resource' in result['error']
        assert 'invalid-resource' in result['error']


class TestTiltTrigger:
    """Test tilt_trigger tool."""

    @patch('server.run_tilt_command')
    def test_trigger_success(self, mock_run):
        """Test successful resource trigger."""
        mock_run.return_value = {
            'success': True,
            'output': 'Triggered resource: manmanv2-api',
            'error': ''
        }

        result = tilt_trigger('manmanv2-api')

        assert 'error' not in result
        assert result['success'] is True
        assert result['resource'] == 'manmanv2-api'
        assert 'Successfully triggered' in result['message']

    @patch('server.run_tilt_command')
    def test_trigger_failure(self, mock_run):
        """Test resource trigger failure."""
        mock_run.return_value = {
            'success': False,
            'output': '',
            'error': 'resource does not exist'
        }

        result = tilt_trigger('invalid-resource')

        assert 'error' in result
        assert 'Failed to trigger resource' in result['error']
        assert 'invalid-resource' in result['error']
