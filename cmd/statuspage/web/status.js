const labels = {
	operational: 'Работает',
	degraded: 'Деградация',
	down: 'Недоступно',
	investigating: 'Расследуем',
	identified: 'Причина найдена',
	monitoring: 'Наблюдаем',
	resolved: 'Решено',
	minor: 'Низкая',
	major: 'Высокая',
	critical: 'Критичная',
};

const overall = document.getElementById('overall');
const overallLabel = document.getElementById('overall-label');
const checkedAt = document.getElementById('checked-at');
const services = document.getElementById('services');
const incidents = document.getElementById('incidents');
const detail = document.getElementById('detail');
const detailTitle = document.getElementById('detail-title');
const detailBody = document.getElementById('detail-body');

function escapeHtml(value) {
	return String(value ?? '').replace(/[&<>"']/g, (char) => ({
		'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
	}[char]));
}

function date(value) {
	if (!value) return '—';
	return new Date(value).toLocaleString('ru-RU', { dateStyle: 'medium', timeStyle: 'short' });
}

function render(data) {
	overall.className = `overall ${data.status}`;
	overallLabel.textContent = labels[data.status] ?? data.status;
	checkedAt.textContent = `Проверено ${date(data.checkedAt)}`;
	services.innerHTML = data.services.map((service) => `
		<article class="service ${service.state}">
			<div class="service-head"><strong>${escapeHtml(service.name)}</strong><span class="pill">${escapeHtml(labels[service.state] ?? service.state)}</span></div>
			<div class="muted">${escapeHtml(service.detail)}${service.latencyMs ? ` · ${service.latencyMs} ms` : ''}</div>
			${service.uptime != null ? `<div class="uptime">UP ${service.uptime.toFixed(2)}%</div>` : ''}
		</article>
	`).join('');
	if (!data.incidents.length) {
		incidents.innerHTML = '<p class="muted">Активных и недавних инцидентов нет.</p>';
		return;
	}
	incidents.innerHTML = data.incidents.map((incident) => `
		<article class="incident ${incident.status}">
			<div class="incident-head">
				<div class="incident-names"><strong>${escapeHtml(incident.codename || incident.code)}</strong><span class="code">${escapeHtml(incident.code)}</span></div>
				<span class="pill">${escapeHtml(labels[incident.status] ?? incident.status)}</span>
			</div>
			<h3>${escapeHtml(incident.title)}</h3>
			${incident.description ? `<p class="muted">${escapeHtml(incident.description)}</p>` : ''}
			<div class="incident-meta">
				<span>Критичность: ${escapeHtml(labels[incident.severity] ?? incident.severity)}</span>
				<span>Начало: ${date(incident.startedAt)}</span>
				${incident.resolvedAt ? `<span>Решено: ${date(incident.resolvedAt)}</span>` : ''}
				<button class="detail-button" type="button" data-id="${incident.id}">Подробнее</button>
			</div>
		</article>
	`).join('');
	document.querySelectorAll('.detail-button').forEach((button) => {
		button.addEventListener('click', () => showDetail(button.dataset.id));
	});
}

async function load() {
	try {
		const response = await fetch('/rgg-sync/status', { headers: { accept: 'application/json' } });
		if (!response.ok) throw new Error(`HTTP ${response.status}`);
		render(await response.json());
	} catch (error) {
		overall.className = 'overall down';
		overallLabel.textContent = 'Нет связи со статус-сервисом';
		checkedAt.textContent = String(error);
	}
}

document.getElementById('refresh').addEventListener('click', load);
load();
setInterval(load, 30000);

async function showDetail(id) {
	const response = await fetch(`/rgg-sync/status/incidents/${id}`, { headers: { accept: 'application/json' } });
	if (!response.ok) return;
	const incident = await response.json();
	detail.classList.remove('hidden');
	detailTitle.textContent = `${incident.codename || incident.code} · ${incident.code}`;
	detailBody.innerHTML = `
		<h3>${escapeHtml(incident.title)}</h3>
		<p class="muted">${escapeHtml(incident.description || '')}</p>
		<div class="incident-meta">
			<span>Статус: ${escapeHtml(labels[incident.status] ?? incident.status)}</span>
			<span>Критичность: ${escapeHtml(labels[incident.severity] ?? incident.severity)}</span>
			<span>Начало: ${date(incident.startedAt)}</span>
		</div>
		<h3 class="timeline-title">Хронология</h3>
		<div class="timeline">
			${(incident.updates || []).map((update) => `
				<div class="timeline-item">
					<span class="timeline-dot ${update.status}"></span>
					<div><strong>${escapeHtml(labels[update.status] ?? (update.status || 'Обновление'))}</strong><div>${escapeHtml(update.message)}</div><span class="muted">${date(update.createdAt)}</span></div>
				</div>
			`).join('') || `
				<div class="timeline-item">
					<span class="timeline-dot ${incident.status}"></span>
					<div><strong>${escapeHtml(labels[incident.status] ?? incident.status)}</strong><div>Инцидент создан</div><span class="muted">${date(incident.startedAt)}</span></div>
				</div>`}
		</div>`;
	detail.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

document.getElementById('detail-close').addEventListener('click', () => detail.classList.add('hidden'));
