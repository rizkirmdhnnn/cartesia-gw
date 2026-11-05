/* Authentication glue for the dashboard.
 *
 * The console is a session app: no cookie, no dashboard. The login gate asks
 * /auth/state what to render (first-run setup or sign-in), then posts to
 * /auth/login and lets the server set an HttpOnly cookie. Nothing here keeps a
 * credential in localStorage — the old gateway token lives in the database now
 * as a client key, not in the browser.
 */
'use strict';

const CSRF_HEADER = 'X-Requested-With';
const CSRF_VALUE = 'cartesia-gw';

let authState = { configured: false, setup: false, username: '' };

// takePasswordChangedNotice reads and clears the one-shot confirmation the
// change-password flow leaves behind, so it is shown exactly once.
function takePasswordChangedNotice() {
  try {
    const key = 'cartesia-gw-password-changed';
    if (sessionStorage.getItem(key) !== '1') return '';
    sessionStorage.removeItem(key);
    return 'Password berhasil diganti. Masuk lagi dengan password baru.';
  } catch {
    return '';
  }
}

async function fetchAuthState() {
  const res = await fetch('/auth/state', { headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error('tidak bisa membaca status autentikasi');
  authState = await res.json();
  return authState;
}

// showForcePassword is the only screen a default-password session may see: the
// console stays locked and the API is closed until the change lands.
function showForcePassword(message) {
  document.body.classList.add('locked');
  const force = $('forcePanel');
  const login = $('loginForm');
  if (login) login.hidden = true;
  force.hidden = false;
  $('forceCurrent').value = '';
  $('forceNew').value = '';
  $('forceConfirm').value = '';
  const err = $('forceError');
  err.hidden = !message;
  err.textContent = message || '';
  $('forceCurrent').focus();
}

async function submitForcePassword(event) {
  if (event) event.preventDefault();
  const err = $('forceError');
  const current = $('forceCurrent').value;
  const next = $('forceNew').value;
  const submit = $('forceSubmit');

  const fail = (message) => {
    err.hidden = false;
    err.textContent = message;
  };

  if (!current) return fail('isi password sekarang dulu');
  if (next.length < (authState.minPasswordLen || 12)) {
    return fail(`password baru minimal ${authState.minPasswordLen || 12} karakter`);
  }
  if (next !== $('forceConfirm').value) return fail('ulangan password tidak sama');

  err.hidden = true;
  submit.disabled = true;
  submit.textContent = 'Menyimpan…';
  try {
    const res = await fetch('/api/account', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', [CSRF_HEADER]: CSRF_VALUE },
      body: JSON.stringify({ currentPassword: current, newPassword: next, username: '' }),
    });
    const body = await res.json().catch(() => null);
    if (!res.ok) throw new Error(body?.error?.message || `HTTP ${res.status}`);

    // Success is shown before anything else, because the change ends every
    // session — including this one. The reload then lands on the login form
    // with the new password required; without this state a failure to clear
    // the cookie would look like "nothing happened".
    submit.textContent = 'Berhasil — masuk lagi';
    err.hidden = false;
    err.className = 'login-error ok';
    err.textContent = 'Password diganti. Semua sesi dikeluarkan; masuk lagi dengan password baru.';
    try { await fetch('/auth/logout', { method: 'POST', headers: { [CSRF_HEADER]: CSRF_VALUE } }); } catch { /* already gone */ }
    // A marker in sessionStorage survives the reload, so the login screen can
    // confirm the change instead of asking for a password with no explanation.
    try { sessionStorage.setItem('cartesia-gw-password-changed', '1'); } catch { /* private mode */ }
    setTimeout(() => location.replace('/'), 1200);
  } catch (error) {
    fail(error.message);
  } finally {
    submit.disabled = false;
    if (err.className.includes('ok')) {
      // Leave the success message up; the reload is about to happen.
    } else {
      submit.textContent = 'Simpan & masuk lagi';
    }
  }
}

function showLogin(message) {
  document.body.classList.add('locked');
  const form = $('loginForm');
  const confirmWrap = $('loginConfirmWrap');
  const submit = $('loginSubmit');
  const desc = $('loginDesc');
  const err = $('loginError');
  $('forcePanel').hidden = true;

  const setup = !authState.configured;
  confirmWrap.hidden = !setup;
  submit.textContent = setup ? 'Buat akun & masuk' : 'Masuk';
  desc.textContent = setup
    ? 'Belum ada akun operator. Isi username dan password (minimal 12 karakter) untuk membuatnya.'
    : `Masuk dengan akun operator. Instalasi baru memakai ${authState.defaultUsername || 'admin'} / ${authState.defaultPassword || 'admin'} — dan wajib diganti setelah login pertama.`;
  $('loginUser').value = setup ? '' : (authState.username || '');
  $('loginPass').value = '';
  $('loginConfirm').value = '';
  if (err) {
    err.hidden = !message;
    err.textContent = message || '';
    err.className = 'login-error';
  }
  form.hidden = false;
  (setup ? $('loginUser') : $('loginPass')).focus();
}

function showApp() {
  document.body.classList.remove('locked');
  const form = $('loginForm');
  if (form) form.hidden = true;
}

async function submitLogin(event) {
  event.preventDefault();
  const err = $('loginError');
  const username = $('loginUser').value.trim();
  const password = $('loginPass').value;
  const setup = !authState.configured;

  if (setup) {
    if (password.length < 12) {
      err.hidden = false;
      err.textContent = 'password minimal 12 karakter';
      return;
    }
    if (password !== $('loginConfirm').value) {
      err.hidden = false;
      err.textContent = 'ulangan password tidak sama';
      return;
    }
  }

  const path = setup ? '/auth/setup' : '/auth/login';
  $('loginSubmit').disabled = true;
  $('loginSubmit').textContent = 'Memeriksa…';
  try {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', [CSRF_HEADER]: CSRF_VALUE },
      body: JSON.stringify({ username, password }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error(body?.error?.message || `HTTP ${res.status}`);
    }
    // Confirm the cookie really stuck before navigating: a browser that refuses
    // it would otherwise land on a dashboard that cannot load anything.
    const state = await fetch('/auth/state', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .catch(() => null);
    if (!state || state.session !== true) {
      err.hidden = false;
      err.textContent = 'Login diterima tetapi sesi tidak tersimpan di browser ini. '
        + 'Pastikan cookie diizinkan (bukan mode blokir cookie), lalu coba lagi.';
      return;
    }
    location.replace(location.pathname);
  } catch (error) {
    err.hidden = false;
    err.textContent = error.message;
  } finally {
    $('loginSubmit').disabled = false;
    $('loginSubmit').textContent = setup ? 'Buat akun & masuk' : 'Masuk';
  }
}

async function logout() {
  try {
    await fetch('/auth/logout', { method: 'POST', headers: { [CSRF_HEADER]: CSRF_VALUE } });
  } catch { /* the cookie is gone client-side either way */ }
  location.reload();
}
