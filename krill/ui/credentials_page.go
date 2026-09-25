package main

import (
	"html/template"
	"net/http"
)

// handleCredentials is the credential-widget page the original UI
// shipped with, now rendered inside the shell chrome at its own route
// (FR 85a8b33c keeps it reachable from the nav rather than replacing it).
// The widget itself is unchanged: inline vanilla JS against
// POST/GET /credentials and DELETE /credentials/{id}, mounted by
// setupRoutes via app.mcpProvider.MountSelfServe, so an operator can mint
// a static bearer token for a non-OAuth2 MCP client without a devtools
// console.
func (app *App) handleCredentials(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Credentials", credentialsPath, renderPage(credentialsTemplate, nil))
}

// credentialsTemplate is the widget's markup. It is a template rather
// than a constant because every other page body is, and so that the
// widget's markup is escaped by the same machinery as the rest of the
// shell -- the one per-caller string on this page (the signed-in
// username) is rendered by the shell chrome, not here. The script body
// contains no {{ }} sequences, so html/template treats it as static text.
var credentialsTemplate = template.Must(template.New("credentials").Parse(`<h2>Credentials</h2>
<p>Mint a static bearer token for an MCP client that can't run the OAuth2 sign-in flow.</p>
<button id="generate-btn" type="button">Generate a token</button>
<div id="new-token" style="display:none; margin-top: 0.5em;">
  <p><strong>Copy this token now -- it will not be shown again after you leave this page.</strong></p>
  <input id="new-token-value" type="text" readonly style="width: 100%; font-family: monospace;">
</div>

<h3>Existing credentials</h3>
<button id="refresh-btn" type="button">Refresh</button>
<table id="credentials-table">
  <thead>
    <tr><th>ID</th><th>Created</th><th>Status</th><th></th></tr>
  </thead>
  <tbody id="credentials-body"></tbody>
</table>

<script>
function escapeHTML(s) {
  const div = document.createElement('div');
  div.textContent = s;
  return div.innerHTML;
}

async function refreshCredentials() {
  const body = document.getElementById('credentials-body');
  body.innerHTML = '';
  const res = await fetch('/credentials', { method: 'GET' });
  if (!res.ok) {
    body.innerHTML = '<tr><td colspan="4">failed to load credentials</td></tr>';
    return;
  }
  const data = await res.json();
  const creds = data.credentials || [];
  if (creds.length === 0) {
    body.innerHTML = '<tr><td colspan="4">no credentials yet</td></tr>';
    return;
  }
  for (const cred of creds) {
    const row = document.createElement('tr');
    const status = cred.revoked_at ? 'revoked' : 'active';
    row.innerHTML =
      '<td>' + escapeHTML(cred.id) + '</td>' +
      '<td>' + escapeHTML(cred.created_at) + '</td>' +
      '<td>' + escapeHTML(status) + '</td>' +
      '<td></td>';
    if (!cred.revoked_at) {
      const revokeBtn = document.createElement('button');
      revokeBtn.type = 'button';
      revokeBtn.textContent = 'Revoke';
      revokeBtn.addEventListener('click', function () { revokeCredential(cred.id); });
      row.lastElementChild.appendChild(revokeBtn);
    }
    body.appendChild(row);
  }
}

async function revokeCredential(id) {
  await fetch('/credentials/' + encodeURIComponent(id), { method: 'DELETE' });
  refreshCredentials();
}

document.getElementById('generate-btn').addEventListener('click', async function () {
  const res = await fetch('/credentials', { method: 'POST' });
  if (!res.ok) {
    alert('failed to generate token');
    return;
  }
  const data = await res.json();
  document.getElementById('new-token-value').value = data.token;
  document.getElementById('new-token').style.display = 'block';
  refreshCredentials();
});

document.getElementById('refresh-btn').addEventListener('click', refreshCredentials);

refreshCredentials();
</script>`))

