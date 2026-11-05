// Verifies the browser-facing half of the login fix: credentials travel in a
// POST body, and a stray GET with them in the query gets a scrubbing page.
const base = process.env.BASE || 'http://127.0.0.1:20140';
let fail = 0;
const check = (ok, label, detail) => {
  if (!ok) fail++;
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? '  → ' + detail : ''}`);
};

(async () => {
  // 1. Native form POST (what the browser sends before JS runs).
  const form = new URLSearchParams({ username: 'admin', password: 'admin' });
  const post = await fetch(base + '/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', Accept: 'text/html' },
    body: form,
    redirect: 'manual',
  });
  check(post.status === 303, 'form POST login answers 303', String(post.status));
  check(post.headers.get('location') === '/', 'redirect goes to / without credentials', post.headers.get('location'));
  const cookies = post.headers.getSetCookie();
  const sessionCookie = cookies.find((c) => c.startsWith('cartesia_gw_session=')) || '';
  check(/HttpOnly/i.test(sessionCookie), 'session cookie is HttpOnly');
  check(/SameSite=Strict/i.test(sessionCookie), 'session cookie is SameSite=Strict');
  check(!/;\s*Secure/i.test(sessionCookie), 'session cookie is not Secure over plain HTTP (so the browser keeps it)', sessionCookie);
  check([...post.headers.entries()].every(([, v]) => !String(v).includes('password=')), 'no response header echoes the password');

  // 2. Wrong password from a form: redirect + flash cookie, nothing in the URL.
  const bad = await fetch(base + '/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', Accept: 'text/html' },
    body: new URLSearchParams({ username: 'admin', password: 'salah' }),
    redirect: 'manual',
  });
  check(bad.status === 303 && bad.headers.get('location') === '/', 'bad form login redirects to /', bad.status + ' ' + bad.headers.get('location'));
  const flash = bad.headers.getSetCookie().find((c) => c.startsWith('cartesia_gw_flash='));
  check(Boolean(flash) && flash.toLowerCase().includes('httponly'), 'failure message rides an HttpOnly flash cookie');

  // 3. The flash is read exactly once by /auth/state.
  const state = await fetch(base + '/auth/state', { headers: { Cookie: flash.split(';')[0] } });
  const body = await state.json();
  check(body.notify === 'username atau password salah', 'auth/state returns the flash message', body.notify);

  // 4. A legacy GET with credentials never reaches the console and never logs in.
  const legacy = await fetch(base + '/?username=admin&password=admin', { headers: { Accept: 'text/html' } });
  const html = await legacy.text();
  check(legacy.status === 200 && html.includes("location.replace('/')"), 'credential-in-query GET gets a scrubbing page');
  check(!legacy.headers.getSetCookie().some((c) => c.startsWith('cartesia_gw_session=')), 'credential-in-query GET sets no session');
  check(!html.includes('loginForm'), 'the console shell is not served at the dirty URL');

  console.log(fail === 0 ? '\nALL PASSED' : `\n${fail} FAILED`);
  process.exit(fail === 0 ? 0 : 1);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
