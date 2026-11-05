/* Provider awareness for the console. One place that knows how a provider is
 * labelled, which ranges its controls use, and how the key/voice pages switch
 * between providers. Loaded before app.js. */
'use strict';

const PROVIDERS = {
  cartesia: { label: 'Cartesia', short: 'ct' },
  elevenlabs: { label: 'ElevenLabs', short: 'el' },
};

function providerLabel(name) {
  return PROVIDERS[name]?.label || name || '—';
}

function providerTag(name) {
  if (!name) return '';
  return `<span class="tag tag-provider" data-provider="${escapeHTML(name)}">${escapeHTML(providerLabel(name))}</span>`;
}

// modelProvider reads the provider out of a "provider/model" id. A bare model
// name has none, which is what the server accepts too.
function modelProvider(model) {
  const idx = String(model || '').indexOf('/');
  if (idx <= 0) return '';
  const prefix = model.slice(0, idx).toLowerCase();
  if (prefix === 'el' || prefix === 'eleven') return 'elevenlabs';
  return PROVIDERS[prefix] ? prefix : '';
}

// modelName strips the provider prefix for display.
function modelName(model) {
  const idx = String(model || '').indexOf('/');
  return idx > 0 ? model.slice(idx + 1) : model;
}

// rangesFor returns the valid bounds of a control for a provider. Volume is
// null where the provider has no such control, and the console hides it
// instead of showing a slider that does nothing.
function rangesFor(providerName) {
  const controls = overview?.controls || {};
  const perProvider = controls.providerRanges || {};
  const range = perProvider[providerName];
  if (!range) {
    return { speed: controls.speedRange || [0.6, 1.5], volume: controls.volumeRange || [0.5, 2] };
  }
  return { speed: range.speed || controls.speedRange, volume: range.volume };
}

// keyProviderFilter and voiceProviderFilter are the two segmented controls.
let keyProviderFilter = '';
let voiceProviderFilter = '';

function renderSegments(host, names, current, onPick) {
  const counts = overview?.keys?.providers || {};
  host.innerHTML = names.map((name) => {
    const label = name ? providerLabel(name) : 'Semua';
    const count = counts[name]?.total;
    return `<button type="button" data-provider="${escapeHTML(name)}"
      aria-pressed="${current === name ? 'true' : 'false'}">${escapeHTML(label)}${
      name && count !== undefined ? `<span class="seg-count">${count}</span>` : ''}</button>`;
  }).join('');
  host.onclick = (event) => {
    const button = event.target.closest('button[data-provider]');
    if (button) onPick(button.dataset.provider);
  };
}
