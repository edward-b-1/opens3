// views/buckets.js — bucket list, creation and per-bucket settings.
window.views = window.views || {};

views.buckets = async function (main) {
  const { h, clear, table, badge, fmtDate, modal, confirm, field, tagsField, toast } = ui;
  const data = await api.get('buckets');
  const filter = h('input.input.grow', { placeholder: 'Filter buckets (press / to focus)', 'data-filter': '1', onInput: () => render() });
  const wrap = h('div');
  main.append(h('h1', 'Buckets'), h('div.toolbar', filter, h('button.btn.btn-primary', { onClick: create }, 'Create bucket')), wrap);

  function render() {
    const q = filter.value.trim().toLowerCase();
    const rows = data.buckets.filter((b) => b.name.includes(q));
    clear(wrap);
    wrap.append(table([
      { h: 'Name', cell: (b) => h('a', { href: '#' + app.objectsHash(b.name, '') }, b.name) },
      { h: 'Created', cell: (b) => fmtDate(b.created) },
      { h: 'Region', cell: (b) => b.region },
      { h: 'Owner', cell: (b) => b.ownerName || b.owner.slice(0, 12) },
      { h: 'Features', cell: (b) => [
        b.versioning ? badge('Versioning ' + b.versioning.toLowerCase(), b.versioning === 'Enabled' ? 'ok' : 'warn') : null, ' ',
        b.objectLock ? badge('Object Lock', 'accent') : null] },
      { h: '', cls: 'actions-cell', cell: (b) => [
        h('a.btn.btn-sm', { href: '#' + app.settingsHash(b.name) }, 'Settings'),
        h('button.btn.btn-sm.btn-danger', { onClick: () => remove(b) }, 'Delete')] },
    ], rows, data.buckets.length ? 'No buckets match the filter.' : 'No buckets yet. Create one to get started.'));
  }

  async function create() {
    const name = h('input.input', { required: true, pattern: '[a-z0-9.-]{3,63}', placeholder: 'my-bucket', spellcheck: false });
    const versioning = h('input', { type: 'checkbox' });
    const lock = h('input', { type: 'checkbox', onChange: () => { if (lock.checked) versioning.checked = true; } });
    const tags = tagsField([]);
    const ok = await modal({
      title: 'Create bucket', submit: 'Create',
      body: [field('Name', name, '3–63 characters: lower-case letters, digits, dots and hyphens.'),
        h('label.check', versioning, 'Enable versioning'),
        h('label.check', lock, 'Enable Object Lock (implies versioning; cannot be turned off later)'), tags],
      onSubmit: async () => {
        const b = await api.post('buckets', { name: name.value.trim(), versioning: versioning.checked, objectLock: lock.checked, tags: tags.value() });
        toast('Bucket ' + b.name + ' created', 'success');
        return b;
      },
    });
    if (ok) app.go(app.objectsHash(ok.name, ''));
  }

  async function remove(b) {
    if (!await confirm('Delete bucket', 'Delete bucket "' + b.name + '"? It must be empty.')) return;
    try {
      await api.del('buckets/' + encodeURIComponent(b.name));
      toast('Bucket deleted', 'success');
      data.buckets = data.buckets.filter((x) => x.name !== b.name);
      render();
    } catch (e) { ui.error(e); }
  }
  render();
};

