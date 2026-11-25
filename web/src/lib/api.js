// Thin fetch wrapper: JSON in and out, CSRF token on every change, errors carry the server's message.
let csrfToken = '';
let onUnauthorized = null;

export function setCsrf(token) {
  csrfToken = token || '';
}

// setUnauthorizedHandler registers the callback run whenever a call other than login, setup or
// session itself gets a 401: the session has ended server-side (expired, revoked, server
// restarted), so the UI should return to the login form instead of leaving the caller to show a
// dead end of its own.
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

// SESSION_PATHS are exempt from that handler: a 401 from one of these is the expected, ordinary
// result of not being signed in yet (or of a wrong password/token), handled by the caller itself.
const SESSION_PATHS = new Set(['/api/login', '/api/setup', '/api/session']);

export async function api(method, path, body) {
  const res = await send(method, path, body);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw failure(res, path, data);
  return data;
}

// download POSTs body and returns the file the server answers with: its content as a Blob and the name from
// Content-Disposition.
export async function download(path, body) {
  const res = await send('POST', path, body);
  if (!res.ok) throw failure(res, path, await res.json().catch(() => ({})));
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') || '')?.[1] || 'download';
  return { blob: await res.blob(), name };
}

async function send(method, path, body) {
  const headers = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  // Built before the try: only a failed fetch() itself (a network problem) should map to
  // "Cannot reach the server" — a body that fails to serialize is a different bug.
  const payload = body === undefined ? undefined : JSON.stringify(body);
  try {
    return await fetch(path, { method, headers, credentials: 'same-origin', body: payload });
  } catch (err) {
    if (err instanceof TypeError) throw new Error('Cannot reach the server', { cause: err });
    throw err;
  }
}

// failure is the error for a response that is not ok, carrying the server's message.
function failure(res, path, data) {
  if (res.status === 401 && !SESSION_PATHS.has(path)) onUnauthorized?.();
  const err = new Error(data.error || `Request failed (${res.status})`);
  err.status = res.status;
  return err;
}
