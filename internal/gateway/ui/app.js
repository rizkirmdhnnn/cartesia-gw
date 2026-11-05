/* Dashboard for cartesia-gw. No framework and no imports: one file that the
   embedded binary can serve without any build step.
   Styled for the "Relay" Warm-Minimalist Editorial SaaS Design System. */
'use strict';

const $ = (id) => document.getElementById(id);

const PREFS_KEY = 'cartesia-gw-prefs';

let overview = null;
let audio = null;              // the generated clip, for download without re-billing
let streamCtl = null;          // AbortController for an in-flight generation
let streamUrl = null;          // object URL currently attached to the player
const previewAudio = new Audio();
let previewButton = null;

const voiceState = { items: [], cursor: null, hasMore: false, loading: false, selected: null };
const usageState = { offset: 0, limit: 50, total: 0 };

// Key selection strategies. The hint is what makes the choice understandable
// without reading the README.
const ROTATIONS = {
  'least-recently-used': {
    label: 'Least recently used',
    hint: 'Key yang paling lama tidak dipakai dipilih lebih dulu. Menyebar rata, dan menyesuaikan sendiri saat key ditambah atau dihapus.',
  },
  'round-robin': {
    label: 'Round robin',
    hint: 'Bergilir berurutan menurut id, kembali ke key pertama setelah yang terakhir. Urutannya bisa diprediksi.',
  },
  'random': {
    label: 'Random',
    hint: 'Dipilih acak dari key yang tidak sedang diparkir.',
  },
  'sticky': {
    label: 'Sticky',
    hint: 'Satu key dipakai terus sampai kena limit, jadi kuota key lain tetap utuh.',
  },
  'priority': {
    label: 'Priority',
    hint: 'Key dengan prioritas terkecil dicoba lebih dulu. Isi angka prioritas di kartu key pada Traffic map — key berikutnya hanya terpakai saat yang utama cooldown atau nonaktif.',
  },
};

/* ── SVG Icons ───────────────────────────────────────────────────────────── */
const ICONS = {
  key: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="7.5" cy="15.5" r="5.5"></circle><path d="m21 2-9.6 9.6"></path><path d="m15.5 7.5 3 3L22 7l-3-3"></path></svg>`,
  pulse: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"></polyline></svg>`,
  check: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"></path><polyline points="22 4 12 14.01 9 11.01"></polyline></svg>`,
  text: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 7 4 4 20 4 20 7"></polyline><line x1="9" x2="15" y1="20" y2="20"></line><line x1="12" x2="12" y1="4" y2="20"></line></svg>`,
  speed: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"></polygon></svg>`,
  version: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><polyline points="12 6 12 12 14 14"></polyline></svg>`,
  warn: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"></path><line x1="12" x2="12" y1="9" y2="13"></line><line x1="12" x2="12.01" y1="17" y2="17"></line></svg>`,
  sound: `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><path d="M15.54 8.46a5 5 0 0 1 0 7.07"></path></svg>`,
};

/* ── preferences ─────────────────────────────────────────────────────────── */
function loadPrefs() {
  try { return JSON.parse(localStorage.getItem(PREFS_KEY) || '{}'); } catch { return {}; }
}
function savePrefs(patch) {
  localStorage.setItem(PREFS_KEY, JSON.stringify({ ...loadPrefs(), ...patch }));
}

/* ── small helpers ───────────────────────────────────────────────────────── */
function toast(message, bad) {
  const el = $('toast');
  el.textContent = message;
  el.className = 'toast show' + (bad ? ' bad' : '');
  clearTimeout(el._timer);
  el._timer = setTimeout(() => { el.className = 'toast' + (bad ? ' bad' : ''); }, 4200);
}

function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

const fmtTime = (seconds) => seconds ? new Date(seconds * 1000).toLocaleString() : '—';
const fmtBytes = (n) => n >= 1048576 ? (n / 1048576).toFixed(1) + ' MB'
  : n >= 1024 ? (n / 1024).toFixed(0) + ' KB' : (n || 0) + ' B';
const shortID = (id) => (id || '').length > 14 ? id.slice(0, 8) + '…' : (id || '');
const num = (id) => Number($(id).value);

async function errorMessage(res) {
  const text = await res.text().catch(() => '');
  try {
    const body = JSON.parse(text);
    if (body?.error?.message) return body.error.message;
  } catch { /* not json */ }
  return text || `HTTP ${res.status}`;
}

/* ── micro spark bars helper (Section 4.F) ───────────────────────────────── */
function renderSparkBars(latencyMs, isOk) {
  const heights = [6, 8, 12, 10, 14, 11, 16];
  return `<span class="spark-bars" title="${latencyMs} ms">${heights.map((h, i) => {
    const isLast = i === heights.length - 1;
    const cls = isLast ? (isOk ? 'active' : 'warn') : '';
    return `<span class="spark-bar ${cls}" style="height: ${h}px;"></span>`;
  }).join('')}</span>`;
}

