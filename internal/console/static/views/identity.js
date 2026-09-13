// views/identity.js — users, access keys, groups and policies.
window.views = window.views || {};

views.identity = async function (main, tab) {
  const { h } = ui;
  const body = h('div');
  if (!app.me.admin) {
    // Self-service: only the caller's own keys and service accounts.
    main.append(h('h1', 'My access keys'), h('p.muted', 'Keys and service accounts owned by ' + app.me.user + '. Service accounts can carry a session policy that narrows their permissions.'), body);
    await identityTabs.keys(body);
    return;
  }
  const tabs = h('div.tabs', [['users', 'Users'], ['keys', 'Access keys'], ['groups', 'Groups'], ['policies', 'Policies']].map(([id, label]) =>
    h('a', { href: '#/identity/' + id, class: id === tab ? 'active' : '' }, label)));
  main.append(h('h1', 'Identity'), tabs, body);
  await identityTabs[tab](body);
};

const identityTabs = {};
const enc = encodeURIComponent;
const policyNames = async () => (await api.get('policies')).policies.map((p) => p.name);
const userNames = async () => (await api.get('users')).users.map((u) => u.name);

identityTabs.users = async function (body) {
  const { h, clear, table, badge, fmtDate, modal, confirm, field, multiSelect, toast, error, copy } = ui;
  const filter = h('input.input.grow', { placeholder: 'Filter users', 'data-filter': '1', onInput: render });
  const wrap = h('div');
  let users = [];
  body.append(h('div.toolbar', filter, h('button.btn.btn-primary', { onClick: create }, 'Create user')), wrap);

  async function reload() { users = (await api.get('users')).users; render(); }
  function render() {
    const q = filter.value.trim().toLowerCase();
    clear(wrap);
    wrap.append(table([
      { h: 'Name', cell: (u) => [u.name, u.name === app.me.user ? [' ', badge('you', 'accent')] : null] },
      { h: 'Status', cell: (u) => badge(u.enabled ? 'enabled' : 'disabled', u.enabled ? 'ok' : 'danger') },
      { h: 'Policies', cell: (u) => u.policies.join(', ') || h('span.muted', 'none') },
      { h: 'Groups', cell: (u) => u.groups.join(', ') || h('span.muted', 'none') },
      { h: 'Keys', cls: 'num', cell: (u) => u.keys },
      { h: 'Console', cell: (u) => u.name === 'root' ? badge('root', 'ok') : u.console ? badge('password set', 'ok') : h('span.muted', 'API only') },
      { h: 'Created', cell: (u) => fmtDate(u.created) },
      { h: '', cls: 'actions-cell', cell: (u) => [
        h('a.btn.btn-sm', { href: '#/identity/keys?user=' + enc(u.name), onClick: (e) => { e.preventDefault(); sessionStorage.setItem('keysUser', u.name); app.go('/identity/keys'); } }, 'Keys'),
        h('button.btn.btn-sm', { onClick: () => edit(u), disabled: u.name === 'root' }, 'Edit'),
        h('button.btn.btn-sm.btn-danger', { onClick: () => remove(u), disabled: u.name === 'root' || u.name === app.me.user }, 'Delete')] },
    ], users.filter((u) => u.name.includes(q)), 'No users yet.'));
  }
  async function create() {
    const pols = await policyNames();
    const name = h('input.input', { required: true, spellcheck: false, placeholder: 'alice' });
    const pw = h('input.input', { type: 'password', autocomplete: 'new-password', minLength: 8, placeholder: 'leave empty for API-only users' });
    const noKey = h('input', { type: 'checkbox' });
    const sel = multiSelect('Policies', pols, []);
    const r = await modal({ title: 'Create user', submit: 'Create',
      body: [field('User name', name), sel,
        field('Console password', pw, 'Optional. Lets this user sign in to the console. At least 8 characters.'),
        h('label.check', noKey, 'Do not create an access key now'),
        h('p.muted.small', 'An access key ID and secret are generated for the S3 API and shown once after creation.')],
      onSubmit: () => api.post('users', { name: name.value.trim(), policies: sel.value(), password: pw.value || undefined, noKey: noKey.checked }) });
    if (r) { toast('User created', 'success'); if (r.key) await showCredentials(r); reload(); }
  }
  function showCredentials(r) {
    return modal({ title: 'Access key for ' + r.key.user, submit: null, cancel: 'Done', body: [
      h('div.secret-box', h('p', h('strong', 'Copy the secret key now.'), ' It is shown only once and cannot be recovered.'),
        h('dl.kv', h('dt', 'Access key'), h('dd', h('code', r.key.accessKey), ' ', h('button.btn.btn-sm', { type: 'button', onClick: () => copy(r.key.accessKey) }, 'Copy')),
          h('dt', 'Secret key'), h('dd', h('code', r.secretKey), ' ', h('button.btn.btn-sm', { type: 'button', onClick: () => copy(r.secretKey) }, 'Copy'))))] });
  }
  async function edit(u) {
    const pols = await policyNames();
    const enabled = h('input', { type: 'checkbox', checked: u.enabled });
    const pw = h('input.input', { type: 'password', autocomplete: 'new-password', minLength: 8, placeholder: u.console ? 'leave empty to keep' : 'none set' });
    const clearPw = h('input', { type: 'checkbox', disabled: !u.console });
    const sel = multiSelect('Policies', pols, u.policies);
    if (await modal({ title: 'Edit ' + u.name, body: [h('label.check', enabled, 'Enabled'), sel,
        field('Console password', pw, u.console ? 'Set a new console password.' : 'Set a console password to let this user sign in here.'),
        u.console ? h('label.check', clearPw, 'Remove the console password (API access keys are unaffected)') : null],
      onSubmit: async () => {
        await api.patch('users/' + enc(u.name), { enabled: enabled.checked, policies: sel.value() });
        if (clearPw.checked) await api.del('users/' + enc(u.name) + '/password');
        else if (pw.value) await api.put('users/' + enc(u.name) + '/password', { password: pw.value });
        toast('User updated', 'success');
      } })) reload();
  }
  async function remove(u) {
    if (!await confirm('Delete user', 'Delete user "' + u.name + '" and all of its access keys?')) return;
    try { await api.del('users/' + enc(u.name)); toast('User deleted', 'success'); reload(); } catch (e) { error(e); }
  }
  await reload();
};

