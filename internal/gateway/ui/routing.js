/* Traffic map: clients -> gateway -> Cartesia keys.
 *
 * One request to /api/routing returns the snapshot this draws — both columns,
 * their 24h shares, and which key the active strategy would pick next. The SVG
 * edges are re-laid out on every render and on resize, because a stale line
 * after a scroll or a zoom is worse than no line.
 */
'use strict';

let routingState = null;
let routingLayoutQueued = false;

const STRATEGY_LABELS = {
  'least-recently-used': 'Least recently used',
  'round-robin': 'Round robin',
  'random': 'Random',
  'sticky': 'Sticky',
  'priority': 'Priority',
};

const STRATEGY_HINTS = {
  'least-recently-used': 'Key yang paling lama menganggur dipakai lebih dulu, jadi beban rata dan kuota tiap key terpakai bergiliran.',
  'round-robin': 'Bergilir menurut urutan id dan kembali ke awal. Cocok saat Anda ingin pembagian yang bisa diprediksi.',
  'random': 'Dipilih acak dari key yang tidak sedang cooldown. Sederhana, tanpa urutan yang perlu dirawat.',
  'sticky': 'Satu key dipakai terus sampai kena limit. Key lain menyimpan kuotanya untuk cadangan.',
  'priority': 'Key dengan angka prioritas terkecil dicoba lebih dulu; key berikutnya hanya terpakai saat yang utama cooldown atau nonaktif.',
};

function pct(value) {
  const p = Math.round((Number(value) || 0) * 1000) / 10;
  return p + '%';
}

function relativeTime(seconds) {
  if (!seconds) return 'belum dipakai';
  const delta = Math.max(0, Math.floor(Date.now() / 1000) - seconds);
  if (delta < 60) return delta + ' dtk lalu';
  if (delta < 3600) return Math.floor(delta / 60) + ' mnt lalu';
  if (delta < 86400) return Math.floor(delta / 3600) + ' jam lalu';
  return Math.floor(delta / 86400) + ' hari lalu';
}

function routingClientNode(client) {
  const state = !client.isActive ? 'off' : client.expired ? 'off' : 'ok';
  return `<div class="routing-node is-${state}" data-role="client" data-id="${client.id}">
    <span class="rn-dot"></span>
    <span class="rn-text">
      <span class="rn-name">${escapeHTML(client.label || client.prefix + '…')}</span>
      <span class="rn-meta">${client.requests24h || 0} req · ${relativeTime(client.lastSeenTs)}</span>
    </span>
    <span class="rn-share">${pct(client.share)}</span>
  </div>`;
}

function routingKeyNode(key, strategy) {
  const state = !key.isActive ? 'off' : key.cooling ? 'cooling' : 'ok';
  const showPriority = strategy === 'priority';
  return `<div class="routing-node is-${state}${key.nextUp ? ' is-next' : ''}"
               data-role="key" data-id="${key.id}" title="${escapeHTML(key.label || '')}">
    <span class="rn-dot"></span>
    <span class="rn-text">
      <span class="rn-name">${escapeHTML(key.label || key.maskedSecret)}</span>
      <span class="rn-meta">
        ${key.nextUp ? '<span class="rn-tag">berikutnya</span>' : ''}
        ${key.requests24h || 0} req · ${key.cooling ? 'cooldown' : key.isActive ? 'siap' : 'nonaktif'}
        ${showPriority ? `<span class="rn-pri">P<input type="number" min="0" max="100" step="1"
            value="${key.priority}" data-priority-for="${key.id}" title="Prioritas (0 = paling dulu)"
            aria-label="Prioritas key ${escapeHTML(key.label || key.id)}"></span>` : ''}
      </span>
    </span>
    <span class="rn-share">${pct(key.share)}</span>
  </div>`;
}