/* ── transport ───────────────────────────────────────────────────────────── */
// Every /api call rides the session cookie (credentials included for the same
// origin) and carries the CSRF marker. On 401 the session is gone, so the gate
// is shown again rather than a token prompt.
async function api(path, options) {
  const opts = { ...(options || {}), headers: { ...(options?.headers || {}) }, credentials: 'same-origin' };
  opts.headers[CSRF_HEADER] = CSRF_VALUE;
  if (opts.body && !opts.headers['Content-Type']) opts.headers['Content-Type'] = 'application/json';
  const res = await fetch(path, opts);
  if (res.status === 401) {
    showLogin('sesi berakhir — silakan masuk lagi');
    throw new Error('sesi berakhir');
  }
  return res;
}

async function apiJSON(path, options, wantStatus) {
  const res = await api(path, options);
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { /* not json */ }
  if (!res.ok) throw new Error(body?.error?.message || text || `HTTP ${res.status}`);
  if (wantStatus && res.status !== wantStatus) throw new Error(`HTTP ${res.status}`);
  return body;
}

/* ── 4.D KPI / Summary Strip & Overview ──────────────────────────────────── */
// chip is one cell of the KPI ledger: a label, a value, and an optional state
// word. "hot" marks the cell the operator should read first (requests in
// flight); it earns the lime highlight, nothing else does.
function chip(label, value, state, hot) {
  return `<div class="chip${hot ? ' hot' : ''}">
    <span class="chip-label">${escapeHTML(label)}</span>
    <b>${escapeHTML(value)}</b>
    ${state ? `<span class="chip-label">${escapeHTML(state)}</span>` : ''}
  </div>`;
}

function renderOverview() {
  const u = overview.usage || {};
  const k = overview.keys || {};
  const rate = u.requests24h ? Math.round((u.success24h / u.requests24h) * 100) : null;
  
  $('chips').innerHTML = [
    chip('24 jam', `${u.requests24h || 0}`, 'req', true),
    rate === null ? chip('sukses', '—', '') : chip('sukses', `${rate}%`, rate >= 95 ? 'optimal' : 'perlu cek'),
    chip('karakter', (u.charsToday || 0).toLocaleString(), 'disintesis'),
    chip('latensi', `${u.avgLatencyMs24h || 0} ms`, 'rata-rata'),
    chip('keys', `${k.active || 0}/${k.total || 0}`, k.cooling ? `${k.cooling} cooldown` : 'aktif'),
    chip('client', `${overview.clients?.active ?? 0}`, 'key aktif'),
    chip('versi', overview.version, 'build'),
  ].join('');

  // Update sidebar counter badge
  const keysBadge = $('keysNavBadge');
  if (keysBadge) {
    keysBadge.textContent = `${k.active || 0}/${k.total || 0}`;
    if (k.cooling) {
      keysBadge.classList.add('urgent');
    } else {
      keysBadge.classList.remove('urgent');
    }
  }
}

async function loadOverview() {
  overview = await apiJSON('/api/overview');
  const prefs = loadPrefs();
  const controls = overview.controls || {};

  if (!$('model').options.length) {
    $('model').innerHTML = overview.models.map((m) =>
      `<option value="${escapeHTML(m)}">${escapeHTML(m)}</option>`).join('');
    $('model').value = prefs.model && overview.models.includes(prefs.model) ? prefs.model : overview.defaultModel;
    $('format').value = prefs.format || 'mp3';

    const [minSpeed, maxSpeed] = controls.speedRange || [0.6, 1.5];
    $('speed').min = minSpeed; $('speed').max = maxSpeed;
    const [minVolume, maxVolume] = controls.volumeRange || [0.5, 2];
    $('volume').min = minVolume; $('volume').max = maxVolume;
    $('speed').value = prefs.speed ?? 1;
    $('volume').value = prefs.volume ?? 1;

    const primary = new Set(controls.primaryEmotions || []);
    const all = controls.emotions || [];
    const primaryOptions = all.filter((e) => primary.has(e));
    const restOptions = all.filter((e) => !primary.has(e));
    $('emotion').innerHTML =
      '<option value="">(biarkan model memilih)</option>'
      + primaryOptions.map((e) => `<option value="${e}">${e}</option>`).join('')
      + (restOptions.length ? `<optgroup label="lainnya">${restOptions.map((e) => `<option value="${e}">${e}</option>`).join('')}</optgroup>` : '');
    $('emotion').value = prefs.emotion || '';
  }
  updateRangeOutputs();
  renderOverview();
  renderRotation();
  updateCurl();
}

function renderRotation() {
  const select = $('rotation');
  const values = overview.rotations || [];
  if (!select.options.length) {
    select.innerHTML = values.map((v) =>
      `<option value="${escapeHTML(v)}">${escapeHTML(ROTATIONS[v]?.label || v)}</option>`).join('');
  }
  select.value = overview.rotation || values[0] || '';
  $('rotationHint').textContent = ROTATIONS[overview.rotation]?.hint || '';
}

