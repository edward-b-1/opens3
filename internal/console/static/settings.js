// settings.js — per-browser console preferences kept in localStorage.
// Loaded before ui.js so the formatters can read them. Density and theme
// are applied as attributes on <html> and handled purely in CSS; the
// other settings are read by the formatters and views, which re-render on
// the "settingschange" event.
(function () {
  const KEY = 'opens3.console.settings';
  const defaults = {
    density: 'comfortable',   // comfortable | compact
    theme: 'system',          // system | light | dark
    dateStyle: 'absolute',    // absolute | relative
    timeZone: 'local',        // local | utc
    sizeUnits: 'binary',      // binary (KiB) | decimal (kB) | bytes
    pageSize: 300,            // rows fetched per listing page
    showMarkers: true,        // show empty folder marker objects
  };
  const options = {
    density: ['comfortable', 'compact'], theme: ['system', 'light', 'dark'], dateStyle: ['absolute', 'relative'],
    timeZone: ['local', 'utc'], sizeUnits: ['binary', 'decimal', 'bytes'], pageSize: [100, 300, 1000], showMarkers: [true, false],
  };
  let current = load();

  function load() {
    let saved = {};
    try { saved = JSON.parse(localStorage.getItem(KEY) || '{}') || {}; } catch (e) { saved = {}; }
    const out = { ...defaults };
    for (const k of Object.keys(defaults)) if (k in saved && options[k].includes(saved[k])) out[k] = saved[k];
    return out;
  }
  function persist() {
    try { localStorage.setItem(KEY, JSON.stringify(current)); } catch (e) { /* private mode or quota: keep in memory */ }
  }
  function apply() {
    const root = document.documentElement;
    root.setAttribute('data-density', current.density);
    if (current.theme === 'system') root.removeAttribute('data-theme'); else root.setAttribute('data-theme', current.theme);
  }
  const get = (k) => current[k];
  const all = () => ({ ...current });
  function set(patch) {
    const before = { ...current };
    for (const [k, v] of Object.entries(patch)) if (k in defaults && options[k].includes(v)) current[k] = v;
    persist();
    apply();
    const changed = Object.keys(defaults).filter((k) => before[k] !== current[k]);
    if (changed.length) window.dispatchEvent(new CustomEvent('settingschange', { detail: { changed, settings: all() } }));
  }
  function reset() { current = { ...defaults }; persist(); apply(); window.dispatchEvent(new CustomEvent('settingschange', { detail: { changed: Object.keys(defaults), settings: all() } })); }

  apply();
  window.settings = { get, set, all, reset, defaults, options };
})();