function renderRouting() {
  const data = routingState;
  if (!data) return;
  const strategy = data.strategy;

  $('routingStrategy').textContent = STRATEGY_LABELS[strategy] || strategy;
  const g = data.gateway || {};
  $('routingCaption').textContent = STRATEGY_HINTS[strategy] || '';
  $('hubSub').textContent = `${g.requests24h || 0} req / ${g.success24h || 0} ok`;
  $('hubPulse').hidden = false;
  $('hubPulse').style.animationPlayState = (g.requests24h || 0) > 0 ? 'running' : 'paused';

  const clients = data.clients || [];
  const keys = data.keys || [];
  $('routingClients').innerHTML = clients.length
    ? clients.map(routingClientNode).join('')
    : '<div class="routing-empty">Belum ada client key.</div>';
  $('routingKeys').innerHTML = keys.length
    ? keys.map((key) => routingKeyNode(key, strategy)).join('')
    : '<div class="routing-empty">Belum ada Cartesia key.</div>';

  queueRoutingLayout();
}

// Recompute the SVG edges so each client card connects to the hub and the hub
// to each key card, with stroke width scaled by that node's 24h share.
function layoutRouting() {
  const canvas = $('routingCanvas');
  const svg = $('routingEdges');
  if (!canvas || !svg || !routingState) return;

  const box = canvas.getBoundingClientRect();
  svg.setAttribute('viewBox', `0 0 ${box.width} ${box.height}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('width', String(box.width));
  svg.setAttribute('height', String(box.height));
  const hub = canvas.querySelector('.hub-core');
  if (!hub) return;
  const hubBox = hub.getBoundingClientRect();
  const hubLeft = { x: hubBox.left - box.left, y: hubBox.top - box.top + hubBox.height / 2 };
  const hubRight = { x: hubBox.right - box.left, y: hubLeft.y };

  const maxShare = Math.max(
    0.0001,
    ...(routingState.clients || []).map((c) => c.share || 0),
    ...(routingState.keys || []).map((k) => k.share || 0),
  );
  const parts = [];
  const edge = (from, to, share, idle) => {
    const width = 1 + 3.2 * Math.sqrt((share || 0) / maxShare);
    const midX = (from.x + to.x) / 2;
    parts.push(`<path class="routing-edge${idle ? ' idle' : ''}"
      d="M ${from.x} ${from.y} C ${midX} ${from.y}, ${midX} ${to.y}, ${to.x} ${to.y}"
      style="stroke-width:${width.toFixed(2)}"></path>`);
  };

  canvas.querySelectorAll('.routing-node[data-role="client"]').forEach((node, i) => {
    const client = (routingState.clients || [])[i];
    if (!client) return;
    const r = node.getBoundingClientRect();
    edge({ x: r.right - box.left, y: r.top - box.top + r.height / 2 }, hubLeft, client.share, !client.isActive);
  });
  canvas.querySelectorAll('.routing-node[data-role="key"]').forEach((node, i) => {
    const key = (routingState.keys || [])[i];
    if (!key) return;
    const r = node.getBoundingClientRect();
    edge(hubRight, { x: r.left - box.left, y: r.top - box.top + r.height / 2 }, key.share,
      !key.isActive || key.cooling);
  });
  svg.innerHTML = parts.join('');
}

function queueRoutingLayout() {
  if (routingLayoutQueued) return;
  routingLayoutQueued = true;
  requestAnimationFrame(() => {
    routingLayoutQueued = false;
    layoutRouting();
  });
}

async function loadRouting() {
  try {
    const data = await apiJSON('/api/routing');
    routingState = { ...data, clients: data.clients || [], keys: data.keys || [] };
    renderRouting();
  } catch (error) {
    $('routingKeys').innerHTML = `<div class="routing-empty">Gagal memuat peta: ${escapeHTML(error.message)}</div>`;
  }
}

async function changeKeyPriority(id, value) {
  const priority = Math.max(0, Math.min(100, Number(value)));
  try {
    await apiJSON(`/api/keys/${id}`, { method: 'PATCH', body: JSON.stringify({ priority }) });
    toast('prioritas key #' + id + ' → ' + priority);
    loadRouting();
  } catch (error) {
    toast(error.message, true);
    loadRouting();
  }
}