async function changeRotation() {
  const select = $('rotation');
  try {
    const body = await apiJSON('/api/settings', {
      method: 'PATCH',
      body: JSON.stringify({ rotation: select.value }),
    });
    overview.rotation = body.rotation;
    renderRotation();
    toast('rotasi: ' + (ROTATIONS[body.rotation]?.label || body.rotation));
  } catch (err) {
    select.value = overview.rotation; // the server kept the old one
    toast(err.message, true);
  }
}

function updateRangeOutputs() {
  $('speedOut').textContent = num('speed').toFixed(2);
  $('volumeOut').textContent = num('volume').toFixed(2);
}

/* ── voice library ───────────────────────────────────────────────────────── */
function voiceQuery(reset) {
  const params = new URLSearchParams();
  const q = $('voiceSearch').value.trim();
  const lang = $('langFilter').value.trim();
  const gender = $('genderFilter').value;
  if (q) params.set('q', q);
  if (lang) params.set('language', lang);
  if (gender) params.set('gender', gender);
  params.set('limit', '50');
  if (!reset && voiceState.cursor) params.set('starting_after', voiceState.cursor);
  return params;
}

function voiceLocale(v) {
  const accent = Array.isArray(v.accents) && v.accents[0];
  return (accent && accent.locale) || v.language || '';
}

function renderVoices() {
  const list = $('voiceList');
  if (!voiceState.items.length) {
    list.innerHTML = '<p class="empty">Tidak ada voice yang cocok. Coba hapus filter atau muat ulang.</p>';
  } else {
    list.innerHTML = voiceState.items.map((v) => {
      const selected = v.id === voiceState.selected?.id;
      const tags = [
        voiceLocale(v) ? `<span class="tag">${escapeHTML(voiceLocale(v))}</span>` : '',
        v.gender ? `<span class="tag">${escapeHTML(v.gender.replace('_', ' '))}</span>` : '',
        v.is_pro ? '<span class="tag tag-pro">pro</span>' : '',
        v.is_owner ? '<span class="tag">milik sendiri</span>' : '',
      ].join('');
      const desc = v.tagline || v.description || '';
      return `<div class="voice-card" role="option" tabindex="0" data-id="${escapeHTML(v.id)}"
                   aria-selected="${selected ? 'true' : 'false'}"
                   title="${escapeHTML(v.name || v.id)}">
        <button class="play-btn" data-play="${escapeHTML(v.id)}" title="Putar contoh${desc ? ' — ' + escapeHTML(desc) : ''}">▶</button>
        <span class="vc-text">
          <span class="vc-name">${escapeHTML(v.name || v.id)}</span>
          <span class="vc-id" title="Voice ID Cartesia">
            <code>${escapeHTML(v.id)}</code>
            <button class="vc-copy" type="button" data-copy="${escapeHTML(v.id)}" title="Salin voice ID">salin</button>
          </span>
          <span class="vc-meta">${tags}</span>
          ${desc ? `<span class="vc-desc">${escapeHTML(desc)}</span>` : ''}
        </span>
      </div>`;
    }).join('');
  }
  $('loadMore').hidden = !voiceState.hasMore;
  $('voiceCount').textContent = voiceState.items.length
    ? `${voiceState.items.length} voice ditampilkan` : '';

  const voiceBadge = $('voiceNavBadge');
  if (voiceBadge) voiceBadge.textContent = String(voiceState.items.length);
}

async function loadVoices(reset) {
  if (voiceState.loading) return;
  voiceState.loading = true;
  $('loadMore').disabled = true;
  if (reset) {
    voiceState.cursor = null;
    $('voiceList').innerHTML = '<p class="empty">Memuat voice…</p>';
  }
  try {
    const body = await apiJSON('/api/voices?' + voiceQuery(reset).toString());
    const page = body?.data || [];
    voiceState.items = reset ? page : voiceState.items.concat(page);
    const last = voiceState.items[voiceState.items.length - 1];
    voiceState.cursor = body?.next_page || (last ? last.id : null);
    voiceState.hasMore = Boolean(body?.has_more) && Boolean(voiceState.cursor);
    const remembered = loadPrefs().voice;
    if (!voiceState.selected && remembered) {
      voiceState.selected = voiceState.items.find((v) => v.id === remembered) || null;
    }
    renderVoices();
    renderSelectedVoice();
  } catch (err) {
    $('voiceList').innerHTML = `<p class="empty">Gagal memuat voice: ${escapeHTML(err.message)}</p>`;
  } finally {
    voiceState.loading = false;
    $('loadMore').disabled = false;
  }
}

function selectVoice(voice) {
  voiceState.selected = voice;
  savePrefs({ voice: voice.id });
  renderVoices();
  renderSelectedVoice();
  updateCurl();
}

