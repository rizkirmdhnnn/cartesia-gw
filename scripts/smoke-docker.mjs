/* Local smoke test for the built image.
 *
 * Runs inside the container's own network (from the host via a port mapping),
 * so it exercises the same paths a browser and a client would. No credentials
 * are hard-coded: the operator password is generated per run.
 */
const base = process.env.BASE || 'http://127.0.0.1:20999';
const pass = 'docker-' + Math.random().toString(36).slice(2) + '-' + Date.now();
let cookie = '';
let failures = 0;

function log(ok, label, detail) {
  if (!ok) failures++;
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? '  → ' + detail : ''}`);
}

async function call(method, path, body, opts = {}) {
  const headers = { 'Content-Type': 'application/json', 'X-Requested-With': 'cartesia-gw', ...(opts.headers || {}) };
  if (cookie && !opts.noCookie) headers.Cookie = cookie;
  const res = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual' });
  for (const c of res.headers.getSetCookie()) {
    const pair = c.split(';')[0];
    if (pair.startsWith('cartesia_gw_session=')) cookie = pair;
  }
  const text = await res.text();
  let json = null;
  try { json = text ? JSON.parse(text) : null; } catch { /* not json */ }
  return { status: res.status, json, text, headers: res.headers };
}

(async () => {
  let r = await call('GET', '/healthz');
  log(r.status === 200 && r.json.status === 'ok', 'healthz answers', r.json && `keys=${JSON.stringify(r.json.keys)}, token=${r.json.gatewayTokenIsSet}`);

  r = await call('GET', '/');
  log(r.status === 200 && r.text.includes('loginGate'), 'page shell served');

  for (const asset of ['/app.css', '/app.js', '/auth.js', '/routing.js', '/clients.js']) {
    const a = await call('GET', asset);
    log(a.status === 200, `asset ${asset}`, String(a.status));
  }

  r = await call('GET', '/auth/state');
  log(r.status === 200, 'auth state reachable');

  if (r.json.mustChangePassword) {
    // Fresh install: admin/admin is usable but locked to the change screen.
    r = await call('POST', '/auth/login', { username: 'admin', password: 'admin' });
    log(r.status === 200 && r.json.mustChangePassword === true, 'default admin/admin signs in and is told to change the password');
    log(cookie !== '', 'session cookie issued');

    const locked = await call('GET', '/api/overview');
    log(locked.status === 403, 'a default password cannot use the api', String(locked.status));

    r = await call('PATCH', '/api/account', { currentPassword: 'admin', newPassword: 'admin', username: '' });
    log(r.status === 400, 'the default cannot be "changed" into itself', String(r.status));

    r = await call('PATCH', '/api/account', { currentPassword: 'admin', newPassword: pass, username: '' });
    log(r.status === 200, 'password change accepted');
    const revoked = await call('GET', '/api/keys');
    log(revoked.status === 401, 'the change revoked the old session', String(revoked.status));

    r = await call('GET', '/auth/state');
    log(r.json.mustChangePassword === false, 'auth state reports the password was changed');

    r = await call('POST', '/auth/login', { username: 'admin', password: pass });
    log(r.status === 200 && r.json.mustChangePassword === false, 'login with the new password');
    log(cookie !== '', 'new session cookie issued');
  } else {
    r = await call('POST', '/auth/login', { username: 'admin', password: process.env.OPERATOR_PASSWORD || pass });
    log(r.status === 200, 'login with the operator password', r.text.slice(0, 90));
    log(cookie !== '', 'session cookie issued');
  }

  r = await call('GET', '/api/overview');
  log(r.status === 200 && !!r.json.controls, 'overview with the session');

  r = await call('GET', '/api/routing');
  log(r.status === 200 && Array.isArray(r.json.keys), 'routing snapshot', r.json && r.json.strategy);

  r = await call('POST', '/api/clients', { label: 'docker-smoke' });
  const secret = r.json && r.json.secret;
  log(r.status === 201 && !!secret, 'client key created', secret && secret.slice(0, 10) + '…');

  const anon = await fetch(base + '/v1/models');
  log(anon.status === 401, '/v1 refuses an anonymous request', String(anon.status));
  const withKey = await fetch(base + '/v1/models', { headers: { Authorization: 'Bearer ' + secret } });
  log(withKey.status === 200, '/v1 accepts the client key', String(withKey.status));

  const noMarker = await fetch(base + '/api/settings', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', Cookie: cookie },
    body: JSON.stringify({ rotation: 'sticky' }),
  });
  log(noMarker.status === 403, 'writes without the CSRF marker are refused', String(noMarker.status));

  r = await call('PATCH', '/api/settings', { rotation: 'priority' });
  log(r.status === 200 && r.json.rotation === 'priority', 'priority strategy accepted');

  r = await call('POST', '/auth/logout');
  log(r.status === 204, 'logout');
  cookie = '';
  r = await call('GET', '/api/keys');
  log(r.status === 401, 'after logout the api is closed', String(r.status));

  console.log(failures === 0 ? '\nALL PASSED' : `\n${failures} FAILED`);
  process.exit(failures === 0 ? 0 : 1);
})().catch((error) => {
  console.error('smoke test crashed:', error);
  process.exit(1);
});