views.bucketSettings = async function (main, name) {
  const { h, badge, fmtDate, fmtBytes, confirm, jsonField, tagsField, toast, error } = ui;
  const b = await api.get('buckets/' + encodeURIComponent(name));
  const crumbs = h('div.breadcrumb', h('a', { href: '#/buckets' }, 'Buckets'), h('span.sep', '/'),
    h('a', { href: '#' + app.objectsHash(name, '') }, name), h('span.sep', '/'), h('span.cur', 'Settings'));

  // Overview.
  const kv = (pairs) => h('dl.kv', pairs.filter((p) => p[1] !== undefined && p[1] !== null && p[1] !== '').map(([k, v]) => [h('dt', k), h('dd', v)]));
  const overview = h('div.card', h('h2', 'Overview'), kv([
    ['Created', fmtDate(b.created)], ['Region', b.region], ['Owner', b.ownerName || b.owner],
    ['Ownership', b.ownership], ['Object Lock', b.objectLock ? (b.objectLockConfig ? b.objectLockConfig.mode + ' ' + (b.objectLockConfig.days || b.objectLockConfig.years) + (b.objectLockConfig.days ? ' days' : ' years') : 'enabled') : 'disabled'],
    ['Default encryption', b.encryption ? b.encryption.algorithm + (b.encryption.kmsKeyId ? ' (' + b.encryption.kmsKeyId + ')' : '') : 'none'],
    ['Public access block', b.publicAccessBlock ? Object.entries(b.publicAccessBlock).filter(([, v]) => v).map(([k]) => k).join(', ') || 'configured, all off' : 'not configured'],
    ['Quota', b.quota ? fmtBytes(b.quota) : ''],
    ['Other configuration', [b.hasLifecycle ? 'lifecycle ' : '', b.hasCors ? 'CORS ' : '', b.hasNotification ? 'notifications' : ''].join('') || 'none'],
  ]));

  // Versioning.
  const vsel = h('select', { disabled: b.objectLock }, ['', 'Enabled', 'Suspended'].map((v) => h('option', { value: v, selected: (b.versioning || '') === v, disabled: v === '' }, v || 'Never enabled')));
  const versioning = h('div.card', h('h2', 'Versioning'),
    h('p.muted', b.objectLock ? 'Versioning is required while Object Lock is enabled.' : 'Keep every version of every object. Suspending stops creating new versions but keeps existing ones.'),
    h('div.row', h('div', { style: 'width:200px' }, vsel), h('button.btn', {
      disabled: b.objectLock, onClick: async () => {
        try { await api.put('buckets/' + encodeURIComponent(name) + '/versioning', { status: vsel.value }); toast('Versioning ' + vsel.value.toLowerCase(), 'success'); }
        catch (e) { error(e); }
      },
    }, 'Apply')));

  // Tags.
  const tags = tagsField(b.tags);
  const tagsCard = h('div.card', h('h2', 'Tags'), tags, h('div.row', h('button.btn', {
    onClick: async () => {
      try { await api.put('buckets/' + encodeURIComponent(name) + '/tags', { tags: tags.value() }); toast('Tags saved', 'success'); } catch (e) { error(e); }
    },
  }, 'Save tags')));

  // Policy.
  const pubBadge = h('span', b.policyPublic ? badge('Public', 'danger') : null);
  const pol = jsonField('Policy document', b.policy, { readOnly: !b.policyReadable });
  const example = () => { pol.textarea.value = ui.pretty({ Version: '2012-10-17', Statement: [{ Effect: 'Allow', Principal: '*', Action: ['s3:GetObject'], Resource: ['arn:aws:s3:::' + name + '/*'] }] }); pol.textarea.dispatchEvent(new Event('input')); };
  const policyCard = h('div.card', h('div.row.between', h('h2', 'Bucket policy ', pubBadge), h('button.btn.btn-sm', { type: 'button', onClick: example }, 'Insert public-read example')),
    b.policyReadable ? null : h('p.muted', 'You are not allowed to read this bucket policy.'),
    pol, h('div.row',
      h('button.btn.btn-primary', {
        onClick: async () => {
          try {
            const doc = pol.value();
            if (!doc) throw new Error('Policy document is required; use Remove to delete the policy');
            const r = await api.put('buckets/' + encodeURIComponent(name) + '/policy', { policy: doc });
            ui.clear(pubBadge); if (r.public) pubBadge.append(badge('Public', 'danger'));
            toast('Policy saved', 'success');
          } catch (e) { error(e); }
        },
      }, 'Save policy'),
      h('button.btn.btn-danger', {
        onClick: async () => {
          if (!await confirm('Remove policy', 'Remove the bucket policy from "' + name + '"?', 'Remove')) return;
          try { await api.del('buckets/' + encodeURIComponent(name) + '/policy'); pol.textarea.value = ''; pol.textarea.dispatchEvent(new Event('input')); ui.clear(pubBadge); toast('Policy removed', 'success'); }
          catch (e) { error(e); }
        },
      }, 'Remove')));

  // Danger zone.
  const danger = h('div.card', h('h2', 'Delete bucket'), h('p.muted', 'The bucket must be empty. Object versions and delete markers count.'),
    h('button.btn.btn-danger', {
      onClick: async () => {
        if (!await confirm('Delete bucket', 'Delete bucket "' + name + '"?')) return;
        try { await api.del('buckets/' + encodeURIComponent(name)); toast('Bucket deleted', 'success'); app.go('/buckets'); } catch (e) { error(e); }
      },
    }, 'Delete bucket'));

  main.append(crumbs, h('h1', name), h('div.grid', { style: 'grid-template-columns: repeat(auto-fit, minmax(360px, 1fr))' }, overview, versioning, tagsCard), policyCard, danger);
};