// copyVoiceID puts the id on the clipboard. The click is handled before the
// card's selection so copying never changes the selected voice.
async function copyVoiceID(id, button) {
  try {
    await navigator.clipboard.writeText(id);
    toast('voice ID disalin: ' + id);
  } catch {
    toast('salin manual: ' + id, true);
  }
  if (button) {
    const label = button.textContent;
    button.textContent = 'tersalin';
    setTimeout(() => { button.textContent = label; }, 1400);
  }
}

function renderSelectedVoice() {
  const v = voiceState.selected;
  $('voiceName').textContent = v ? (v.name || v.id) : 'belum dipilih';
  $('voiceMeta').textContent = v
    ? [v.id, voiceLocale(v), v.gender].filter(Boolean).join(' · ')
    : 'pilih dari pustaka di sebelah, lalu klik Generate';
  $('voicePlaySelected').disabled = !v;
  $('speak').disabled = !v;
}

/* ── voice preview ───────────────────────────────────────────────────────── */
function setPreviewButton(id, state) {
  const button = $(`button[data-play="${CSS.escape(id)}"]`);
  if (button) button.dataset.state = state;
}

async function playPreview(id) {
  if (previewButton && previewButton.dataset.state === 'playing') {
    previewAudio.pause();
    previewButton.dataset.state = '';
    previewButton.textContent = '▶';
    if (previewButton.dataset.play === id) { previewButton = null; return; }
  }
  if (previewButton) { previewButton.dataset.state = ''; previewButton.textContent = '▶'; }

  const button = $(`button[data-play="${CSS.escape(id)}"]`);
  previewButton = button;
  setPreviewButton(id, 'loading');
  if (button) button.textContent = '…';
  try {
    const res = await api(`/v1/audio/voices/${encodeURIComponent(id)}/preview`);
    if (!res.ok) throw new Error(await errorMessage(res));
    const blob = await res.blob();
    previewAudio.src = URL.createObjectURL(blob);
    previewAudio.onended = previewAudio.onerror = () => {
      if (button) { button.dataset.state = ''; button.textContent = '▶'; }
    };
    setPreviewButton(id, 'playing');
    if (button) button.textContent = '■';
    await previewAudio.play();
  } catch (err) {
    setPreviewButton(id, '');
    if (button) button.textContent = '▶';
    toast(err.message, true);
  }
}

/* ── generation ──────────────────────────────────────────────────────────── */
function requestBody() {
  const body = {
    model: $('model').value,
    input: $('text').value,
    voice: voiceState.selected?.id,
    response_format: $('format').value,
  };
  const emotion = $('emotion').value;
  if (emotion) body.emotion = emotion;
  if (num('speed') !== 1) body.speed = num('speed');
  if (num('volume') !== 1) body.volume = num('volume');
  return body;
}

function streamSupported() {
  if ($('format').value !== 'mp3') return false;
  try {
    return typeof MediaSource !== 'undefined' && MediaSource.isTypeSupported('audio/mpeg');
  } catch {
    return false;
  }
}

async function generate() {
  const voice = voiceState.selected;
  if (!voice) { toast('pilih voice dulu', true); return; }
  const text = $('text').value.trim();
  if (!text) { toast('isi teksnya dulu', true); return; }

  const body = requestBody();
  const status = $('speakStatus');
  const controller = new AbortController();
  streamCtl = controller;
  setBusy(true);
  status.textContent = streamSupported() ? 'mengalir…' : 'menyintesis…';

  try {
    if (streamSupported()) {
      await generateStreaming(body, controller, status);
    } else {
      await generateBuffered(body, controller, status);
    }
  } catch (err) {
    if (err.name === 'AbortError') {
      status.textContent = 'dihentikan';
    } else {
      status.textContent = '';
      toast(err.message, true);
    }
    refreshUsage();
  } finally {
    streamCtl = null;
    setBusy(false);
  }
}

async function generateStreaming(body, controller, status) {
  const res = await api('/api/test', { method: 'POST', body: JSON.stringify(body), signal: controller.signal });
  if (!res.ok) throw new Error(await errorMessage(res));
  if (!res.body) throw new Error('respons tidak punya body');

  const mediaSource = new MediaSource();
  if (streamUrl) URL.revokeObjectURL(streamUrl);
  streamUrl = URL.createObjectURL(mediaSource);
  const player = $('audio');
  player.hidden = false;
  player.src = streamUrl;

  await new Promise((resolve, reject) => {
    mediaSource.addEventListener('sourceopen', resolve, { once: true });
    mediaSource.addEventListener('error', () => reject(new Error('media source gagal dibuka')), { once: true });
  });

  const source = mediaSource.addSourceBuffer('audio/mpeg');
  const reader = res.body.getReader();
  const chunks = [];
  const started = performance.now();
  let bytes = 0;
  let playing = false;

  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!value?.byteLength) continue;
      chunks.push(value);
      bytes += value.byteLength;
      // The meter moves only on real bytes received: a declared total when the
      // upstream sends one, otherwise a slow asymptote so it never claims done
      // while audio is still arriving.
      const declared = Number(res.headers.get('X-Audio-Bytes') || 0);
      const ratio = declared ? Math.min(bytes / declared, 1) : Math.min(bytes / 400000, 1);
      $('streamFill').style.transform = `scaleX(${ratio.toFixed(3)})`;
      status.textContent = `mengalir… ${fmtBytes(bytes)}`;
      await appendChunk(source, value);
      if (!playing) {
        playing = true;
        status.textContent = `${fmtBytes(bytes)} · mulai dalam ${Math.round(performance.now() - started)} ms`;
        player.play().catch(() => { /* the controls are right there */ });
      }
    }
    if (mediaSource.readyState === 'open') mediaSource.endOfStream();
  } catch (err) {
    if (mediaSource.readyState === 'open') { try { mediaSource.endOfStream(); } catch { /* already closed */ } }
    throw err;
  }

  const blob = new Blob(chunks, { type: 'audio/mpeg' });
  audio = { blob, ext: 'mp3' };
  $('download').disabled = false;
  status.textContent = `${fmtBytes(bytes)} · ${Math.round(performance.now() - started)} ms`;
  refreshUsage();
}

