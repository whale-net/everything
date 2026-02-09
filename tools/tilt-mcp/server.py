#!/usr/bin/env python3
"""Tilt MCP Server - Model Context Protocol server for Tilt CLI.

This server provides tools to monitor and control Tilt resources through
the MCP protocol, enabling Claude Code to interact with Tilt sessions.
"""

import json
import re
import subprocess
from datetime import datetime
from typing import Any

from fastmcp import FastMCP

# Initialize FastMCP server
mcp = FastMCP("tilt-mcp")

# ANSI escape code regex for cleaning terminal output
ANSI_ESCAPE = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')


def strip_ansi(text: str) -> str:
    """Remove ANSI escape codes from text.

    Args:
        text: Text potentially containing ANSI escape codes

    Returns:
        Text with ANSI codes removed
    """
    return ANSI_ESCAPE.sub('', text)


def run_tilt_command(args: list[str], timeout: int = 10) -> dict[str, Any]:
    """Execute a tilt CLI command and return structured result.

    Args:
        args: Command arguments (e.g., ['get', 'session', '-o', 'json'])
        timeout: Command timeout in seconds

    Returns:
        Dictionary with 'success', 'output', and 'error' keys
    """
    try:
        result = subprocess.run(
            ['tilt'] + args,
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False
        )

        return {
            'success': result.returncode == 0,
            'output': result.stdout,
            'error': result.stderr
        }
    except FileNotFoundError:
        return {
            'success': False,
            'output': '',
            'error': 'tilt command not found. Is Tilt installed?'
        }
    except subprocess.TimeoutExpired:
        return {
            'success': False,
            'output': '',
            'error': f'Command timed out after {timeout} seconds'
        }
    except Exception as e:
        return {
            'success': False,
            'output': '',
            'error': f'Unexpected error: {str(e)}'
        }


def _tilt_status(resource: str | None = None) -> dict[str, Any]:
    """Get overall Tilt session status.

    Returns session information including name, creation time, status, and targets.
    If Tilt is not running, returns an error message.

    Returns:
        Dictionary containing session details or error information
    """
    result = run_tilt_command(['get', 'session', '-o', 'json'])

    if not result['success']:
        return {
            'error': 'Failed to get Tilt session status',
            'details': result['error']
        }

    try:
        data = json.loads(result['output'])
        # Tilt returns {"items": [...]} format
        if 'items' in data and len(data['items']) > 0:
            session = data['items'][0]
            status_payload = {
                'name': session.get('metadata', {}).get('name', 'unknown'),
                'creationTimestamp': session.get('metadata', {}).get('creationTimestamp'),
                'status': session.get('status', {}),
                'targets': session.get('status', {}).get('targets', [])
            }
            if resource:
                # Provide a focused view for a specific resource, if requested.
                resources = _tilt_get_resources().get('resources', [])
                resource_status = next(
                    (item for item in resources if item.get('name') == resource),
                    None
                )
                status_payload['resource'] = resource
                status_payload['resourceStatus'] = resource_status
            return status_payload
        else:
            return {
                'error': 'No Tilt session found',
                'details': 'Tilt may not be running or no session exists'
            }
    except json.JSONDecodeError as e:
        return {
            'error': 'Failed to parse Tilt output',
            'details': str(e)
        }


@mcp.tool()
def tilt_status(resource: str | None = None) -> dict[str, Any]:
    """Get overall Tilt session status.

    Returns session information including name, creation time, status, and targets.
    If Tilt is not running, returns an error message.

    Args:
        resource: Optional resource name to include status for (e.g., 'postgres-dev')

    Returns:
        Dictionary containing session details or error information
    """
    return _tilt_status(resource)


def _tilt_get_resources() -> dict[str, Any]:
    """List all Tilt resources with their current status.

    Returns information about all resources including their runtime status,
    update status, and conditions.

    Returns:
        Dictionary with 'resources' array or error information
    """
    result = run_tilt_command(['get', 'uiresources', '-o', 'json'])

    if not result['success']:
        return {
            'error': 'Failed to get Tilt resources',
            'details': result['error']
        }

    try:
        data = json.loads(result['output'])
        resources = []

        # Parse resource items
        if 'items' in data:
            for item in data['items']:
                metadata = item.get('metadata', {})
                status = item.get('status', {})

                resources.append({
                    'name': metadata.get('name', 'unknown'),
                    'runtimeStatus': status.get('runtimeStatus'),
                    'updateStatus': status.get('updateStatus'),
                    'conditions': status.get('conditions', [])
                })

        return {
            'resources': resources,
            'count': len(resources)
        }
    except json.JSONDecodeError as e:
        return {
            'error': 'Failed to parse Tilt output',
            'details': str(e)
        }


