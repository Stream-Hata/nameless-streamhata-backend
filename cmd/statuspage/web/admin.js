const $ = (id) => document.getElementById(id);
let incidents = [];

function token() { return sessionStorage.getItem('statusAdminToken') || ''; }
function headers() {
	const result = { 'Content-Type': 'application/json', accept: 'application/json' };
	if (token()) result['X-Status-Token'] = token();
	return result;
}
function esc(value) { return String(value ?? '').replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char])); }

async function load() {
	const response = await fetch('/rgg-sync/status/incidents', { headers: headers() });
	if (!response.ok) throw new Error(`HTTP ${response.status}`);
	incidents = (await response.json()).items || [];
	$('incident-list').innerHTML = incidents.map((incident) => `
		<article class="incident ${incident.status}">
			<div class="incident-head">
				<div class="incident-names"><strong>${esc(incident.codename || incident.code)}</strong><span class="code">${esc(incident.code)}</span></div>
				<span class="pill">${esc(incident.status)}</span>
			</div>
			<div>${esc(incident.title)}</div>
			<button class="detail-button" type="button" data-id="${incident.id}">Выбрать</button>
		</article>
	`).join('') || '<p class="muted">Инцидентов нет.</p>';
	const select = $('incident-id');
	select.innerHTML = '<option value="">Новый инцидент</option>' + incidents.map((incident) =>
		`<option value="${incident.id}">${esc(incident.code)} · ${esc(incident.codename)} · ${esc(incident.title)}</option>`).join('');
	document.querySelectorAll('[data-id]').forEach((button) => button.addEventListener('click', () => selectIncident(Number(button.dataset.id))));
}

function selectIncident(id) {
	const incident = incidents.find((item) => item.id === id);
	if (!incident) return;
	$('incident-id').value = String(id);
	$('code').value = incident.code;
	$('codename').value = incident.codename;
	$('severity').value = incident.severity;
	$('status').value = incident.status;
	$('title').value = incident.title;
	$('description').value = incident.description;
	$('message').value = '';
}

async function create() {
	const body = {
		code: $('code').value, codename: $('codename').value, title: $('title').value,
		description: $('description').value, severity: $('severity').value,
		status: $('status').value, message: $('message').value,
	};
	const response = await fetch('/rgg-sync/status/incidents', { method: 'POST', headers: headers(), body: JSON.stringify(body) });
	$('result').textContent = response.ok ? 'Инцидент создан.' : `Ошибка: ${response.status}`;
	if (response.ok) await load();
}

async function update() {
	const id = Number($('incident-id').value);
	if (!id) { $('result').textContent = 'Выбери инцидент.'; return; }
	const body = {
		title: $('title').value, description: $('description').value,
		severity: $('severity').value, status: $('status').value,
		codename: $('codename').value, message: $('message').value,
	};
	const response = await fetch(`/rgg-sync/status/incidents/${id}`, { method: 'PATCH', headers: headers(), body: JSON.stringify(body) });
	$('result').textContent = response.ok ? 'Инцидент обновлён.' : `Ошибка: ${response.status}`;
	if (response.ok) await load();
}

$('token').value = token();
$('save-token').addEventListener('click', () => { sessionStorage.setItem('statusAdminToken', $('token').value.trim()); $('token-state').textContent = 'Токен сохранён в этой вкладке.'; });
$('reload').addEventListener('click', () => load().catch((error) => { $('result').textContent = String(error); }));
$('create').addEventListener('click', () => create().catch((error) => { $('result').textContent = String(error); }));
$('update').addEventListener('click', () => update().catch((error) => { $('result').textContent = String(error); }));
load().catch((error) => { $('result').textContent = String(error); });