async function appendChunk(source, chunk) {
  await new Promise((resolve, reject) => {
    const done = () => { cleanup(); resolve(); };
    const fail = () => { cleanup(); reject(new Error('append audio gagal')); };
    function cleanup() {
      source.removeEventListener('updateend', done);
      source.removeEventListener('error', fail);
    }
    source.addEventListener('updateend', done);
    source.addEventListener('error', fail);
    try {
      source.appendBuffer(chunk);
    } catch (err) {
      cleanup();
      reject(err);
    }
  });
}

async function generateBuffered(body, controller, status) {
  const started = performance.now();
  const res = await api('/api/test', { method: 'POST', body: JSON.stringify(body), signal: controller.signal });
  if (!res.ok) throw new Error(await errorMessage(res));
  const blob = await res.blob();

  if (streamUrl) { URL.revokeObjectURL(streamUrl); streamUrl = null; }
  const player = $('audio');
  player.hidden = false;
  player.src = URL.createObjectURL(blob);
  player.play().catch(() => { /* the controls are right there */ });

  audio = { blob, ext: $('format').value };
  $('download').disabled = false;
  status.textContent = `${fmtBytes(blob.size)} · ${Math.round(performance.now() - started)} ms`;
  refreshUsage();
}

function stop() {
  if (streamCtl) streamCtl.abort();
  $('audio').pause();
}

function setBusy(busy) {
  $('speak').disabled = busy || !voiceState.selected;
  $('stop').disabled = !busy;
  $('speak').textContent = busy ? 'Generating…' : 'Generate';
  const meter = $('streamMeter');
  if (meter) {
    meter.hidden = !busy;
    // Each run starts empty; the fill advances as audio chunks arrive.
    $('streamFill').style.transform = 'scaleX(0)';
  }
}