identityTabs.keys = async function (body) {
  const { h, clear, table, badge, fmtDate, modal, confirm, field, jsonField, toast, error, copy } = ui;
  const initial = sessionStorage.getItem('keysUser') || ''; sessionStorage.removeItem('keysUser');
  const filter = h('input.input.grow', { placeholder: 'Filter by access key, user or description', 'data-filter': '1', value: initial, onInput: render });
  const wrap = h('div');
  let keys = [];
  body.append(h('div.toolbar', filter, h('button.btn.btn-primary', { onClick: create }, 'Create access key')), wrap);

  const admin = app.me.admin;
  async function reload() { keys = (await api.get('keys', admin ? {} : { user: app.me.user })).keys; render(); }
  function render() {
    const q = filter.value.trim().toLowerCase();
    clear(wrap);
    wrap.append(table([
      { h: 'Access key', cell: (k) => h('code', k.accessKey) },
      { h: 'User', cell: (k) => k.user },
      { h: 'Kind', cell: (k) => badge(k.kind === 'service' ? 'service account' : 'user key', k.kind === 'service' ? 'accent' : '') },
      { h: 'Status', cell: (k) => [badge(k.enabled ? 'enabled' : 'disabled', k.enabled ? 'ok' : 'danger'), k.sessionPolicy ? [' ', badge('session policy', 'warn')] : null] },
      { h: 'Description', cell: (k) => k.description || '' },
      { h: 'Created', cell: (k) => fmtDate(k.created) },
      { h: 'Expires', cell: (k) => k.expires ? fmtDate(k.expires) : h('span.muted', 'never') },
      { h: '', cls: 'actions-cell', cell: (k) => [
        h('button.btn.btn-sm', { onClick: () => edit(k) }, 'Edit'),
        h('button.btn.btn-sm', { onClick: () => toggle(k) }, k.enabled ? 'Disable' : 'Enable'),
        h('button.btn.btn-sm.btn-danger', { onClick: () => remove(k) }, 'Delete')] },
    ], keys.filter((k) => (k.accessKey + ' ' + k.user + ' ' + (k.description || '')).toLowerCase().includes(q)), 'No access keys.'));
  }
  function showSecret(r) {
    return modal({ title: 'Access key created', submit: null, cancel: 'Done', body: [
      h('div.secret-box', h('p', h('strong', 'Copy the secret key now.'), ' It is shown only once and cannot be recovered.'),
        h('dl.kv', h('dt', 'Access key'), h('dd', h('code', r.key.accessKey), ' ', h('button.btn.btn-sm', { type: 'button', onClick: () => copy(r.key.accessKey) }, 'Copy')),
          h('dt', 'Secret key'), h('dd', h('code', r.secretKey), ' ', h('button.btn.btn-sm', { type: 'button', onClick: () => copy(r.secretKey) }, 'Copy'))))] });
  }
  async function create() {
    const users = admin ? await userNames() : [app.me.user];
    const user = h('select', { disabled: !admin }, users.map((u) => h('option', { value: u, selected: u === (initial || app.me.user) }, u)));
    const kind = h('select', h('option', { value: 'user', selected: true }, 'Regular user key'), h('option', { value: 'service' }, 'Service account (restricted by a session policy)'));
    const desc = h('input.input');
    const expires = h('input.input', { type: 'date' });
    const pol = jsonField('Session policy', null);
    const syncKind = () => { const svc = kind.value === 'service'; pol.textarea.disabled = !svc; pol.classList.toggle('disabled', !svc); if (!svc) pol.textarea.value = ''; };
    kind.addEventListener('change', syncKind);
    syncKind();
    const r = await modal({ title: 'Create access key', submit: 'Create', wide: true,
      body: [field('User', user), field('Kind', kind), field('Description', desc), field('Expires', expires, 'Optional. The key stops working at the end of this day, UTC.'),
        h('p.muted.small', 'The access key ID and secret are generated and shown once. A session policy restricts a service account to a subset of the user\'s permissions; it can never grant more.'), pol],
      onSubmit: () => api.post('keys', { user: user.value, kind: kind.value, description: desc.value, sessionPolicy: kind.value === 'service' ? pol.value() : undefined,
        expires: expires.value ? new Date(expires.value + 'T23:59:59Z').toISOString() : undefined }) });
    if (r) { await showSecret(r); reload(); }
  }
  async function edit(k) {
    const desc = h('input.input', { value: k.description || '' });
    const rotate = h('input', { type: 'checkbox' });
    const pol = jsonField('Session policy', k.sessionPolicy);
    if (await modal({ title: 'Edit ' + k.accessKey, wide: true, body: [field('Description', desc), h('label.check', rotate, 'Rotate: generate a new secret key (shown once)'), pol],
      onSubmit: async () => {
        const doc = pol.value();
        const r = await api.patch('keys/' + enc(k.accessKey), { description: desc.value, rotate: rotate.checked || undefined, sessionPolicy: doc === null ? (k.sessionPolicy ? null : undefined) : doc });
        if (r && r.secretKey) await showSecret(r);
        toast('Key updated', 'success');
      } })) reload();
  }
  async function toggle(k) {
    try { await api.patch('keys/' + enc(k.accessKey), { enabled: !k.enabled }); toast(k.enabled ? 'Key disabled' : 'Key enabled', 'success'); reload(); } catch (e) { error(e); }
  }
  async function remove(k) {
    if (!await confirm('Delete access key', 'Delete access key ' + k.accessKey + '? Clients using it will stop working immediately.')) return;
    try { await api.del('keys/' + enc(k.accessKey)); toast('Key deleted', 'success'); reload(); } catch (e) { error(e); }
  }
  await reload();
};

