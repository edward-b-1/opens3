// ui.js — tiny DOM helpers, toasts, modals and formatters shared by views.
(function () {
  // h('tag.class#id', {attrs}, ...children) — children may be strings,
  // nodes, arrays or null. on* attributes become listeners.
  function h(sel, attrs, ...children) {
    if (attrs !== undefined && attrs !== null && (typeof attrs !== 'object' || attrs instanceof Node || Array.isArray(attrs))) { children.unshift(attrs); attrs = {}; }
    const [tag, ...rest] = sel.split(/(?=[.#])/);
    const el = document.createElement(tag || 'div');
    for (const r of rest) { if (r[0] === '.') el.classList.add(r.slice(1)); else el.id = r.slice(1); }
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === null || v === undefined || v === false) continue;
      if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
      else if (k === 'class') el.className += (el.className ? ' ' : '') + v;
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else if (k in el && k !== 'style' && k !== 'list') el[k] = v;
      else el.setAttribute(k, v === true ? '' : v);
    }
    append(el, children);
    return el;
  }
  function append(el, children) {
    for (const c of children) {
      if (c === null || c === undefined || c === false) continue;
      if (Array.isArray(c)) append(el, c);
      else el.append(c instanceof Node ? c : String(c));
    }
  }
  function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

  function toast(msg, kind = 'info', ms = 5000) {
    const box = document.getElementById('toasts');
    const t = h('div.toast.' + kind, { role: kind === 'error' ? 'alert' : 'status' },
      h('span.msg', msg), h('button', { 'aria-label': 'Dismiss', onClick: () => t.remove() }, '×'));
    box.append(t);
    if (ms) setTimeout(() => t.remove(), ms);
    return t;
  }
  const error = (e) => toast(e && e.message ? e.message : String(e), 'error', 8000);

  // modal({title, body, submit, cancel, wide, onSubmit}) resolves when closed.
  // onSubmit may return a promise; throwing keeps the modal open.
  function modal(o) {
    return new Promise((resolve) => {
      const root = document.getElementById('modal-root');
      const prev = document.activeElement;
      let busy = false;
      const close = (v) => { root.removeChild(back); document.removeEventListener('keydown', onKey); if (prev && prev.focus) prev.focus(); resolve(v); };
      const onKey = (e) => { if (e.key === 'Escape' && !busy) { e.preventDefault(); close(null); } };
      const submitBtn = o.submit === null ? null : h('button.btn.btn-primary' + (o.danger ? '.btn-danger' : ''), { type: 'submit' }, o.submit || 'Save');
      const form = h('form', {
        onSubmit: async (e) => {
          e.preventDefault();
          if (!o.onSubmit) return close(true);
          busy = true; if (submitBtn) submitBtn.disabled = true;
          try { const v = await o.onSubmit(form); close(v === undefined ? true : v); }
          catch (err) { error(err); busy = false; if (submitBtn) submitBtn.disabled = false; }
        },
      }, o.body, h('div.actions', h('button.btn', { type: 'button', onClick: () => close(null) }, o.cancel || 'Cancel'), submitBtn));
      const box = h('div.modal' + (o.wide ? '.wide' : ''), { role: 'dialog', 'aria-modal': 'true', 'aria-label': o.title }, h('h2', o.title), form);
      const back = h('div.modal-backdrop', { onMousedown: (e) => { if (e.target === back && !busy) close(null); } }, box);
      root.append(back);
      document.addEventListener('keydown', onKey);
      const first = form.querySelector('input:not([type=hidden]), select, textarea, button');
      if (first) first.focus();
    });
  }
  const confirm = (title, text, submit = 'Delete') => modal({ title, body: h('p', text), submit, danger: true });

  // Formatters.
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  function fmtBytes(n) {
    if (n === undefined || n === null) return '';
    let i = 0; n = Number(n);
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n : n.toFixed(n < 10 ? 2 : 1)) + ' ' + units[i];
  }
  function fmtDate(s) {
    if (!s) return '';
    const d = new Date(s);
    if (isNaN(d)) return s;
    return d.toLocaleString(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }
  function fmtDuration(sec) {
    sec = Math.floor(sec); const d = Math.floor(sec / 86400), hh = Math.floor(sec % 86400 / 3600), mm = Math.floor(sec % 3600 / 60);
    return (d ? d + 'd ' : '') + hh + 'h ' + mm + 'm';
  }
  const pretty = (v) => { try { return JSON.stringify(typeof v === 'string' ? JSON.parse(v) : v, null, 2); } catch (e) { return typeof v === 'string' ? v : ''; } };

  // table(cols, rows): cols = [{h, cell(row), cls}] ; rows are objects.
  function table(cols, rows, empty = 'Nothing here yet.') {
    if (!rows.length) return h('div.table-wrap', h('div.empty', empty));
    return h('div.table-wrap', h('table',
      h('thead', h('tr', cols.map((c) => h('th', { class: c.cls || '' }, c.h)))),
      h('tbody', rows.map((r) => h('tr', { class: r._cls || '' }, cols.map((c) => h('td', { class: c.cls || '' }, c.cell(r))))))));
  }

  // jsonField(label, initial, {required}) — textarea with live validation
  // and a Format button. .value() returns the parsed object (or null when
  // empty and not required) and throws with a friendly message otherwise.
  function jsonField(label, initial, o = {}) {
    const ta = h('textarea.code', { spellcheck: false, readOnly: !!o.readOnly, value: initial ? pretty(initial) : '' });
    const status = h('div.hint');
    const check = () => {
      if (!ta.value.trim()) { status.textContent = o.required ? 'Required.' : ''; status.className = 'hint'; return; }
      try { JSON.parse(ta.value); status.textContent = 'Valid JSON'; status.className = 'hint ok'; }
      catch (e) { status.textContent = e.message; status.className = 'hint err'; }
    };
    ta.addEventListener('input', check); check();
    const fmt = h('button.btn.btn-sm', { type: 'button', onClick: () => { try { ta.value = pretty(ta.value); check(); } catch (e) { /* keep */ } } }, 'Format');
    const el = h('div.field', h('div.row.between', h('span.field-label', label), o.readOnly ? null : fmt), ta, status);
    el.value = () => {
      if (!ta.value.trim()) { if (o.required) throw new Error(label + ' is required'); return null; }
      try { return JSON.parse(ta.value); } catch (e) { throw new Error(label + ': ' + e.message); }
    };
    el.textarea = ta;
    return el;
  }

  // tagsField(initial) — editable key/value list; .value() returns [{key,value}].
  function tagsField(initial) {
    const tags = (initial || []).map((t) => ({ key: t.key, value: t.value }));
    const list = h('div.chips');
    const keyIn = h('input.input', { placeholder: 'Key', 'aria-label': 'Tag key' });
    const valIn = h('input.input', { placeholder: 'Value', 'aria-label': 'Tag value' });
    const render = () => { clear(list); tags.forEach((t, i) => list.append(h('span.chip', t.key + '=' + t.value,
      h('button', { type: 'button', 'aria-label': 'Remove ' + t.key, onClick: () => { tags.splice(i, 1); render(); } }, '×')))); };
    const add = () => { if (!keyIn.value.trim()) return; tags.push({ key: keyIn.value.trim(), value: valIn.value }); keyIn.value = valIn.value = ''; render(); keyIn.focus(); };
    const onKey = (e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } };
    keyIn.addEventListener('keydown', onKey); valIn.addEventListener('keydown', onKey);
    render();
    const el = h('div.field', h('span.field-label', 'Tags'), list, h('div.row', keyIn, valIn, h('button.btn', { type: 'button', onClick: add }, 'Add')));
    el.value = () => tags;
    return el;
  }

  // multiSelect(label, options, selected) — checkbox list; .value() → array.
  function multiSelect(label, options, selected) {
    const set = new Set(selected || []);
    const el = h('div.field', h('span.field-label', label), options.length ? options.map((o) =>
      h('label.check', h('input', { type: 'checkbox', checked: set.has(o), onChange: (e) => e.target.checked ? set.add(o) : set.delete(o) }), o))
      : h('div.hint', 'None available.'));
    el.value = () => [...set];
    return el;
  }

  const field = (label, input, hint) => h('label.field', h('span', label), input, hint ? h('div.hint', hint) : null);
  const badge = (text, kind) => h('span.badge' + (kind ? '.' + kind : ''), text);
  const copy = (text) => navigator.clipboard ? navigator.clipboard.writeText(text).then(() => toast('Copied', 'success', 1500)) : toast('Clipboard unavailable', 'error');

  window.ui = { h, clear, toast, error, modal, confirm, fmtBytes, fmtDate, fmtDuration, pretty, table, jsonField, tagsField, multiSelect, field, badge, copy };
})();
