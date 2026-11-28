import { writable, derived } from 'svelte/store';
import { api } from './api.js';

// servicesById holds every known service, keyed by id, as last reported by the server. It is the
// single source of truth the Apps view renders from.
const servicesById = writable({});

// systemSnapshot holds the latest "system" event (a system.Snapshot: host, containers, notices),
// or null before the first one arrives. The server samples faster while this stream is open; Shell
// keeps it open for as long as someone is signed in.
const systemSnapshot = writable(null);

// attention holds what needs attention now, worst first, as the server works it out for the alerts: every rule's
// conditions, each { key, rule, severity, service, name, title, message, view, firing, since }. null until the
// first "attention" event.
const attention = writable(null);

// reconnecting is true once the stream has been without an open connection for a few seconds.
const reconnecting = writable(false);

// lastEvent is when the stream last delivered an event (ms since epoch), 0 before the first.
const lastEvent = writable(0);

// synced is true once the stream has delivered its first service snapshot, false before it and after close():
// until then an empty service list means "not known yet", not "no services".
const synced = writable(false);

const RECONNECT_HINT_DELAY = 3000;
// A browser EventSource retries on its own only while the connection merely drops; once the
// server (or a proxy in front of it) answers with a non-2xx status — a 503 over the stream cap,
// a 401 after the session expired, a 502/504 during a restart — it sets readyState to CLOSED and
// gives up for good. RETRY_BASE/RETRY_MAX govern the retry loop we then run ourselves.
const RETRY_BASE = 5000;
const RETRY_MAX = 60000;
// The server sends a ping event every 25 s. A stream silent for longer than this has stalled (a laptop woke
// up, the network changed, a proxy dropped the connection without closing it), even though the browser still
// counts it open: it is replaced.
const STALL_AFTER = 70000;

// crowded is true while this tab has no live stream because the session already has one in every tab it may:
// the page then asks for its data now and then (see poll) and says why it is not live.
const crowded = writable(false);
// Without a stream the page asks this often, and looks whether a stream has come free.
const POLL_MS = 30000;

// offline is true while the browser reports no network at all.
const offline = writable(typeof navigator !== 'undefined' && navigator.onLine === false);

let source = null;
let reconnectTimer = null;
let retryTimer = null;
let stallTimer = null;
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

// alive notes that the stream still delivers, and restarts the stall watch.
function alive() {
  disarmReconnectHint();
  clearTimeout(stallTimer);
  stallTimer = setTimeout(stalled, STALL_AFTER);
}

function received() {
  alive();
  lastEvent.set(Date.now());
}

function stalled() {
  stallTimer = null;
  if (!source) return;
  source.close();
  source = null;
  armReconnectHint();
  scheduleRetry();
}

function applySnapshot(list) {
  const next = {};
  for (const svc of list) next[svc.id] = svc;
  servicesById.set(next);
  synced.set(true);
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
  alive();
  crowded.set(false);
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

// scheduleRetry retries after the current delay, spread by up to a fifth either way so that tabs do not all
// return to a restarted server at once, and doubles the delay for the next time.
function scheduleRetry() {
  if (retryTimer) return;
  clearTimeout(stallTimer);
  stallTimer = null;
  retryTimer = setTimeout(retry, retryDelay * (0.8 + Math.random() * 0.4));
  retryDelay = Math.min(retryDelay * 2, RETRY_MAX);
}

// Back online, a waiting retry runs at once; going offline shows as such instead of a retry that cannot work.
if (typeof window !== 'undefined') {
  window.addEventListener('online', () => {
    offline.set(false);
    if (!retryTimer) return;
    clearTimeout(retryTimer);
    retryDelay = RETRY_BASE;
    retry();
  });
  window.addEventListener('offline', () => offline.set(true));
}

// retry checks whether the session is still good before reopening: a stream closed because the
// session expired would otherwise just be closed again immediately, forever.
async function retry() {
  retryTimer = null;
  const g = gen;
  let unauthorized = false;
  let full = false;
  try {
    full = (await api('GET', '/api/session')).streamsFull === true;
  } catch (err) {
    unauthorized = err.status === 401;
    // Any other error (server unreachable, ...) is still worth retrying open() below.
  }
  if (g !== gen) return; // closed (e.g. logged out) while the check was in flight
  if (unauthorized) {
    signedOutHandler?.();
    return;
  }
  // Every stream the session may have is open in other tabs: opening one more would only be refused again. The
  // page asks for what a stream would have sent, and looks again in a while.
  if (full) {
    crowded.set(true);
    disarmReconnectHint();
    retryDelay = RETRY_BASE;
    retryTimer = setTimeout(retry, POLL_MS);
    await poll(g);
    return;
  }
  crowded.set(false);
  open();
}

// poll asks once for what the stream would have sent: the services, the system and what needs attention. What
// cannot be had this time is left as it was.
async function poll(g) {
  const [list, system, needs] = await Promise.all(
    ['/api/services', '/api/system', '/api/alerts/attention'].map((path) => api('GET', path).catch(() => null)));
  if (g !== gen) return;
  if (Array.isArray(list)) applySnapshot(list);
  if (system) systemSnapshot.set(system);
  if (Array.isArray(needs)) attention.set(needs);
  if (list || system) lastEvent.set(Date.now());
}

// open starts the single EventSource for the whole app. Calling it again while already open does
// nothing, so views can call it on mount without coordinating with each other.
function open() {
  if (source) return;
  armReconnectHint();
  source = new EventSource('/api/events');
  source.addEventListener('open', handleOpen);
  source.addEventListener('snapshot', (e) => {
    received();
    applySnapshot(JSON.parse(e.data));
  });
  source.addEventListener('service', (e) => {
    received();
    applyService(JSON.parse(e.data));
  });
  source.addEventListener('removed', (e) => {
    received();
    applyRemoved(JSON.parse(e.data).id);
  });
  source.addEventListener('system', (e) => {
    received();
    systemSnapshot.set(JSON.parse(e.data));
  });
  source.addEventListener('attention', (e) => {
    received();
    attention.set(JSON.parse(e.data));
  });
  source.addEventListener('ping', alive);
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
  clearTimeout(stallTimer);
  stallTimer = null;
  retryDelay = RETRY_BASE;
  disarmReconnectHint();
  crowded.set(false);
  lastEvent.set(0);
  synced.set(false);
  servicesById.set({});
  systemSnapshot.set(null);
  attention.set(null);
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

export { reconnecting, offline, crowded, lastEvent, synced, systemSnapshot, attention };

export const live = { open, close, setOnSignedOut };