identityTabs.groups = async function (body) {
  const { h, clear, table, badge, fmtDate, modal, confirm, field, multiSelect, toast, error, copy } = ui;
  const wrap = h('div');
  let groups = [];
  body.append(h('div.toolbar', h('div.grow'), h('button.btn.btn-primary', { onClick: () => edit(null) }, 'Create group')), wrap);
  async function reload() { groups = (await api.get('groups')).groups; render(); }
  function render() {
    clear(wrap);
    wrap.append(table([
      { h: 'Name', cell: (g) => g.name },
      { h: 'Status', cell: (g) => badge(g.enabled ? 'enabled' : 'disabled', g.enabled ? 'ok' : 'danger') },
      { h: 'Members', cell: (g) => g.members.join(', ') || h('span.muted', 'none') },
      { h: 'Policies', cell: (g) => g.policies.join(', ') || h('span.muted', 'none') },
      { h: 'Created', cell: (g) => fmtDate(g.created) },
      { h: '', cls: 'actions-cell', cell: (g) => [h('button.btn.btn-sm', { onClick: () => edit(g) }, 'Edit'), h('button.btn.btn-sm.btn-danger', { onClick: () => remove(g) }, 'Delete')] },
    ], groups, 'No groups yet. Groups attach policies to several users at once.'));
  }
  async function edit(g) {
    const [users, pols] = await Promise.all([userNames(), policyNames()]);
    const name = h('input.input', { required: true, value: g ? g.name : '', readOnly: !!g, spellcheck: false });
    const enabled = h('input', { type: 'checkbox', checked: g ? g.enabled : true });
    const members = multiSelect('Members', users.filter((u) => u !== 'root'), g ? g.members : []);
    const sel = multiSelect('Policies', pols, g ? g.policies : []);
    if (await modal({ title: g ? 'Edit ' + g.name : 'Create group', submit: g ? 'Save' : 'Create', body: [field('Name', name), g ? h('label.check', enabled, 'Enabled') : null, members, sel],
      onSubmit: async () => {
        if (g) await api.patch('groups/' + enc(g.name), { enabled: enabled.checked, members: members.value(), policies: sel.value() });
        else await api.post('groups', { name: name.value.trim(), members: members.value(), policies: sel.value() });
        toast(g ? 'Group updated' : 'Group created', 'success');
      } })) reload();
  }
  async function remove(g) {
    if (!await confirm('Delete group', 'Delete group "' + g.name + '"? Members keep their own policies.')) return;
    try { await api.del('groups/' + enc(g.name)); toast('Group deleted', 'success'); reload(); } catch (e) { error(e); }
  }
  await reload();
};

