/* Page router. The console is six pages, not one long scroll: the sidebar links
 * to #/overview, #/synthesize, #/history, #/keys, #/clients and #/account, and
 * exactly one <section class="page"> is visible at a time.
 *
 * Hash routing keeps this working from the embedded binary with no server-side
 * rewrite: the shell is served for "/" and the fragment never reaches the
 * server. Each page's title block lives here so the header always matches what
 * is on screen.
 */
'use strict';

const PAGES = {
  overview: {
    eyebrow: 'Live Traffic',
    title: 'Traffic map',
    desc: 'Klien, gateway, dan key upstream dari setiap provider dalam satu pandangan: siapa yang memakai, lewat key mana, dan key apa yang dipilih berikutnya.',
    badge: 'overviewNavBadge',
  },
  synthesize: {
    eyebrow: 'Studio Engine',
    title: 'Voice Synthesis',
    desc: 'Sintesis suara lewat satu endpoint: model memilih providernya (Cartesia atau ElevenLabs), lalu dengar atau unduh hasilnya tanpa membayar dua kali.',
    badge: null,
  },
  history: {
    eyebrow: 'Audit Trail',
    title: 'Riwayat request',
    desc: 'Setiap attempt tercatat: provider, model, voice, ukuran audio, key yang dipakai, status, latensi, dan apa saja yang gateway sesuaikan.',
    badge: 'usageNavBadge',
  },
  keys: {
    eyebrow: 'Upstream Credentials',
    title: 'Upstream keys',
    desc: 'Kunci ke Cartesia dan ElevenLabs beserta state cooldown-nya. Rotasi berjalan per provider; di sinilah strateginya dipilih.',
    badge: 'keysNavBadge',
  },
  clients: {
    eyebrow: 'Client Access',
    title: 'Client API keys',
    desc: 'Kredensial yang dipakai aplikasi Anda untuk memanggil /v1. Buat, putar, nonaktifkan, atau hapus kapan saja.',
    badge: 'clientsNavBadge',
  },
  account: {
    eyebrow: 'Operator',
    title: 'Akun & sesi',
    desc: 'Satu akun operator: ganti username atau password, dan cabut semua sesi yang masih aktif.',
    badge: null,
  },
};

const DEFAULT_PAGE = 'synthesize';
let currentPage = null;

// Lazily loaded pages. Traffic data is cheap to refresh and needed by two
// pages (the map and the badge), so it is always loaded once the session is
// up; the heavier panels only fetch when their page is opened.
const pageLoaders = {
  overview: () => Promise.all([loadOverview(), loadRouting()]),
  synthesize: () => loadVoices(true),
  history: () => loadRequests(),
  keys: () => Promise.all([loadOverview(), loadKeys()]),
  clients: () => loadClients(),
  account: () => loadAccount(),
};

function pageFromHash() {
  const name = (location.hash || '').replace(/^#\/?/, '').replace(/\/.*$/, '');
  return PAGES[name] ? name : DEFAULT_PAGE;
}

function showPage(name, { replace = false } = {}) {
  if (!PAGES[name]) name = DEFAULT_PAGE;

  document.querySelectorAll('.page').forEach((section) => {
    const active = section.dataset.page === name;
    section.hidden = !active;
    section.classList.toggle('is-active', active);
  });
  document.querySelectorAll('.sidebar-nav a.nav-link').forEach((link) => {
    link.classList.toggle('active', link.dataset.page === name);
  });

  const meta = PAGES[name];
  $('pageEyebrow').textContent = meta.eyebrow;
  $('pageTitle').textContent = meta.title;
  $('pageDesc').textContent = meta.desc;
  $('breadcrumbPage').textContent = meta.eyebrow;

  const changed = currentPage !== name;
  currentPage = name;
  if (changed) {
    window.scrollTo({ top: 0 });
    // Layout is only meaningful while the page is visible: the traffic map's
    // SVG needs real element boxes.
    if (name === 'overview') queueRoutingLayout();
  }

  const loader = pageLoaders[name];
  if (loader) {
    Promise.resolve()
      .then(loader)
      .then(() => { if (name === 'overview') queueRoutingLayout(); })
      .catch((error) => toast(error.message, true));
  }

  if (!replace) {
    const target = '#/' + name;
    if (location.hash !== target) history.replaceState(null, '', target);
  }
}

function wireRouter() {
  window.addEventListener('hashchange', () => showPage(pageFromHash()));
  document.querySelectorAll('.sidebar-nav a.nav-link').forEach((link) => {
    link.addEventListener('click', (event) => {
      // Let the anchor update the hash so back/forward and a copied URL work.
      if (!link.dataset.page) return;
      event.preventDefault();
      const target = '#/' + link.dataset.page;
      if (location.hash === target) showPage(link.dataset.page);
      else location.hash = target;
    });
  });
}
