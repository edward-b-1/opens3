// views/login.js — sign-in screen.
window.views = window.views || {};
views.login = function (root, onLogin) {
  const { h, clear, field, error } = ui;
  clear(root);
  const ak = h('input.input', { autocomplete: 'username', required: true, spellcheck: false });
  const sk = h('input.input', { type: 'password', autocomplete: 'current-password', required: true });
  const btn = h('button.btn.btn-primary.w-full', { type: 'submit' }, 'Log in');
  const form = h('form', {
    onSubmit: async (e) => {
      e.preventDefault(); btn.disabled = true;
      try { onLogin(await api.login(ak.value.trim(), sk.value)); }
      catch (err) { error(err); sk.select(); }
      finally { btn.disabled = false; }
    },
  }, field('Access key', ak), field('Secret key', sk), btn);
  root.append(h('div.login', h('div.card',
    h('div.brand', h('span.brand-mark', { 'aria-hidden': 'true' }), 'OpenS3 Console'),
    form,
    h('p.muted.small', { style: 'margin-top:14px' }, 'Sign in with any OpenS3 access key. Root credentials come from OPENS3_ROOT_USER / OPENS3_ROOT_PASSWORD.'))));
  ak.focus();
};
