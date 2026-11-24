// URL helpers shared by the forms that add or change a service.

// A URL pointing here is almost always a mistake: Observarr would talk to its own container,
// never the app being added.
const SELF_HOSTNAMES = new Set(['127.0.0.1', 'localhost', '::1']);

export function hostOf(raw) {
  try {
    return new URL(raw).host;
  } catch {
    return raw;
  }
}

// originOf is scheme+host (host already includes a non-default port) — comparing it is how the
// UI tells "the address changed" from "only the path changed".
export function originOf(raw) {
  try {
    const u = new URL(raw);
    return `${u.protocol}//${u.host}`;
  } catch {
    return raw;
  }
}

// selfHostOf returns the URL's host name when it points at Observarr's own container, else ''.
export function selfHostOf(raw) {
  if ((raw ?? '').trim() === '') return '';
  let h = '';
  try {
    h = new URL(raw).hostname.replace(/^\[|\]$/g, '');
  } catch {
    return '';
  }
  return SELF_HOSTNAMES.has(h) ? h : '';
}

// withHost fills a host into a host-less suggestion URL (Go's url.URL.String() with an empty
// host, e.g. "http://:8989/app"), which the browser's URL parser refuses to parse at all.
export function withHost(raw, host) {
  const m = /^(https?):\/\/(?::(\d+))?(\/[^?#]*)?$/i.exec(raw ?? '');
  const scheme = m ? m[1].toLowerCase() : 'http';
  const port = m?.[2] ?? '';
  const path = m?.[3] ?? '';
  return `${scheme}://${host}${port ? ':' + port : ''}${path}`;
}

// The docs on GitHub: the app does not serve its own docs, so links to them are absolute.
const REPO = 'https://github.com/whenphdtho/observarr';
export const DEPLOY_DOCS = `${REPO}/blob/main/docs/deploy.md`;
export const ADDING_APPS_DOCS = `${REPO}#adding-your-apps`;

// connectDocs links to the guide's "Connect <app>" section, using GitHub's heading anchors.
export function connectDocs(appName) {
  return `${DEPLOY_DOCS}#connect-${appName.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`;
}