identityTabs.policies = async function (body) {
  const { h, clear, table, badge, fmtDate, modal, confirm, field, jsonField, toast, error } = ui;
  const wrap = h('div');
  let pols = [];
  body.append(h('div.toolbar', h('p.muted', { style: 'margin:0' }, 'Policies use the AWS IAM policy language. Built-in policies are read-only.'), h('div.grow'), h('button.btn.btn-primary', { onClick: () => edit(null) }, 'Create policy')), wrap);
  async function reload() { pols = (await api.get('policies')).policies; render(); }
  function render() {
    clear(wrap);
    wrap.append(table([
      { h: 'Name', cell: (p) => [p.name, p.builtIn ? [' ', badge('built-in')] : null] },
      { h: 'Statements', cls: 'num', cell: (p) => { try { const d = typeof p.document === 'string' ? JSON.parse(p.document) : p.document; return (Array.isArray(d.Statement) ? d.Statement : [d.Statement]).length; } catch (e) { return '?'; } } },
      { h: 'Updated', cell: (p) => fmtDate(p.updated) },
      { h: '', cls: 'actions-cell', cell: (p) => [h('button.btn.btn-sm', { onClick: () => edit(p) }, p.builtIn ? 'View' : 'Edit'), p.builtIn ? null : h('button.btn.btn-sm.btn-danger', { onClick: () => remove(p) }, 'Delete')] },
    ], pols, 'No policies.'));
  }
  async function edit(p) {
    const name = h('input.input', { required: true, value: p ? p.name : '', readOnly: !!p, spellcheck: false });
    const doc = jsonField('Document', p ? p.document : { Version: '2012-10-17', Statement: [{ Effect: 'Allow', Action: ['s3:ListBucket', 's3:GetObject'], Resource: ['arn:aws:s3:::example-bucket', 'arn:aws:s3:::example-bucket/*'] }] }, { required: true, readOnly: p && p.builtIn });
    if (await modal({ title: p ? (p.builtIn ? p.name : 'Edit ' + p.name) : 'Create policy', wide: true, submit: p && p.builtIn ? null : 'Save', cancel: p && p.builtIn ? 'Close' : 'Cancel',
      body: [field('Name', name), doc],
      onSubmit: async () => { await api.put('policies/' + enc(name.value.trim()), { document: doc.value() }); toast('Policy saved', 'success'); } })) reload();
  }
  async function remove(p) {
    if (!await confirm('Delete policy', 'Delete policy "' + p.name + '"? It is detached from every user and group.')) return;
    try { await api.del('policies/' + enc(p.name)); toast('Policy deleted', 'success'); reload(); } catch (e) { error(e); }
  }
  await reload();
};
