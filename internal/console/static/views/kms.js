// views/kms.js — named encryption keys of the built-in KMS (SSE-KMS).
window.views = window.views || {};

views.kms = async function (main) {
  const { h, clear, table, badge, fmtDate, modal, confirm, field, toast, error } = ui;
  const wrap = h('div');
  let keys = [];
  main.append(h('h1', 'Encryption keys'),
    h('p.muted', 'Named keys for SSE-KMS (x-amz-server-side-encryption: aws:kms). Key material is stored wrapped by the server master key and never leaves the server.'),
    h('div.toolbar', h('div.grow'), h('button.btn.btn-primary', { onClick: create }, 'Create key')), wrap);
  async function reload() { keys = (await api.get('kms/keys')).keys; render(); }
  function render() {
    clear(wrap);
    wrap.append(table([
      { h: 'Key ID', cell: (k) => [h('code', k.id), k.default ? [' ', badge('default', 'accent')] : null] },
      { h: 'Status', cell: (k) => badge(k.disabled ? 'disabled' : 'enabled', k.disabled ? 'danger' : 'ok') },
      { h: 'Created', cell: (k) => fmtDate(k.created) },
      { h: '', cls: 'actions-cell', cell: (k) => k.default ? null : h('button.btn.btn-sm.btn-danger', { onClick: () => remove(k) }, 'Delete') },
    ], keys, 'No keys.'));
  }
  async function create() {
    const id = h('input.input', { required: true, spellcheck: false, placeholder: 'my-app-key' });
    if (await modal({ title: 'Create KMS key', submit: 'Create', body: field('Key ID', id, 'Up to 128 characters, no slashes.'),
      onSubmit: async () => { await api.post('kms/keys', { id: id.value.trim() }); toast('Key created', 'success'); } })) reload();
  }
  async function remove(k) {
    if (!await confirm('Delete KMS key', 'Delete key "' + k.id + '"? Every object encrypted with it becomes permanently unreadable.')) return;
    try { await api.del('kms/keys/' + encodeURIComponent(k.id)); toast('Key deleted', 'success'); reload(); } catch (e) { error(e); }
  }
  await reload();
};