function download() {
  if (!audio) return;
  const url = URL.createObjectURL(audio.blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = `speech.${audio.ext}`;
  link.click();
  URL.revokeObjectURL(url);
}

function updateCurl() {
  const voice = voiceState.selected?.id || '<voice-id>';
  const body = {
    model: $('model').value,
    input: 'Halo dunia',
    voice,
    response_format: $('format').value,
  };
  if ($('emotion').value) body.emotion = $('emotion').value;
  if (num('speed') !== 1) body.speed = num('speed');
  if (num('volume') !== 1) body.volume = num('volume');
  const origin = location.origin;
  $('curl').textContent =
    `curl -X POST ${origin}/v1/audio/speech \\\n`
    + `  -H "Authorization: Bearer $CLIENT_API_KEY" \\\n`
    + `  -H "Content-Type: application/json" \\\n`
    + `  -d '${JSON.stringify(body)}' \\\n`
    + `  --output speech.${$('format').value === 'pcm' ? 'pcm' : $('format').value}`;
}

/* ── 4.E Riwayat (Usage Table with Leading Entity Cells & Spark-Bars) ─────── */
function renderRequests(body) {
  usageState.total = body.total || 0;
  const rows = body.data || [];
  if (!rows.length) {
    $('usageTable').innerHTML = '<p class="empty">Belum ada request.</p>';
  } else {
    $('usageTable').innerHTML = '<div class="table-wrap"><table><thead><tr>'
      + '<th><span class="th-content">' + ICONS.sound + ' Model & Waktu</span></th>'
      + '<th><span class="th-content">' + ICONS.text + ' Voice</span></th>'
      + '<th><span class="th-content">Karakter</span></th>'
      + '<th class="hide-sm"><span class="th-content">Ukuran</span></th>'
      + '<th><span class="th-content">' + ICONS.check + ' Status</span></th>'
      + '<th class="hide-sm"><span class="th-content">' + ICONS.speed + ' Latensi</span></th>'
      + '<th class="hide-sm"><span class="th-content">' + ICONS.warn + ' Error</span></th>'
      + '</tr></thead><tbody>'
      + rows.map((r) => {
        const isOk = r.status >= 200 && r.status < 300;
        return `<tr>
          <td>
            <div class="entity-cell">
              <div class="entity-badge ${isOk ? 'success' : 'warning'}">
                ${ICONS.sound}
              </div>
              <div class="entity-stack">
                <span class="entity-title mono">${escapeHTML(r.model)}</span>
                <span class="entity-desc table-time">${escapeHTML(fmtTime(r.ts))}</span>
              </div>
            </div>
          </td>
          <td class="mono" title="${escapeHTML(r.voiceId)}">${escapeHTML(shortID(r.voiceId))}</td>
          <td class="tabular">${(r.chars || 0).toLocaleString()}</td>
          <td class="hide-sm tabular">${r.bytes ? fmtBytes(r.bytes) : '—'}</td>
          <td><span class="pill ${isOk ? 'ok' : 'err bad'}">${r.status}</span></td>
          <td class="hide-sm tabular">${r.latencyMs} ms ${renderSparkBars(r.latencyMs, isOk)}</td>
          <td class="hide-sm err-text">${escapeHTML((r.error || '').slice(0, 90))}</td>
        </tr>`;
      }).join('')
      + '</tbody></table></div>';
  }
  const from = usageState.total ? usageState.offset + 1 : 0;
  const to = Math.min(usageState.offset + usageState.limit, usageState.total);
  $('pageInfo').textContent = `${from}–${to} dari ${usageState.total}`;
  $('pageNewer').disabled = usageState.offset === 0;
  $('pageOlder').disabled = usageState.offset + usageState.limit >= usageState.total;

  const usageBadge = $('usageNavBadge');
  if (usageBadge) usageBadge.textContent = String(usageState.total);
}

async function loadRequests() {
  renderRequests(await apiJSON(`/api/requests?limit=${usageState.limit}&offset=${usageState.offset}`));
}

function refreshUsage() {
  Promise.all([loadOverview(), loadRequests()]).catch(() => { /* the table can wait */ });
  loadKeys().catch(() => { /* the badge can wait */ });
}

/* ── 4.E API Keys Table with Leading Entity Badges ───────────────────────── */
function renderKeys(keys) {
  if (!keys.length) {
    $('keysTable').innerHTML = '<p class="empty">Belum ada key. Tambahkan satu di atas — '
      + 'tanpa key, endpoint menjawab 503.</p>';
    return;
  }
  const now = Math.floor(Date.now() / 1000);
  $('keysTable').innerHTML = '<div class="table-wrap"><table><thead><tr>'
    + '<th><span class="th-content">' + ICONS.key + ' Label & ID</span></th>'
    + '<th><span class="th-content">Key</span></th>'
    + '<th><span class="th-content">' + ICONS.check + ' Status</span></th>'
    + '<th class="hide-sm"><span class="th-content">Terakhir dipakai</span></th>'
    + '<th class="hide-sm"><span class="th-content">' + ICONS.warn + ' Error</span></th>'
    + '<th><span class="th-content">Aksi</span></th>'
    + '</tr></thead><tbody>'
    + keys.map((k) => {
      const cooling = k.cooldownUntil > now;
      const statusPill = !k.isActive ? '<span class="pill neutral">nonaktif</span>'
        : cooling ? `<span class="pill warn">cooldown ${Math.max(1, Math.ceil((k.cooldownUntil - now) / 60))} m</span>`
          : '<span class="pill ok">siap</span>';
      const badgeCls = k.isActive && !cooling ? 'success' : cooling ? 'warning' : '';
      return `<tr>
        <td>
          <div class="entity-cell">
            <div class="entity-badge ${badgeCls}">${ICONS.key}</div>
            <div class="entity-stack">
              <span class="entity-title">${escapeHTML(k.label || '(tanpa label)')}</span>
              <span class="entity-desc mono">id: ${escapeHTML(String(k.id))}</span>
            </div>
          </div>
        </td>
        <td class="mono">${escapeHTML(k.maskedSecret)}</td>
        <td>${statusPill}${k.strikes ? ` <span class="muted tabular">×${k.strikes}</span>` : ''}</td>
        <td class="hide-sm muted tabular">${escapeHTML(fmtTime(k.lastUsedAt))}</td>
        <td class="hide-sm err-text">${escapeHTML(k.lastError || '')}</td>
        <td><div class="row">
          <button class="btn ghost tiny" data-act="reveal" data-id="${k.id}">salin</button>
          <button class="btn ghost tiny" data-act="toggle" data-id="${k.id}" data-active="${k.isActive ? 1 : 0}">${k.isActive ? 'nonaktifkan' : 'aktifkan'}</button>
          ${cooling ? `<button class="btn ghost tiny" data-act="reset" data-id="${k.id}">bebaskan</button>` : ''}
          <button class="btn ghost tiny" data-act="delete" data-id="${k.id}">hapus</button>
        </div></td>
      </tr>`;
    }).join('')
    + '</tbody></table></div>';
}

async function loadKeys() {
  renderKeys((await apiJSON('/api/keys')).data || []);
}

async function addKey() {
  const secret = $('keySecret').value.trim();
  if (!secret) { toast('isi API key dulu', true); return; }
  try {
    await apiJSON('/api/keys', {
      method: 'POST',
      body: JSON.stringify({ label: $('keyLabel').value.trim(), secret }),
    });
    $('keySecret').value = '';
    $('keyLabel').value = '';
    toast('key tersimpan');
    refreshUsage();
    loadKeys();
  } catch (err) {
    toast(err.message, true);
  }
}

async function keyAction(button) {
  const { id, act, active } = button.dataset;
  try {
    if (act === 'delete') {
      if (!confirm('Hapus key ini?')) return;
      await apiJSON(`/api/keys/${id}`, { method: 'DELETE' });
    } else if (act === 'toggle') {
      await apiJSON(`/api/keys/${id}`, { method: 'PATCH', body: JSON.stringify({ isActive: active !== '1' }) });
    } else if (act === 'reset') {
      await apiJSON(`/api/keys/${id}/reset`, { method: 'POST' });
    } else if (act === 'reveal') {
      const body = await apiJSON(`/api/keys/${id}/reveal`);
      try {
        await navigator.clipboard.writeText(body.secret);
        toast('key disalin ke clipboard');
      } catch {
        toast('salin manual: ' + body.secret);
      }
      return;
    }
    loadKeys();
    refreshUsage();
  } catch (err) {
    toast(err.message, true);
  }
}

/* ── wiring ──────────────────────────────────────────────────────────────── */
function updateTextStats() {
  const text = $('text').value;
  const chars = [...text].length;
  const words = text.trim() ? text.trim().split(/\s+/).length : 0;
  $('charCount').textContent = `${chars.toLocaleString()} karakter · ${words.toLocaleString()} kata`;
}

let searchTimer = null;

function wire() {
  const remember = (patch) => { savePrefs(patch); updateCurl(); };

  $('model').addEventListener('change', () => remember({ model: $('model').value }));
  $('format').addEventListener('change', () => remember({ format: $('format').value }));
  $('emotion').addEventListener('change', () => remember({ emotion: $('emotion').value }));
  $('speed').addEventListener('input', () => { updateRangeOutputs(); remember({ speed: num('speed') }); });
  $('volume').addEventListener('input', () => { updateRangeOutputs(); remember({ volume: num('volume') }); });
  $('text').addEventListener('input', updateTextStats);
  $('text').addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); generate(); }
  });

  $('speak').addEventListener('click', generate);
  $('stop').addEventListener('click', stop);
  $('download').addEventListener('click', download);

  $('voicePlaySelected').addEventListener('click', () => {
    if (voiceState.selected) playPreview(voiceState.selected.id);
  });
  $('voiceList').addEventListener('click', (e) => {
    const copy = e.target.closest('[data-copy]');
    if (copy) { e.stopPropagation(); copyVoiceID(copy.dataset.copy, copy); return; }
    const play = e.target.closest('[data-play]');
    if (play) { e.stopPropagation(); playPreview(play.dataset.play); return; }
    const card = e.target.closest('.voice-card');
    if (card) selectVoice(voiceState.items.find((v) => v.id === card.dataset.id));
  });
  $('voiceList').addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    const card = e.target.closest('.voice-card');
    if (card) { e.preventDefault(); selectVoice(voiceState.items.find((v) => v.id === card.dataset.id)); }
  });
  $('voiceSearch').addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => loadVoices(true), 320);
  });
  $('langFilter').addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => loadVoices(true), 420);
  });
  $('genderFilter').addEventListener('change', () => loadVoices(true));
  $('voiceRefresh').addEventListener('click', () => loadVoices(true));
  $('loadMore').addEventListener('click', () => loadVoices(false));

  $('pageOlder').addEventListener('click', () => { usageState.offset += usageState.limit; loadRequests().catch(() => {}); });
  $('pageNewer').addEventListener('click', () => { usageState.offset = Math.max(0, usageState.offset - usageState.limit); loadRequests().catch(() => {}); });

  $('keyAdd').addEventListener('click', addKey);
  $('keySecret').addEventListener('keydown', (e) => { if (e.key === 'Enter') addKey(); });
  $('rotation').addEventListener('change', changeRotation);
  $('keysTable').addEventListener('click', (e) => {
    const button = e.target.closest('button[data-act]');
    if (button) keyAction(button);
  });

  // Client keys, account and the traffic map.
  $('clientAdd').addEventListener('click', addClient);
  $('clientLabel').addEventListener('keydown', (e) => { if (e.key === 'Enter') addClient(); });
  $('clientsTable').addEventListener('click', (e) => {
    const button = e.target.closest('button[data-cact]');
    if (button) clientAction(button);
  });
  $('newClientCopy').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText($('newClientSecretValue').textContent);
      toast('secret disalin');
    } catch {
      toast('salin manual: ' + $('newClientSecretValue').textContent, true);
    }
  });
  $('accountSave').addEventListener('click', saveAccount);
  $('accountLogoutAll').addEventListener('click', logoutAll);
  $('logoutBtn').addEventListener('click', logout);
  $('logoutTopBtn').addEventListener('click', logout);
  $('routingRefresh').addEventListener('click', () => { loadRouting(); refreshUsage(); });
  $('routingKeys').addEventListener('change', (e) => {
    const input = e.target.closest('input[data-priority-for]');
    if (input) changeKeyPriority(input.dataset.priorityFor, input.value);
  });
  window.addEventListener('resize', queueRoutingLayout);
  // Login gate.
  $('loginForm').addEventListener('submit', submitLogin);
  $('forcePanel').addEventListener('submit', submitForcePassword);
  $('forceCurrent').addEventListener('keydown', () => { $('forceError').hidden = true; });
  // A stray Enter inside the gate must never navigate: without JS the form
  // would send the password in the query string.
  document.querySelectorAll('#loginForm input').forEach((input) => {
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') { event.preventDefault(); submitLogin(event); }
    });
  });
  document.querySelectorAll('#forcePanel input').forEach((input) => {
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') { event.preventDefault(); submitForcePassword(event); }
    });
  });

  // One listener for the script files, so load order cannot silently break it.
  window.addEventListener('error', (event) => {
    const message = event.message || 'kesalahan skrip';
    toast(message, true);
    if (document.body.classList.contains('locked')) showLogin(message);
  });
  window.addEventListener('unhandledrejection', (event) => {
    const message = (event.reason && event.reason.message) || 'kesalahan tak tertangani';
    toast(message, true);
  });

  // Header quick command input (⌘K): jumps to Synthesize and filters there.
  const globalCmd = $('globalCommandInput');
  if (globalCmd) {
    globalCmd.addEventListener('input', (e) => {
      $('voiceSearch').value = e.target.value;
      if (currentPage !== 'synthesize') location.hash = '#/synthesize';
      clearTimeout(searchTimer);
      searchTimer = setTimeout(() => loadVoices(true), 320);
    });
  }

  window.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      if (globalCmd) globalCmd.focus(); else $('voiceSearch').focus();
    }
  });

  wireRouter();
}

