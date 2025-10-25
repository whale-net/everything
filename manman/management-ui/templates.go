package main

import (
	"html/template"
)

var homeTemplate = template.Must(template.New("home").Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ManMan Management</title>
    
    <!-- HTMX -->
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: #f5f5f5;
            color: #333;
            line-height: 1.6;
        }
        
        .header {
            background: #2c3e50;
            color: white;
            padding: 1rem 2rem;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        
        .header-content {
            max-width: 1200px;
            margin: 0 auto;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        
        .header h1 {
            font-size: 1.5rem;
            font-weight: 600;
        }
        
        .user-info {
            display: flex;
            align-items: center;
            gap: 1rem;
        }
        
        .container {
            max-width: 1200px;
            margin: 2rem auto;
            padding: 0 2rem;
        }
        
        .card {
            background: white;
            border-radius: 8px;
            padding: 1.5rem;
            margin-bottom: 1.5rem;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        
        .card h2 {
            margin-bottom: 1rem;
            color: #2c3e50;
            font-size: 1.25rem;
        }
        
        .status-row {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 0.75rem 0;
            border-bottom: 1px solid #eee;
        }
        
        .status-row:last-child {
            border-bottom: none;
        }
        
        .status-label {
            font-weight: 500;
            color: #666;
        }
        
        .worker-active {
            color: #27ae60;
            font-weight: 600;
            font-family: 'Courier New', monospace;
        }
        
        .worker-inactive {
            color: #95a5a6;
            font-style: italic;
        }
        
        table {
            width: 100%;
            border-collapse: collapse;
        }
        
        th, td {
            padding: 0.75rem;
            text-align: left;
            border-bottom: 1px solid #eee;
        }
        
        th {
            background: #f8f9fa;
            font-weight: 600;
            color: #2c3e50;
        }
        
        .status-running {
            color: #27ae60;
            font-weight: 500;
        }
        
        .status-other {
            color: #f39c12;
        }
        
        .no-servers {
            color: #95a5a6;
            text-align: center;
            padding: 2rem;
            font-style: italic;
        }
        
        .btn {
            display: inline-block;
            padding: 0.5rem 1rem;
            background: #3498db;
            color: white;
            text-decoration: none;
            border-radius: 4px;
            border: none;
            cursor: pointer;
            font-size: 0.9rem;
        }
        
        .btn:hover {
            background: #2980b9;
        }
        
        .btn-danger {
            background: #e74c3c;
        }
        
        .btn-danger:hover {
            background: #c0392b;
        }
        
        .refresh-indicator {
            display: inline-block;
            margin-left: 0.5rem;
            color: #3498db;
            font-size: 0.9rem;
        }
        
        .htmx-indicator {
            opacity: 0;
            transition: opacity 200ms ease-in;
        }
        
        .htmx-request .htmx-indicator {
            opacity: 1;
        }
        
        .htmx-request.htmx-indicator {
            opacity: 1;
        }
    </style>
</head>
<body>
    <div class="header">
        <div class="header-content">
            <h1>🎮 ManMan Management</h1>
            <div class="user-info">
                <span>{{if .User.Name}}{{.User.Name}}{{else if .User.Email}}{{.User.Email}}{{else}}{{.User.PreferredUsername}}{{end}}</span>
                <a href="/auth/logout" class="btn btn-danger">Logout</a>
            </div>
        </div>
    </div>
    
    <div class="container">
        <div class="card">
            <h2>Worker Status</h2>
            <div class="status-row">
                <span class="status-label">Active Worker ID:</span>
                <span id="worker-status" 
                      hx-get="/api/worker-status/{{.UserID}}" 
                      hx-trigger="load, every 10s"
                      hx-indicator=".refresh-indicator">
                    {{if .WorkerID}}
                        <span class="worker-active">{{.WorkerID}}</span>
                    {{else}}
                        <span class="worker-inactive">No active worker</span>
                    {{end}}
                </span>
                <span class="refresh-indicator htmx-indicator">🔄</span>
            </div>
        </div>

        <div class="card">
            <h2>Running Servers</h2>
            <table>
                <thead>
                    <tr>
                        <th>Server Name</th>
                        <th>Status</th>
                        <th>Connection</th>
                    </tr>
                </thead>
                <tbody id="servers-list"
                       hx-get="/api/servers/{{.UserID}}"
                       hx-trigger="load, every 15s"
                       hx-indicator=".refresh-indicator">
                    {{if .Servers}}
                        {{range .Servers}}
                        <tr>
                            <td>{{.Name}}</td>
                            <td>
                                <span class="{{if eq .Status "running"}}status-running{{else}}status-other{{end}}">
                                    {{.Status}}
                                </span>
                            </td>
                            <td>{{.IP}}:{{.Port}}</td>
                        </tr>
                        {{end}}
                    {{else}}
                        <tr>
                            <td colspan="3">
                                <p class="no-servers">No running servers</p>
                            </td>
                        </tr>
                    {{end}}
                </tbody>
            </table>
        </div>

        <div class="card">
            <h2>Quick Actions</h2>
            <p style="color: #666; margin-bottom: 1rem;">
                Additional management features will be added here.
            </p>
        </div>
    </div>
    
    <script>
        // HTMX event listeners for better UX
        document.body.addEventListener('htmx:beforeRequest', function(evt) {
            console.log('HTMX request starting:', evt.detail.path);
        });
        
        document.body.addEventListener('htmx:afterRequest', function(evt) {
            console.log('HTMX request completed:', evt.detail.path);
        });
    </script>
</body>
</html>
`))
