// app.js — hash router, session state and global chrome. Views live in
// views/*.js and register themselves on window.views.
(function () {
  const { clear, error, toast, h, modal } = ui;
  const app = { me: null, path: null };
  window.app = app;

  const routes = [
    { re: /^\/buckets$/, view: 'buckets', nav: 'buckets' },
    { re: /^\/buckets\/([^/]+)\/settings$/, view: 'bucketSettings', nav: 'buckets', args: (m) => [decodeURIComponent(m[1])] },
    { re: /^\/b\/([^/]+)\/?(.*)$/, view: 'objects', nav: 'buckets', args: (m) => [decodeURIComponent(m[1]), decodeURIComponent(m[2] || '')] },
    // Non-admins may still manage their own access keys.
    { re: /^\/identity\/(users|keys|groups|policies)$/, view: 'identity', nav: 'identity', allow: (me, m) => me.admin || m[1] === 'keys', args: (m) => [m[1]] },
    { re: /^\/kms$/, view: 'kms', nav: 'kms', allow: (me) => me.admin },
    { re: /^\/status$/, view: 'status', nav: 'status' },
  ];

  function currentPath() {
    let p = location.hash.slice(1) || '/buckets';
    if (!p.startsWith('/')) p = '/' + p;
    return p;
  }

  async function route() {
    if (!app.me) return;
    const path = currentPath();
    const main = document.getElementById('main');
    for (const r of routes) {
      const m = path.match(r.re);
      if (!m) continue;
      if (r.allow && !r.allow(app.me, m)) { location.hash = '#/buckets'; return; }
      document.querySelectorAll('#nav a[data-nav]').forEach((a) => a.classList.toggle('active', a.dataset.nav === r.nav));
      document.getElementById('nav').classList.remove('open');
      app.path = path;
      clear(main);
      try { await views[r.view](main, ...(r.args ? r.args(m) : [])); }
      catch (e) { error(e); }
      main.scrollTop = 0;
      return;
    }
    location.hash = '#/buckets';
  }

  function showApp(me) {
    app.me = me;
    document.getElementById('login-root').hidden = true;
    document.getElementById('app').hidden = false;
    document.getElementById('topbar-user').textContent = me.user + (me.isRoot ? ' (root)' : '');
    document.querySelectorAll('#nav a[data-admin]').forEach((a) => { a.hidden = !me.admin; });
    document.querySelectorAll('#nav a[data-user]').forEach((a) => { a.hidden = me.admin; });
    document.getElementById('sidebar-foot').textContent = 'OpenS3 ' + me.version + ' · ' + me.region;
    route();
  }

  function showLogin() {
    app.me = null;
    document.getElementById('app').hidden = true;
    const root = document.getElementById('login-root');
    root.hidden = false;
    views.login(root, showApp);
  }

  app.refresh = route;
  app.go = (path) => { if (currentPath() === path) route(); else location.hash = '#' + path; };
  // Hash for the object browser: bucket segment plus the prefix with '/' kept readable.
  app.objectsHash = (bucket, prefix) => '/b/' + encodeURIComponent(bucket) + '/' + encodeURIComponent(prefix || '').replace(/%2F/g, '/');
  app.settingsHash = (bucket) => '/buckets/' + encodeURIComponent(bucket) + '/settings';

  window.addEventListener('hashchange', route);
  window.addEventListener('unauthorized', () => { if (app.me) { toast('Your session has ended. Please log in again.', 'error'); showLogin(); } });
  document.getElementById('logout-btn').addEventListener('click', async () => {
    try { await api.post('logout'); } catch (e) { /* cookie is gone either way */ }
    showLogin();
  });
  // Settings dialog: values live in localStorage (settings.js).
  const settingLabels = {
    density: ['Density', { comfortable: 'Comfortable', compact: 'Compact' }],
    theme: ['Theme', { system: 'Follow system', light: 'Light', dark: 'Dark' }],
    dateStyle: ['Dates', { absolute: 'Absolute (12 Sep 2026, 14:05)', relative: 'Relative (3 hours ago)' }],
    timeZone: ['Time zone', { local: 'Local time', utc: 'UTC' }],
    sizeUnits: ['Sizes', { binary: 'Binary units (KiB, MiB)', decimal: 'Decimal units (kB, MB)', bytes: 'Exact bytes' }],
    pageSize: ['Rows per page', { 100: '100', 300: '300', 1000: '1000' }],
    showMarkers: ['Folder markers', { true: 'Show empty folder marker objects', false: 'Hide them' }],
  };
  async function openSettings() {
    const cur = settings.all();
    const selects = {};
    const grid = h('div.settings-grid');
    for (const [key, [label, names]] of Object.entries(settingLabels)) {
      const sel = h('select', { id: 'setting-' + key }, settings.options[key].map((v) => h('option', { value: String(v), selected: String(v) === String(cur[key]) }, names[v])));
      selects[key] = sel;
      grid.append(h('label', { htmlFor: 'setting-' + key }, label), sel);
    }
    const resetBtn = h('button.btn.btn-sm', { type: 'button', onClick: () => { for (const [k, sel] of Object.entries(selects)) sel.value = String(settings.defaults[k]); } }, 'Reset to defaults');
    await modal({ title: 'Settings', submit: 'Apply', body: [grid, h('p.muted.small', { style: 'margin-top:12px' }, 'Stored in this browser only. ', resetBtn)],
      onSubmit: () => {
        const patch = {};
        for (const [k, sel] of Object.entries(selects)) {
          const v = sel.value;
          patch[k] = typeof settings.defaults[k] === 'number' ? Number(v) : typeof settings.defaults[k] === 'boolean' ? v === 'true' : v;
        }
        settings.set(patch);
      } });
  }
  app.openSettings = openSettings;
  document.getElementById('settings-btn').addEventListener('click', openSettings);
  // Formats and page size need a re-render; density and theme are CSS only.
  window.addEventListener('settingschange', (e) => { if (e.detail.changed.some((k) => !['density', 'theme'].includes(k))) route(); });
  document.getElementById('nav-toggle').addEventListener('click', (e) => {
    const open = document.getElementById('nav').classList.toggle('open');
    e.currentTarget.setAttribute('aria-expanded', String(open));
  });
  // Keyboard: "/" focuses the page filter, "r" reloads the view, "u" opens upload in the browser, "," opens settings.
  document.addEventListener('keydown', (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey || !app.me) return;
    const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName) || document.getElementById('modal-root').firstChild;
    if (typing) return;
    if (e.key === '/') { const f = document.querySelector('#main [data-filter]'); if (f) { e.preventDefault(); f.focus(); } }
    else if (e.key === 'r') { route(); }
    else if (e.key === 'u') { const b = document.querySelector('#main [data-upload]'); if (b) b.click(); }
    else if (e.key === ',') { e.preventDefault(); openSettings(); }
  });

  api.me().then(showApp).catch(showLogin);
})();