// refreshAll is what the boot path and the 30s ticker use: the small numeric
// data every page shows in a badge, without the heavy per-page panels.
async function refreshAll() {
  const jobs = [loadOverview(), loadRequests(), loadKeys(), loadRouting(), loadClients()];
  const results = await Promise.allSettled(jobs);
  for (const result of results) {
    if (result.status === 'rejected') toast(result.reason.message, true);
  }
}

async function boot() {
  let state = null;
  try {
    state = await fetchAuthState();
  } catch (err) {
    showLogin('gateway tidak dapat dihubungi: ' + err.message);
    return;
  }

  // No account yet: the gate offers setup instead of a sign-in form, and the
  // console stays hidden until that succeeds.
  if (!state.configured) {
    showLogin('');
    return;
  }

  // A default password has exactly one way forward.
  if (state.mustChangePassword && state.session) {
    showForcePassword(state.notify || '');
    return;
  }

  // With an account, the session is the only door. Probe before rendering so
  // the console never flashes for a request that is about to be refused.
  const probe = await fetch('/api/overview', {
    headers: { [CSRF_HEADER]: CSRF_VALUE },
    credentials: 'same-origin',
  }).catch(() => null);
  if (!probe || probe.status === 401) {
    // Explain the gate rather than just showing it: after a password change
    // the session is over by design.
    const notice = takePasswordChangedNotice() || state.notify;
    const isSuccess = Boolean(notice && !state.notify);
    const message = notice || (probe ? '' : 'gateway tidak dapat dihubungi');
    showLogin(message);
    // A successful change is good news; anything else (wrong password from a
    // native submission) is rendered as a warning by showLogin already.
    if (isSuccess) $('loginError').classList.add('ok');
    return;
  }
  showApp();
  wire();
  updateTextStats();
  renderSelectedVoice();
  await refreshAll();

  // The probe passed, but a panel can still be refused if the session vanished
  // between the two requests. Route that to the login screen (with a message)
  // instead of leaving an empty dashboard behind.
  if (!(await sessionStillValid())) {
    showLogin('sesi tidak ditemukan — silakan masuk lagi');
    return;
  }
  showPage(pageFromHash());
  setInterval(() => {
    if (document.hidden) return;
    refreshUsage();
    loadRouting();
  }, 30000);
}

// sessionStillValid asks the server whether this browser's cookie still works.
// It sends the CSRF marker but no credentials of its own: the cookie is
// HttpOnly and travels automatically.
async function sessionStillValid() {
  const res = await fetch('/api/overview', {
    headers: { [CSRF_HEADER]: CSRF_VALUE },
    credentials: 'same-origin',
  }).catch(() => null);
  return Boolean(res && res.ok);
}

boot();
