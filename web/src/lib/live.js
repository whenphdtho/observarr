import { writable, derived } from 'svelte/store';
import { api } from './api.js';

// servicesById holds every known service, keyed by id, as last reported by the server. It is the
// single source of truth the Apps view renders from.
const servicesById = writable({});

// systemSnapshot holds the latest "system" event (a system.Snapshot: host, containers, notices),
// or null before the first one arrives. The server samples faster while this stream is open, so
// the System view keeps it open via live.open() even when the Apps view isn't mounted.
const systemSnapshot = writable(null);

// reconnecting is true once the stream has been without an open connection for a few seconds.
const reconnecting = writable(false);

const RECONNECT_HINT_DELAY = 3000;
// A browser EventSource retries on its own only while the connection merely drops; once the
// server (or a proxy in front of it) answers with a non-2xx status — a 503 over the stream cap,
// a 401 after the session expired, a 502/504 during a restart — it sets readyState to CLOSED and
// gives up for good. RETRY_BASE/RETRY_MAX govern the retry loop we then run ourselves.
const RETRY_BASE = 5000;
const RETRY_MAX = 60000;

let source = null;
let reconnectTimer = null;
let retryTimer = null;
let retryDelay = RETRY_BASE;
let signedOutHandler = null;
// gen counts close() calls. retry() captures it before its await (the session check) and bails
// if it no longer matches afterwards, so a retry racing a logout can't reopen a stream the user
// already signed out of.
let gen = 0;

function armReconnectHint() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => reconnecting.set(true), RECONNECT_HINT_DELAY);
}

function disarmReconnectHint() {
  clearTimeout(reconnectTimer);
  reconnectTimer = null;
  reconnecting.set(false);
}

function applySnapshot(list) {
  const next = {};
  for (const svc of list) next[svc.id] = svc;
  servicesById.set(next);
}

function applyService(svc) {
  servicesById.update((cur) => ({ ...cur, [svc.id]: svc }));
}

function applyRemoved(id) {
  servicesById.update((cur) => {
    if (!(id in cur)) return cur;
    const next = { ...cur };
    delete next[id];
    return next;
  });
}

function handleOpen() {
  disarmReconnectHint();
  retryDelay = RETRY_BASE;
}

// handleError responds to the stream ending. A CLOSED readyState means the browser will not
// retry by itself (it only auto-retries while reconnecting, readyState CONNECTING); in that case
// we drop the dead source and schedule our own retry. Otherwise the browser is already retrying
// on its own, and the reconnecting hint (armed above) is all that's needed.
function handleError() {
  armReconnectHint();
  if (source && source.readyState === EventSource.CLOSED) {
    source = null;
    scheduleRetry();
  }
}

function scheduleRetry() {
  if (retryTimer) return;
  retryTimer = setTimeout(retry, retryDelay);
  retryDelay = Math.min(retryDelay * 2, RETRY_MAX);
}

// retry checks whether the session is still good before reopening: a stream closed because the
// session expired would otherwise just be closed again immediately, forever.
async function retry() {
  retryTimer = null;
  const g = gen;
  let unauthorized = false;
  try {
    await api('GET', '/api/session');
  } catch (err) {
    unauthorized = err.status === 401;
    // Any other error (server unreachable, ...) is still worth retrying open() below.
  }
  if (g !== gen) return; // closed (e.g. logged out) while the check was in flight
  if (unauthorized) {
    signedOutHandler?.();
    return;
  }
  open();
}

// open starts the single EventSource for the whole app. Calling it again while already open does
// nothing, so views can call it on mount without coordinating with each other.
function open() {
  if (source) return;
  armReconnectHint();
  source = new EventSource('/api/events');
  source.addEventListener('open', handleOpen);
  source.addEventListener('snapshot', (e) => {
    disarmReconnectHint();
    applySnapshot(JSON.parse(e.data));
  });
  source.addEventListener('service', (e) => {
    disarmReconnectHint();
    applyService(JSON.parse(e.data));
  });
  source.addEventListener('removed', (e) => {
    disarmReconnectHint();
    applyRemoved(JSON.parse(e.data).id);
  });
  source.addEventListener('system', (e) => {
    disarmReconnectHint();
    systemSnapshot.set(JSON.parse(e.data));
  });
  source.addEventListener('error', handleError);
}

// close ends the stream and clears everything it knew. Call it on logout: the next sign-in opens
// a fresh stream and gets a fresh snapshot.
function close() {
  gen++;
  if (source) {
    source.close();
    source = null;
  }
  clearTimeout(retryTimer);
  retryTimer = null;
  retryDelay = RETRY_BASE;
  disarmReconnectHint();
  servicesById.set({});
  systemSnapshot.set(null);
}

// setOnSignedOut registers the callback run when a retry discovers the session is gone (a stream
// that closed because it expired must not just be retried forever). The caller is expected to
// close the live stream itself and show the login view.
function setOnSignedOut(fn) {
  signedOutHandler = fn;
}

// serviceList is every known service, ordered by name, for the Apps view to render.
export const serviceList = derived(servicesById, (byId) =>
  Object.values(byId).sort((a, b) => a.name.localeCompare(b.name)),
);

export { reconnecting, systemSnapshot };

export const live = { open, close, setOnSignedOut };