@mcp.tool()
def tilt_get_resources() -> dict[str, Any]:
    """List all Tilt resources with their current status.

    Returns information about all resources including their runtime status,
    update status, and conditions.

    Returns:
        Dictionary with 'resources' array or error information
    """
    return _tilt_get_resources()


def _tilt_logs(resource: str, lines: int = 100) -> dict[str, Any]:
    """Retrieve logs for a specific Tilt resource.

    Fetches recent log entries with ANSI escape codes removed for clean display.

    Args:
        resource: Name of the Tilt resource (e.g., 'postgres-dev', 'manmanv2-api')
        lines: Number of recent log lines to retrieve (default: 100)

    Returns:
        Dictionary with 'logs' text or error information
    """
    # Tilt CLI doesn't support --tail; fetch logs and trim locally.
    result = run_tilt_command(['logs', resource], timeout=30)

    if not result['success']:
        return {
            'error': f'Failed to get logs for resource: {resource}',
            'details': result['error']
        }

    # Strip ANSI codes from logs and trim to last N lines
    clean_logs = strip_ansi(result['output'])
    if lines is not None and lines > 0:
        log_lines = clean_logs.splitlines()
        clean_logs = '\n'.join(log_lines[-lines:])

    return {
        'resource': resource,
        'lines': lines,
        'logs': clean_logs,
        'timestamp': datetime.utcnow().isoformat() + 'Z'
    }


@mcp.tool()
def tilt_logs(resource: str, lines: int = 100) -> dict[str, Any]:
    """Retrieve logs for a specific Tilt resource.

    Fetches recent log entries with ANSI escape codes removed for clean display.

    Args:
        resource: Name of the Tilt resource (e.g., 'postgres-dev', 'manmanv2-api')
        lines: Number of recent log lines to retrieve (default: 100)

    Returns:
        Dictionary with 'logs' text or error information
    """
    return _tilt_logs(resource, lines)


def _tilt_trigger(resource: str) -> dict[str, Any]:
    """Force rebuild/update of a specific Tilt resource.

    Triggers Tilt to rebuild and redeploy the specified resource immediately.

    Args:
        resource: Name of the Tilt resource to trigger

    Returns:
        Dictionary with success confirmation or error information
    """
    result = run_tilt_command(['trigger', resource])

    if not result['success']:
        return {
            'error': f'Failed to trigger resource: {resource}',
            'details': result['error']
        }

    return {
        'success': True,
        'resource': resource,
        'message': f'Successfully triggered rebuild of {resource}',
        'timestamp': datetime.utcnow().isoformat() + 'Z'
    }


@mcp.tool()
def tilt_trigger(resource: str) -> dict[str, Any]:
    """Force rebuild/update of a specific Tilt resource.

    Triggers Tilt to rebuild and redeploy the specified resource immediately.

    Args:
        resource: Name of the Tilt resource to trigger

    Returns:
        Dictionary with success confirmation or error information
    """
    return _tilt_trigger(resource)


def _tilt_reload() -> dict[str, Any]:
    """Reload the Tiltfile configuration.

    Forces Tilt to re-evaluate the Tiltfile, picking up any configuration changes.
    This is useful after modifying the Tiltfile or related configuration files.

    Returns:
        Dictionary with success confirmation or error information
    """
    # Trigger the special (Tiltfile) resource to reload configuration
    result = run_tilt_command(['trigger', '(Tiltfile)'])

    if not result['success']:
        return {
            'error': 'Failed to reload Tiltfile',
            'details': result['error']
        }

    return {
        'success': True,
        'message': 'Successfully reloaded Tiltfile configuration',
        'timestamp': datetime.utcnow().isoformat() + 'Z'
    }


@mcp.tool()
def tilt_reload() -> dict[str, Any]:
    """Reload the Tiltfile configuration.

    Forces Tilt to re-evaluate the Tiltfile, picking up any configuration changes.
    This is useful after modifying the Tiltfile or related configuration files.

    Returns:
        Dictionary with success confirmation or error information
    """
    return _tilt_reload()


if __name__ == '__main__':
    mcp.run(transport='stdio')
