/* Client keys: the credentials external callers present to /v1.
 *
 * A secret is shown exactly once, right after creation or rotation. The list
 * only ever carries the prefix, because the server stores a hash.
 */
'use strict';

let clientSecretTimer = null;

function renderClients(payload) {
  const clients = payload.data || [];
  const stats = payload.stats || {};
  $('clientsNavBadge').textContent = String(stats.active || 0);
  $('clientsMeta').textContent = clients.length
    ? `${stats.active || 0} aktif · ${stats.total || 0} total`
    : 'belum ada key';

  if (!clients.length) {
    $('clientsTable').innerHTML = '<p class="empty">Belum ada client key. Selama belum ada, '
      + 'request dari loopback masih terbuka; begitu key pertama dibuat, semua pemanggil /v1 wajib memakainya.</p>';
    return;
  }

  const now = Math.floor(Date.now() / 1000);
  $('clientsTable').innerHTML = '<div class="table-wrap"><table><thead><tr>'
    + '<th><span class="th-content">' + ICONS.key + ' Nama</span></th>'
    + '<th><span class="th-content">Prefix</span></th>'
    + '<th><span class="th-content">' + ICONS.check + ' Status</span></th>'
    + '<th><span class="th-content">Pemakaian</span></th>'
    + '<th class="hide-sm"><span class="th-content">Terakhir dipakai</span></th>'
    + '<th class="hide-sm"><span class="th-content">Kedaluwarsa</span></th>'
    + '<th><span class="th-content">Aksi</span></th>'
    + '</tr></thead><tbody>'
    + clients.map((c) => {
      const expired = c.expiresAt > 0 && c.expiresAt <= now;
      const status = !c.isActive ? '<span class="pill neutral">nonaktif</span>'
        : expired ? '<span class="pill warn">kedaluwarsa</span>'
          : '<span class="pill ok">aktif</span>';
      const expires = c.expiresAt > 0
        ? escapeHTML(fmtTime(c.expiresAt))
        : '<span class="muted">tanpa batas</span>';
      return `<tr>
        <td>
          <div class="entity-cell">
            <div class="entity-badge ${c.isActive && !expired ? 'success' : 'warning'}">${ICONS.key}</div>
            <div class="entity-stack">
              <span class="entity-title">${escapeHTML(c.label || '(tanpa nama)')}</span>
              <span class="entity-desc mono">id: ${escapeHTML(String(c.id))}</span>
            </div>
          </div>
        </td>
        <td class="mono">${escapeHTML(c.prefix)}…</td>
        <td>${status}</td>
        <td class="tabular" title="${(c.requestCount || 0).toLocaleString()} request">${(c.requestCount || 0).toLocaleString()} req</td>
        <td class="hide-sm muted tabular">${escapeHTML(fmtTime(c.lastUsedAt))}</td>
        <td class="hide-sm muted tabular">${expires}</td>
        <td><div class="row">
          <button class="btn ghost tiny" data-cact="rotate" data-id="${c.id}">putar</button>
          <button class="btn ghost tiny" data-cact="toggle" data-id="${c.id}" data-active="${c.isActive ? 1 : 0}">${c.isActive ? 'nonaktifkan' : 'aktifkan'}</button>
          <button class="btn ghost tiny" data-cact="delete" data-id="${c.id}">hapus</button>
        </div></td>
      </tr>`;
    }).join('')
    + '</tbody></table></div>';
}

async function loadClients() {
  try {
    renderClients(await apiJSON('/api/clients'));
  } catch (error) {
    $('clientsTable').innerHTML = `<p class="empty">Gagal memuat client key: ${escapeHTML(error.message)}</p>`;
  }
}

function showClientSecret(secret, note) {
  $('newClientSecretValue').textContent = secret;
  $('newClientSecret').hidden = false;
  clearTimeout(clientSecretTimer);
  // Keep it on screen long enough to copy; it is never retrievable again.
  clientSecretTimer = setTimeout(() => { $('newClientSecret').hidden = true; }, 5 * 60 * 1000);
  toast(note || 'key dibuat — salin sekarang, tidak ditampilkan lagi');
}

async function addClient() {
  const label = $('clientLabel').value.trim();
  const days = Number($('clientExpiry').value || 0);
  try {
    const body = await apiJSON('/api/clients', {
      method: 'POST',
      body: JSON.stringify({ label, expiresInDays: days }),
    });
    if (body.secret) showClientSecret(body.secret);
    $('clientLabel').value = '';
    loadClients();
  } catch (error) {
    toast(error.message, true);
  }
}

async function clientAction(button) {
  const { id, cact, active } = button.dataset;
  try {
    if (cact === 'delete') {
      if (!confirm('Hapus client key ini? Aplikasi yang memakainya akan langsung ditolak.')) return;
      await apiJSON(`/api/clients/${id}`, { method: 'DELETE' });
    } else if (cact === 'toggle') {
      await apiJSON(`/api/clients/${id}`, { method: 'PATCH', body: JSON.stringify({ isActive: active !== '1' }) });
    } else if (cact === 'rotate') {
      if (!confirm('Putar key ini? Secret lama langsung tidak berlaku.')) return;
      const body = await apiJSON(`/api/clients/${id}/rotate`, { method: 'POST', body: '{}' }, 201);
      if (body.secret) showClientSecret(body.secret, 'key diputar — salin secret baru');
    }
    loadClients();
    loadRouting();
  } catch (error) {
    toast(error.message, true);
  }
}

async function loadAccount() {
  try {
    const account = await apiJSON('/api/account');
    $('accountUsername').value = account.username || '';
  } catch { /* the panel simply stays empty */ }
}

async function saveAccount() {
  const currentPassword = $('accountCurrent').value;
  const username = $('accountUsername').value.trim();
  const newPassword = $('accountNew').value;
  if (!currentPassword) { toast('isi password sekarang dulu', true); return; }
  try {
    const body = await apiJSON('/api/account', {
      method: 'PATCH',
      body: JSON.stringify({ currentPassword, username, newPassword }),
    });
    toast(body.reauthMessage || 'akun diperbarui');
    setTimeout(() => location.reload(), 1200);
  } catch (error) {
    toast(error.message, true);
  }
}

async function logoutAll() {
  if (!confirm('Keluarkan semua sesi? Browser ini juga akan keluar.')) return;
  try {
    await fetch('/api/account/logout-all', { method: 'POST', headers: { [CSRF_HEADER]: CSRF_VALUE } });
  } catch { /* fall through to reload */ }
  location.reload();
}
