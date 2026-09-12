// views/status.js — server information and disk usage.
window.views = window.views || {};

views.status = async function (main) {
  const { h, fmtBytes, fmtDate, fmtDuration } = ui;
  const info = await api.get('info');
  const stat = (k, v) => h('div.stat', h('div.v', v), h('div.k', k));
  const stats = [stat('Version', info.version), stat('Region', info.region), stat('Uptime', fmtDuration(info.uptimeSeconds))];
  if (info.admin) stats.push(stat('Buckets', info.buckets), stat('Users', info.users), stat('Access keys', info.accessKeys));
  main.append(h('h1', 'Status'), h('div.grid', stats));
  if (!info.admin) {
    main.append(h('p.muted', { style: 'margin-top:16px' }, 'Disk usage and host details require the admin:ServerInfo permission (the built-in diagnostics or consoleAdmin policies).'));
    return;
  }
  const d = info.disk;
  const pct = d && d.total ? Math.round(d.used / d.total * 100) : 0;
  main.append(h('div.card', { style: 'margin-top:16px' }, h('h2', 'Disk'),
    d ? [h('div.row.between', h('span', fmtBytes(d.used) + ' used of ' + fmtBytes(d.total)), h('span.muted', fmtBytes(d.free) + ' free · ' + pct + '%')),
      h('div.progress', { style: 'margin-top:8px', role: 'progressbar', 'aria-valuenow': pct, 'aria-valuemin': 0, 'aria-valuemax': 100 }, h('div', { style: 'width:' + pct + '%' + (pct > 90 ? ';background:var(--danger)' : '') }))]
      : h('p.err', info.diskError || 'unavailable')),
    h('div.card', h('h2', 'Server'), h('dl.kv',
      h('dt', 'Host'), h('dd', info.hostname || ''), h('dt', 'Platform'), h('dd', info.os + ' · ' + info.goVersion),
      h('dt', 'Started'), h('dd', fmtDate(info.startedAt)), h('dt', 'Account ID'), h('dd', h('code', info.accountId)),
      h('dt', 'Goroutines'), h('dd', info.goroutines), h('dt', 'Memory'), h('dd', fmtBytes(info.memory.heap) + ' heap · ' + fmtBytes(info.memory.sys) + ' from OS'))),
    h('div.card', h('h2', 'Endpoints'), h('dl.kv',
      h('dt', 'S3 API'), h('dd', h('code', location.origin + '/')), h('dt', 'Health'), h('dd', h('code', '/opens3/health/live'), ' · ', h('code', '/opens3/health/ready')),
      h('dt', 'Metrics'), h('dd', h('code', '/opens3/metrics')), h('dt', 'Console'), h('dd', h('code', '/console/')))));
};
