// WAVE Agent Local Web UI
// Embedded HTML/JS dashboard served by the Go agent
// Accessible at http://<device-ip>:8080/
package main

import (
	"net/http"
)

const webUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>WAVE Edge</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
:root {
  --bg-body: #050508; --bg-section: #0a0a12; --bg-card: #12121e;
  --text-primary: #ffffff; --text-secondary: #a1a1b5; --text-muted: #6b6b80;
  --primary-500: oklch(55% 0.22 265); --primary-400: oklch(65% 0.20 265);
  --success-500: oklch(65% 0.20 145); --warning-500: oklch(75% 0.18 85);
  --destructive-500: oklch(55% 0.22 25); --border-subtle: rgba(255,255,255,0.08);
  --radius: 12px;
}
body { font-family: 'Inter', -apple-system, sans-serif; background: var(--bg-body); color: var(--text-primary); min-height: 100vh; }
.container { max-width: 1200px; margin: 0 auto; padding: 24px; }
header { display: flex; align-items: center; justify-content: space-between; padding: 16px 0; border-bottom: 1px solid var(--border-subtle); margin-bottom: 24px; }
header h1 { font-size: 20px; font-weight: 700; }
header .badge { font-size: 11px; padding: 4px 10px; border-radius: 20px; background: var(--success-500); color: var(--bg-body); font-weight: 600; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 16px; margin-bottom: 24px; }
.card { background: var(--bg-card); border: 1px solid var(--border-subtle); border-radius: var(--radius); padding: 20px; }
.card h2 { font-size: 14px; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 12px; }
.stat { font-size: 32px; font-weight: 700; font-family: 'JetBrains Mono', monospace; }
.stat-label { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.modules { list-style: none; }
.modules li { display: flex; align-items: center; justify-content: space-between; padding: 10px 0; border-bottom: 1px solid var(--border-subtle); }
.modules li:last-child { border-bottom: none; }
.module-name { font-weight: 500; font-size: 14px; }
.module-status { font-size: 12px; padding: 3px 8px; border-radius: 8px; font-weight: 600; }
.module-status.running { background: rgba(34,197,94,0.15); color: var(--success-500); }
.module-status.stopped { background: rgba(239,68,68,0.15); color: var(--destructive-500); }
.module-status.error { background: rgba(245,158,11,0.15); color: var(--warning-500); }
.actions { display: flex; gap: 8px; margin-top: 16px; }
.btn { padding: 8px 16px; border-radius: 8px; border: 1px solid var(--border-subtle); background: var(--bg-section); color: var(--text-primary); cursor: pointer; font-size: 13px; transition: all 0.15s; }
.btn:hover { background: var(--bg-card); border-color: var(--primary-400); }
.btn-primary { background: var(--primary-500); border-color: transparent; color: white; }
.btn-primary:hover { filter: brightness(1.1); }
.network-info { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.network-item { font-size: 13px; }
.network-item .label { color: var(--text-muted); font-size: 11px; }
.network-item .value { font-family: 'JetBrains Mono', monospace; }
.health-bar { height: 6px; background: var(--bg-section); border-radius: 3px; margin-top: 8px; overflow: hidden; }
.health-fill { height: 100%; border-radius: 3px; transition: width 0.3s; }
.health-fill.good { background: var(--success-500); }
.health-fill.warn { background: var(--warning-500); }
.health-fill.bad { background: var(--destructive-500); }
#log-output { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-body); border: 1px solid var(--border-subtle); border-radius: 8px; padding: 12px; max-height: 200px; overflow-y: auto; color: var(--text-secondary); white-space: pre-wrap; }
footer { text-align: center; color: var(--text-muted); font-size: 11px; padding: 24px 0; border-top: 1px solid var(--border-subtle); margin-top: 24px; }
</style>
</head>
<body>
<div class="container">
  <header>
    <h1>WAVE Edge</h1>
    <span class="badge" id="status-badge">Loading</span>
  </header>

  <div class="grid">
    <div class="card">
      <h2>Device</h2>
      <div id="device-info">
        <div class="network-info">
          <div class="network-item"><div class="label">Device ID</div><div class="value" id="device-id">-</div></div>
          <div class="network-item"><div class="label">Platform</div><div class="value" id="platform">-</div></div>
          <div class="network-item"><div class="label">IP address</div><div class="value" id="ip-addr">-</div></div>
          <div class="network-item"><div class="label">Profile</div><div class="value" id="profile">-</div></div>
          <div class="network-item"><div class="label">Agent</div><div class="value" id="agent-ver">-</div></div>
          <div class="network-item"><div class="label">Uptime</div><div class="value" id="uptime">-</div></div>
        </div>
      </div>
    </div>

    <div class="card">
      <h2>Hardware</h2>
      <div class="network-info">
        <div class="network-item"><div class="label">SoC</div><div class="value" id="soc">-</div></div>
        <div class="network-item"><div class="label">RAM</div><div class="value" id="ram">-</div></div>
        <div class="network-item"><div class="label">CPU temp</div><div class="value" id="temp">-</div></div>
        <div class="network-item"><div class="label">Storage</div><div class="value" id="storage">-</div></div>
      </div>
      <div class="health-bar" style="margin-top:12px">
        <div class="health-fill good" id="cpu-bar" style="width:0%"></div>
      </div>
      <div class="stat-label" style="margin-top:4px">CPU temperature</div>
    </div>
  </div>

  <div class="card">
    <h2>Modules</h2>
    <ul class="modules" id="module-list">
      <li><span class="module-name">Loading...</span></li>
    </ul>
    <div class="actions">
      <button class="btn btn-primary" onclick="refreshData()">Refresh</button>
      <button class="btn" onclick="showLogs()">View logs</button>
    </div>
  </div>

  <div class="card" id="log-card" style="display:none;margin-top:16px">
    <h2>Recent logs</h2>
    <div id="log-output">Loading logs...</div>
  </div>

  <footer>WAVE Edge Agent v<span id="footer-ver">0.1.0</span> &mdash; WAVE Online, LLC</footer>
</div>

<script>
async function fetchData() {
  try {
    const res = await fetch('/api/system');
    const data = await res.json();
    updateUI(data);
  } catch (e) {
    document.getElementById('status-badge').textContent = 'Offline';
    document.getElementById('status-badge').style.background = 'var(--destructive-500)';
  }
}

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.textContent;
}

function updateUI(data) {
  const d = data.device || {};
  const hw = data.hardware || {};

  document.getElementById('status-badge').textContent = 'Online';
  document.getElementById('device-id').textContent = d.device_id || '-';
  document.getElementById('platform').textContent = d.platform || '-';
  document.getElementById('profile').textContent = d.profile || 'none';
  document.getElementById('agent-ver').textContent = 'v' + (data.agent_version || '0.1.0');
  document.getElementById('footer-ver').textContent = data.agent_version || '0.1.0';
  document.getElementById('uptime').textContent = data.uptime || '-';

  if (hw.network) {
    document.getElementById('ip-addr').textContent = hw.network.ip || '-';
  }
  if (hw.soc) document.getElementById('soc').textContent = hw.soc;
  if (hw.ram_mb) document.getElementById('ram').textContent = hw.ram_mb + ' MB';
  if (hw.storage_mb) document.getElementById('storage').textContent = Math.round(hw.storage_mb / 1024) + ' GB';
  if (hw.cpu_temp_c !== undefined) {
    const temp = hw.cpu_temp_c;
    document.getElementById('temp').textContent = temp + ' C';
    const pct = Math.min(100, (temp / 85) * 100);
    const bar = document.getElementById('cpu-bar');
    bar.style.width = pct + '%';
    bar.className = 'health-fill ' + (temp < 60 ? 'good' : temp < 75 ? 'warn' : 'bad');
  }

  const modules = data.modules || [];
  const list = document.getElementById('module-list');
  while (list.firstChild) list.removeChild(list.firstChild);
  if (modules.length === 0) {
    const li = document.createElement('li');
    const span = document.createElement('span');
    span.className = 'module-name';
    span.style.color = 'var(--text-muted)';
    span.textContent = 'No modules installed';
    li.appendChild(span);
    list.appendChild(li);
  } else {
    modules.forEach(function(m) {
      const li = document.createElement('li');
      const nameSpan = document.createElement('span');
      nameSpan.className = 'module-name';
      nameSpan.textContent = m.name;
      const statusSpan = document.createElement('span');
      statusSpan.className = 'module-status ' + escapeHTML(m.status);
      statusSpan.textContent = m.status;
      li.appendChild(nameSpan);
      li.appendChild(statusSpan);
      list.appendChild(li);
    });
  }
}

async function showLogs() {
  const card = document.getElementById('log-card');
  card.style.display = 'block';
  document.getElementById('log-output').textContent = 'Agent running. Use journalctl -u wave-agent for full logs.';
}

function refreshData() { fetchData(); }

fetchData();
setInterval(fetchData, 5000);
</script>
</body>
</html>`

// RegisterWebUI adds the web UI route to the HTTP mux
func RegisterWebUI(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(webUIHTML))
	})
}
