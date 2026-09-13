// api.js — thin client for /console/api. Every call returns parsed JSON or
// throws an Error with .code and .status. A 401 triggers the "unauthorized"
// event so the app can show the login screen.
(function () {
  const BASE = 'api/';

  class ApiError extends Error {
    constructor(status, code, message) { super(message); this.status = status; this.code = code; }
  }

  async function call(method, path, body, opts = {}) {
    const headers = { 'Accept': 'application/json' };
    if (method !== 'GET') headers['X-OpenS3-Console'] = '1';
    let payload;
    if (body !== undefined && !(body instanceof FormData) && !(body instanceof Blob)) {
      headers['Content-Type'] = 'application/json';
      payload = JSON.stringify(body);
    } else {
      payload = body;
    }
    let res;
    try {
      res = await fetch(BASE + path, { method, headers, body: payload, credentials: 'same-origin' });
    } catch (e) {
      throw new ApiError(0, 'NetworkError', 'cannot reach the server');
    }
    if (res.status === 401 && !opts.quiet401) {
      window.dispatchEvent(new CustomEvent('unauthorized'));
    }
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
    if (!res.ok) {
      const err = (data && data.error) || {};
      throw new ApiError(res.status, err.code || 'Error', err.message || (res.status + ' ' + res.statusText));
    }
    return data;
  }

  function qs(params) {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(params || {})) if (v !== undefined && v !== null && v !== '') p.set(k, v);
    const s = p.toString();
    return s ? '?' + s : '';
  }

  // upload sends files with XMLHttpRequest so that progress events work.
  function upload(bucket, files, prefix, onProgress) {
    return new Promise((resolve, reject) => {
      const fd = new FormData();
      fd.append('prefix', prefix || '');
      for (const f of files) fd.append('file', f, f.name);
      const xhr = new XMLHttpRequest();
      xhr.open('POST', BASE + 'buckets/' + encodeURIComponent(bucket) + '/upload');
      xhr.setRequestHeader('X-OpenS3-Console', '1');
      xhr.upload.onprogress = (e) => { if (e.lengthComputable && onProgress) onProgress(e.loaded, e.total); };
      xhr.onload = () => {
        let data = null;
        try { data = JSON.parse(xhr.responseText); } catch (e) { /* ignore */ }
        if (xhr.status >= 200 && xhr.status < 300) return resolve(data);
        const err = (data && data.error) || {};
        if (xhr.status === 401) window.dispatchEvent(new CustomEvent('unauthorized'));
        reject(new ApiError(xhr.status, err.code || 'Error', err.message || ('upload failed: ' + xhr.status)));
      };
      xhr.onerror = () => reject(new ApiError(0, 'NetworkError', 'upload failed'));
      xhr.send(fd);
    });
  }

  window.api = {
    ApiError,
    get: (p, params) => call('GET', p + qs(params)),
    post: (p, b) => call('POST', p, b),
    put: (p, b) => call('PUT', p, b),
    patch: (p, b) => call('PATCH', p, b),
    del: (p) => call('DELETE', p),
    login: (accessKey, secretKey) => call('POST', 'login', { accessKey, secretKey }, { quiet401: true }),
    me: () => call('GET', 'me', undefined, { quiet401: true }),
    upload,
    qs,
    downloadURL: (bucket, key, versionId, inline) =>
      BASE + 'buckets/' + encodeURIComponent(bucket) + '/download' + qs({ key, versionId, inline: inline ? '1' : '' }),
    zipURL: (bucket, prefix) => BASE + 'buckets/' + encodeURIComponent(bucket) + '/zip' + qs({ prefix }),
  };
})();
