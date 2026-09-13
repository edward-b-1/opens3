// views/objects.js — object browser with folder navigation, upload,
// download, delete, versions and a details panel.
window.views = window.views || {};

views.objects = async function (main, bucket, prefix) {
  const { h, clear, table, badge, fmtBytes, fmtDate, modal, confirm, toast, error, copy } = ui;
  const st = { entries: [], versions: false, next: null, selected: new Set(), loading: false, detail: null };
  const keyOf = (e) => e.prefix ? 'prefix:' + e.prefix : e.key + '\x00' + (e.versionId || '');
  const enc = encodeURIComponent;

  // Breadcrumb: Buckets / bucket / seg / seg.
  const segs = prefix.split('/').filter(Boolean);
  const crumbs = h('div.breadcrumb', h('a', { href: '#/buckets' }, 'Buckets'), h('span.sep', '/'),
    segs.length ? h('a', { href: '#' + app.objectsHash(bucket, '') }, bucket) : h('span.cur', bucket));
  segs.forEach((s, i) => {
    const p = segs.slice(0, i + 1).join('/') + '/';
    crumbs.append(h('span.sep', '/'), i === segs.length - 1 ? h('span.cur', s) : h('a', { href: '#' + app.objectsHash(bucket, p) }, s));
  });

  const filter = h('input.input.grow', { placeholder: 'Filter this folder (press / to focus)', 'data-filter': '1', onInput: () => renderTable() });
  const versionsBox = h('input', { type: 'checkbox', onChange: () => { st.versions = versionsBox.checked; load(true); } });
  const delBtn = h('button.btn.btn-danger', { disabled: true, onClick: () => removeSelected() }, 'Delete selected');
  const listWrap = h('div');
  const moreWrap = h('div', { style: 'margin-top:10px' });
  const panel = h('div.panel');
  const drop = h('div', { onDragover: (e) => { e.preventDefault(); drop.classList.add('over'); }, onDragleave: () => drop.classList.remove('over'),
    onDrop: (e) => { e.preventDefault(); drop.classList.remove('over'); if (e.dataTransfer.files.length) uploadFiles([...e.dataTransfer.files]); } }, listWrap, moreWrap);
  main.append(crumbs, h('div.toolbar', filter,
    h('label.check', { style: 'margin:0' }, versionsBox, 'Show versions'),
    h('button.btn', { onClick: () => load(true), title: 'Reload (r)' }, 'Refresh'),
    h('button.btn', { onClick: newFolder }, 'New folder'),
    h('button.btn.btn-primary', { 'data-upload': '1', onClick: uploadDialog, title: 'Upload (u)' }, 'Upload'),
    delBtn,
    h('a.btn', { href: api.zipURL(bucket, prefix), title: prefix ? 'Download this folder as a zip archive' : 'Download the whole bucket as a zip archive' }, prefix ? 'Download folder' : 'Download all'),
    prefix ? h('button.btn.btn-danger', { onClick: () => removePrefix(prefix) }, 'Delete folder') : null,
    h('a.btn', { href: '#' + app.settingsHash(bucket) }, 'Bucket settings')),
    h('div.split', drop, panel));

  async function load(reset) {
    if (st.loading) return;
    st.loading = true;
    if (reset) { st.entries = []; st.next = null; st.selected.clear(); }
    try {
      const params = { prefix, versions: st.versions ? '1' : '', max: settings.get('pageSize') };
      if (st.next) { params.after = st.next.after; params.afterVersion = st.next.afterVersion; }
      const r = await api.get('buckets/' + enc(bucket) + '/objects', params);
      st.entries = st.entries.concat(r.entries);
      st.next = r.truncated ? { after: r.nextAfter, afterVersion: r.nextAfterVersion } : null;
    } catch (e) { error(e); }
    st.loading = false;
    renderTable();
  }

  function renderTable() {
    const q = filter.value.trim().toLowerCase();
    const rows = st.entries.filter((e) => (e.prefix || e.key).toLowerCase().includes(q) && (settings.get('showMarkers') || e.prefix || e.key !== prefix));
    const name = (e) => (e.prefix || e.key).slice(prefix.length) || '(folder marker)';
    const all = h('input', { type: 'checkbox', 'aria-label': 'Select all', onChange: () => { rows.forEach((e) => { all.checked ? st.selected.add(keyOf(e)) : st.selected.delete(keyOf(e)); }); renderTable(); } });
    const cols = [
      { h: all, cell: (e) => h('input', { type: 'checkbox', 'aria-label': 'Select ' + (e.prefix || e.key), checked: st.selected.has(keyOf(e)), onChange: (ev) => { ev.target.checked ? st.selected.add(keyOf(e)) : st.selected.delete(keyOf(e)); updateSel(); } }) },
      { h: 'Name', cell: (e) => e.prefix
        ? h('a.name-cell', { href: '#' + app.objectsHash(bucket, e.prefix) }, ui.fileIcon(e.prefix, 'folder'), name(e))
        : h('a.cell-link.name-cell', { href: '#', onClick: (ev) => { ev.preventDefault(); showDetail(e); }, class: e.deleteMarker ? 'muted' : '' }, ui.fileIcon(e.key), name(e), e.deleteMarker ? [' ', badge('delete marker', 'warn')] : null, st.versions && e.isLatest ? [' ', badge('latest', 'ok')] : null) },
      { h: 'Size', cls: 'num', cell: (e) => e.prefix || e.deleteMarker ? '' : fmtBytes(e.size) },
      { h: 'Modified', cell: (e) => e.prefix ? '' : ui.dateCell(e.lastModified) },
    ];
    if (st.versions) cols.push({ h: 'Version', cell: (e) => e.versionId ? h('code', e.versionId.length > 16 ? e.versionId.slice(0, 16) + '…' : e.versionId) : '' });
    cols.push({ h: '', cls: 'actions-cell', cell: (e) => e.deleteMarker ? null : e.prefix ? [
      h('a.btn.btn-sm', { href: api.zipURL(bucket, e.prefix), title: 'Download as zip' }, 'Download'),
      h('button.btn.btn-sm.btn-danger', { onClick: () => removePrefix(e.prefix) }, 'Delete')] : [
      h('a.btn.btn-sm', { href: api.downloadURL(bucket, e.key, e.versionId), download: '' }, 'Download'),
      h('button.btn.btn-sm.btn-danger', { onClick: () => remove([e]) }, 'Delete')] });
    clear(listWrap);
    listWrap.append(table(cols, rows, st.loading ? 'Loading…' : (st.entries.length ? 'Nothing matches the filter.' : 'This folder is empty. Drop files here or use Upload.')));
    clear(moreWrap);
    if (st.next) moreWrap.append(h('button.btn', { onClick: () => load(false) }, 'Load more'));
    updateSel();
  }
  function updateSel() { delBtn.disabled = st.selected.size === 0; delBtn.textContent = st.selected.size ? 'Delete ' + st.selected.size + ' selected' : 'Delete selected'; }

  async function showDetail(e) {
    clear(panel);
    panel.append(h('div.card', h('div.empty', 'Loading…')));
    try {
      const o = await api.get('buckets/' + enc(bucket) + '/object', { key: e.key, versionId: st.versions ? e.versionId : '' });
      st.detail = o;
      const kv = (pairs) => h('dl.kv', pairs.filter((p) => p[1] !== undefined && p[1] !== null && p[1] !== '').map(([k, v]) => [h('dt', k), h('dd', v)]));
      const meta = Object.entries(o.userMeta || {});
      panel.append(h('div.card',
        h('div.row.between', h('h2.name-cell', { style: 'word-break:break-all' }, ui.fileIcon(o.key), o.key.split('/').pop()), h('button.btn.btn-ghost.icon-btn', { 'aria-label': 'Close details', onClick: () => clear(panel) }, '×')),
        h('div.row', { style: 'margin-bottom:12px' },
          h('a.btn.btn-primary.btn-sm', { href: api.downloadURL(bucket, o.key, o.versionId), download: '' }, 'Download'),
          h('a.btn.btn-sm', { href: api.downloadURL(bucket, o.key, o.versionId, true), target: '_blank', rel: 'noopener' }, 'Open'),
          h('button.btn.btn-sm', { onClick: () => copy(o.key) }, 'Copy key'),
          h('button.btn.btn-sm.btn-danger', { onClick: () => remove([{ key: o.key, versionId: st.versions ? o.versionId : '' }]) }, 'Delete')),
        kv([['Key', h('code', o.key)], ['Size', fmtBytes(o.size) + ' (' + o.size + ' bytes)'], ['Modified', ui.dateCell(o.lastModified)],
          ['ETag', h('code', o.etag)], ['Version', o.versionId ? h('code', o.versionId) : ''], ['Content type', o.contentType],
          ['Content encoding', o.contentEncoding], ['Cache control', o.cacheControl], ['Storage class', o.storageClass || 'STANDARD'],
          ['Parts', o.parts > 1 ? o.parts : ''], ['Owner', o.owner],
          ['Checksum', o.checksum ? h('code', o.checksum.algorithm + ' ' + o.checksum.value + ' (' + o.checksum.type + ')') : ''],
          ['Encryption', o.sse ? o.sse.type + (o.sse.kmsKeyId ? ' · ' + o.sse.kmsKeyId : '') : 'none'],
          ['Retention', o.retention ? o.retention.mode + ' until ' + fmtDate(o.retention.retainUntil) : ''], ['Legal hold', o.legalHold ? 'ON' : '']]),
        h('h2', { style: 'margin-top:14px' }, 'Metadata'), meta.length ? kv(meta) : h('p.muted', 'No user metadata.'),
        h('h2', { style: 'margin-top:14px' }, 'Tags'), o.tags.length ? h('div.chips', o.tags.map((t) => h('span.chip', t.key + '=' + t.value))) : h('p.muted', 'No tags.')));
    } catch (err) { clear(panel); error(err); }
  }

  async function remove(items) {
    const what = items.length === 1 ? '"' + items[0].key + '"' + (items[0].versionId && st.versions ? ' (version ' + items[0].versionId + ')' : '') : items.length + ' objects';
    const versioned = st.versions && items.some((i) => i.versionId);
    if (!await confirm('Delete', 'Delete ' + what + '?' + (versioned ? ' Specific versions are removed permanently.' : ''))) return;
    try {
      const r = await api.post('buckets/' + enc(bucket) + '/delete', { objects: items.map((i) => ({ key: i.key, versionId: st.versions ? i.versionId : undefined })) });
      const bad = r.results.filter((x) => x.error);
      if (bad.length) error(new Error(bad.length + ' failed: ' + bad[0].key + ': ' + bad[0].error));
      else toast(items.length === 1 ? 'Deleted' : 'Deleted ' + items.length + ' objects', 'success');
      clear(panel);
      load(true);
    } catch (e) { error(e); }
  }
  async function removeSelected() {
    const objs = st.entries.filter((e) => e.key && st.selected.has(keyOf(e)));
    const dirs = st.entries.filter((e) => e.prefix && st.selected.has(keyOf(e)));
    if (dirs.length) {
      if (!await confirm('Delete', 'Delete ' + dirs.length + ' folder' + (dirs.length > 1 ? 's' : '') + ' with everything inside' + (objs.length ? ' and ' + objs.length + ' object' + (objs.length > 1 ? 's' : '') : '') + '?' + (st.versions ? ' All versions are removed permanently.' : ''))) return;
      let deleted = 0, failed = 0;
      for (const d of dirs) {
        try {
          const r = await api.post('buckets/' + enc(bucket) + '/delete-prefix', { prefix: d.prefix, versions: st.versions });
          deleted += r.deleted; failed += r.failed;
        } catch (e) { failed++; error(e); }
      }
      if (failed) error(new Error(failed + ' could not be deleted (' + deleted + ' deleted)'));
      else toast('Deleted ' + deleted + ' objects from ' + dirs.length + ' folder' + (dirs.length > 1 ? 's' : ''), 'success');
      if (objs.length) await remove(objs); else load(true);
      return;
    }
    remove(objs);
  }

  // removePrefix deletes every object under a folder: a dry run counts
  // first so the confirmation can say what is at stake.
  async function removePrefix(p) {
    let count;
    try { count = await api.post('buckets/' + enc(bucket) + '/delete-prefix', { prefix: p, versions: st.versions, dryRun: true }); }
    catch (e) { error(e); return; }
    const what = count.matched === 0 ? 'The folder "' + p + '" is empty. Remove it?'
      : 'Delete "' + p + '" and the ' + count.matched + (st.versions ? ' version' : ' object') + (count.matched > 1 ? 's' : '') + ' inside (' + fmtBytes(count.bytes) + ')?' + (st.versions ? ' Versions are removed permanently.' : '');
    if (!await confirm('Delete folder', what)) return;
    const t = ui.toast('Deleting ' + p + '…', 'info', 0);
    try {
      const r = await api.post('buckets/' + enc(bucket) + '/delete-prefix', { prefix: p, versions: st.versions });
      t.remove();
      if (r.failed) error(new Error(r.failed + ' of ' + r.matched + ' could not be deleted' + (r.errors.length ? ': ' + r.errors[0].key + ': ' + r.errors[0].error : '')));
      else toast('Deleted ' + r.deleted + ' object' + (r.deleted === 1 ? '' : 's'), 'success');
    } catch (e) { t.remove(); error(e); }
    clear(panel);
    if (p === prefix) app.go(app.objectsHash(bucket, prefix.replace(/[^/]+\/$/, ''))); else load(true);
  }

  async function newFolder() {
    const name = h('input.input', { required: true, placeholder: 'folder-name' });
    const ok = await modal({ title: 'New folder', submit: 'Create', body: ui.field('Folder name', name, 'Folders are just key prefixes; an empty marker object is created.'),
      onSubmit: async () => {
        const n = name.value.trim().replace(/^\/+|\/+$/g, '');
        if (!n) throw new Error('name is required');
        await api.put('buckets/' + enc(bucket) + '/upload?key=' + enc(prefix + n + '/'), new Blob([]));
        return n;
      } });
    if (ok) app.go(app.objectsHash(bucket, prefix + ok + '/'));
  }

  async function uploadDialog() {
    const input = h('input', { type: 'file', multiple: true });
    const ok = await modal({ title: 'Upload to ' + bucket + '/' + prefix, submit: 'Upload', body: [ui.field('Files', input), h('p.muted.small', 'Files are stored under the current folder. You can also drag files onto the list.')],
      onSubmit: () => { if (!input.files.length) throw new Error('choose at least one file'); return [...input.files]; } });
    if (ok) uploadFiles(ok);
  }

  async function uploadFiles(files) {
    const bar = h('div.progress', h('div', { style: 'width:0%' }));
    const label = h('div.small.muted', 'Uploading ' + files.length + ' file' + (files.length > 1 ? 's' : '') + '…');
    const t = ui.toast(h('div', { style: 'min-width:240px' }, label, bar), 'info', 0);
    try {
      const r = await api.upload(bucket, files, prefix, (done, total) => { bar.firstChild.style.width = Math.round(done / total * 100) + '%'; label.textContent = 'Uploading… ' + fmtBytes(done) + ' / ' + fmtBytes(total); });
      t.remove();
      toast('Uploaded ' + r.uploaded.length + ' file' + (r.uploaded.length > 1 ? 's' : ''), 'success');
      load(true);
    } catch (e) { t.remove(); error(e); load(true); }
  }

  await load(true);
};
