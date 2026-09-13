// icons.js — file-type icons for the object browser: small inline SVGs
// chosen by extension, drawn with currentColor so they follow the theme.
(function () {
  const P = (d) => '<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + d + '</svg>';
  const page = '<path d="M4 1.5h5l3.5 3.5v9.5h-8.5z"/><path d="M9 1.5V5h3.5"/>';
  const svgs = {
    folder: P('<path d="M1.5 4a1 1 0 0 1 1-1h3.2l1.6 1.6h6.2a1 1 0 0 1 1 1V12a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1z"/>'),
    file: P(page),
    image: P(page + '<circle cx="6.3" cy="7.8" r="1"/><path d="M4.5 13l3-3.5 2 2 1.2-1.4 1.8 2.9"/>'),
    video: P('<rect x="1.5" y="3.5" width="9.5" height="9" rx="1"/><path d="M11 7l3.5-2v6L11 9z"/>'),
    audio: P('<path d="M6 12.5V4l7-1.5V11"/><circle cx="4" cy="12.5" r="2"/><circle cx="11" cy="11" r="2"/>'),
    archive: P(page + '<path d="M7 1.5v11M6 3h2M6 5h2M6 7h2M6 9h2"/>'),
    pdf: P(page + '<path d="M5.5 12.5c1-3 2-6 2.5-7.5 0 3 1.5 5 3.5 6-2 0-4 .5-6 1.5z"/>'),
    sheet: P(page + '<path d="M5 7.5h6M5 10h6M8 7.5v5"/>'),
    doc: P(page + '<path d="M5.5 7.5h5M5.5 9.5h5M5.5 11.5h3"/>'),
    slides: P('<rect x="1.5" y="2.5" width="13" height="9" rx="1"/><path d="M8 11.5v3M5.5 14.5h5"/>'),
    code: P(page + '<path d="M6.5 8l-1.5 1.5 1.5 1.5M9.5 8l1.5 1.5-1.5 1.5"/>'),
    data: P(page + '<path d="M6 8h4M6 10h4M6 12h2"/>'),
    binary: P(page + '<path d="M6 8.5h1v3H6zM9 8.5h1v3H9z"/>'),
  };
  const kinds = {
    image: 'png jpg jpeg gif webp svg bmp tif tiff heic heif avif ico psd raw',
    video: 'mp4 mov mkv avi webm m4v mpg mpeg wmv',
    audio: 'mp3 wav flac ogg m4a aac wma opus aiff',
    archive: 'zip tar gz tgz bz2 xz 7z rar zst lz4 tbz jar',
    pdf: 'pdf',
    sheet: 'csv tsv xls xlsx xlsm ods parquet numbers',
    doc: 'doc docx odt rtf txt md markdown pages log',
    slides: 'ppt pptx odp key',
    code: 'js mjs ts tsx jsx py go rs java c h cc cpp hpp cs rb php sh bash zsh pl lua swift kt scala sql html htm css scss',
    data: 'json yaml yml toml xml ini cfg conf env properties proto avro',
    binary: 'bin exe dmg iso img so dll deb rpm apk wasm o a',
  };
  const byExt = {};
  for (const [k, list] of Object.entries(kinds)) for (const ext of list.split(' ')) byExt[ext] = k;

  // fileKind(name) → icon kind for a key or file name.
  const fileKind = (name) => {
    const base = name.split('/').pop();
    const i = base.lastIndexOf('.');
    return (i > 0 && byExt[base.slice(i + 1).toLowerCase()]) || 'file';
  };
  // fileIcon(name, kind?) → a span.ficon carrying the SVG; kind 'folder' for prefixes.
  const fileIcon = (name, kind) => {
    const k = kind || fileKind(name);
    const el = document.createElement('span');
    el.className = 'ficon ficon-' + k;
    el.innerHTML = svgs[k] || svgs.file;
    return el;
  };
  window.ui = Object.assign(window.ui || {}, { fileIcon, fileKind });
})();
