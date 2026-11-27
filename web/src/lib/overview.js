import { tone, queuePaused } from './status.js';
import { streamsOf } from './streams.js';
import { formatSpan, formatLeft, formatPercent, formatRate, plural, prettyRelease, sentence, stripSource } from './format.js';

// What the Overview says, worked out apart from how it is drawn: the page's title, the rows that need attention
// and what each explains when opened, and the lines of the downloads and streams. It reads only the server's
// attention list and the generic shapes, never anything app-specific.

// situation is the Overview's title, the state of the whole in one line, and the count the tab and the views list
// carry: { title, count, tone }. items is the attention list (null before it arrived), apps how many apps are
// watched, checked whether each of them has been checked once. Errors come before warnings; an all-clear is only
// given once everything was looked at, and never for an empty list.
export function situation(items, apps, checked) {
  if (!items) return { title: 'Overview', count: 0, tone: '' };
  // It counts the rows the Overview lists, so the title and the list agree.
  const { rows } = issues(items, 0);
  const fails = rows.filter((r) => r.tone === 'fail').length;
  const warns = rows.length - fails;
  if (fails) return { title: `${plural(fails, 'problem')} need${fails === 1 ? 's' : ''} you`, count: fails, tone: 'fail' };
  if (warns) return { title: `${plural(warns, 'warning')} to look at`, count: warns, tone: 'warn' };
  if (apps === 0) return { title: 'Overview', count: 0, tone: '' };
  if (!checked) return { title: apps === 1 ? 'Checking your app…' : `Checking ${apps} apps…`, count: 0, tone: '' };
  return { title: apps === 1 ? 'Your app is fine' : `All ${apps} apps are fine`, count: 0, tone: 'ok' };
}

// since says how long ago a condition began, coarsely, as a row's figure: "40 s", "12 min", "3 h 5 min".
export function since(at, nowMs) {
  const t = new Date(at).getTime();
  if (!(t > 0)) return '';
  const s = Math.max(0, (nowMs - t) / 1000);
  return formatSpan(s < 60 ? s : Math.floor(s / 60) * 60);
}

// detail is an attention item's message as one plain line: an app's check name is dropped from its front, and
// a release it names (a word with dots in it) is made readable.
function detail(message) {
  const plain = stripSource(message).split(' ').map((w) => {
    const stop = /[:,;]$/.exec(w)?.[0] ?? '';
    const name = stop ? w.slice(0, -1) : w;
    return /[._].*[._]/.test(name) ? prettyRelease(name) + stop : w;
  }).join(' ');
  return sentence(plain);
}

// issues turns the attention list into the rows that need someone, errors before warnings, and the notes that are
// only good to know. Several things about one app at one severity make one row with their count, so an app is
// not listed five times. A row is { key, tone, rule, service, name, view, title, detail, value, items }, items
// what it stands for (each { message, rule, began }) and value the figure that sums it up: the server's, how long
// a thing has been down, or how many it stands for.
export function issues(items, nowMs) {
  const rows = [];
  const notes = [];
  const groups = new Map();
  for (const it of items ?? []) {
    const t = tone(it.severity);
    if (!t) {
      notes.push({ key: it.key, text: `${it.name ? `${it.name}: ` : ''}${it.message}`, view: it.view, service: it.service });
      continue;
    }
    const one = { message: it.message, rule: it.rule, began: it.since };
    const group = it.service ? groups.get(`${it.service}/${t}`) : null;
    if (group) {
      group.items.push(one);
      continue;
    }
    const row = {
      key: it.key, tone: t, rule: it.rule, service: it.service ?? null, name: it.name, view: it.view,
      title: it.title, detail: detail(it.message), items: [one],
      value: it.value || (it.rule === 'down' ? since(it.since, nowMs) : ''),
    };
    if (it.service) groups.set(`${it.service}/${t}`, row);
    rows.push(row);
  }
  for (const row of rows) {
    const n = row.items.length;
    if (n === 1) continue;
    row.title = `${row.name}: ${plural(n, row.tone === 'fail' ? 'problem' : 'warning')}`;
    row.detail = row.items.map((i) => detail(i.message)).join(' · ');
    row.value = String(n);
  }
  // The server sends worst first; errors stay ahead of warnings whatever order arrives.
  rows.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === 'fail' ? -1 : 1));
  return { rows, notes };
}

// What a row says when opened, after the app's own words: what it means and what helps. Told by the rule that
// raised it, so it holds for any app.
const WHY = {
  down: 'Observarr gets no answer from it. It may have stopped or be restarting.',
  container: 'Docker reports this state. The container’s log says why.',
  health: 'The app reports this about itself. Open it to see what it suggests.',
  storage: 'Free some space or raise the limit, so that new files can be written.',
  traffic: 'Everything the server sent and received counts towards the budget you set.',
  security: 'Someone keeps trying to sign in to Observarr and fails.',
  downloads: 'It will not finish on its own. Open the app to retry it or remove it.',
};

// explain is the text of an opened row, a paragraph for each thing it stands for: the app's message in full, as
// it gave it, where the row shows it shortened or reworded (an opened row shows its own line unshortened); what it
// means and what helps; and since when.
export function explain(row) {
  return row.items.map((it) => {
    const full = sentence(it.message ?? '');
    const said = full && (row.items.length > 1 || full !== row.detail) ? `${full}${/[.!?…]$/.test(full) ? '' : '.'}` : '';
    const why = WHY[it.rule === 'down' && row.service === null ? 'container' : it.rule] ?? '';
    const t = new Date(it.began).getTime();
    const when = t > 0 ? `Since ${new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}.` : '';
    return [said, why, when].filter(Boolean).join(' ');
  });
}

// The view a row leads to, as its action says it.
const VIEWS = { '#/storage': 'View storage', '#/security': 'View security', '#/network': 'View network',
  '#/system': 'View containers', '#/apps': 'Show in Apps' };

// actions are what an opened row offers, the first one primary: the app itself for a problem only it can solve,
// else the view that shows the details. Each is { label, href, external } or { label, jump } (the app's card).
// svc is the service the row is about, if any; only an http(s) address becomes a link.
export function actions(row, svc) {
  const web = svc && /^https?:\/\//i.test(svc.url ?? '') ? { label: `Open ${svc.name}`, href: svc.url, external: true } : null;
  const card = svc ? { label: VIEWS['#/apps'], jump: svc.id } : null;
  const view = row.view !== '#/apps' && VIEWS[row.view] ? { label: VIEWS[row.view], href: row.view } : null;
  return (view ? [view, web] : [web, card]).filter(Boolean);
}

// downloads sums up the queues of services and lists their items for the Overview: what moves first, then what is
// stuck, then what waits. An item two apps report (a manager and its downloader) is listed once. It returns
// { any, rows, summary }, any false when no service has a queue, each row { key, title, raw, note, tone, pct, idle },
// note the time left, or its state in a word.
export function downloads(services) {
  const seen = new Map();
  let any = false;
  let speed = 0;
  let pausedOnly = false;
  for (const svc of services) {
    if (!svc.kinds?.includes('queue')) continue;
    any = true;
    const q = svc.readings?.queue;
    if (!q?.data || q.error) continue;
    const paused = queuePaused(svc);
    pausedOnly ||= paused;
    speed += q.data.speed ?? 0;
    for (const it of q.data.items) {
      const stuck = !!it.problem || it.status === 'failed' || it.status === 'warning';
      const moving = !stuck && !paused && it.status === 'downloading';
      const row = {
        key: it.title, raw: it.title, title: prettyRelease(it.title), pct: formatPercent(it.progress),
        rank: moving ? 0 : stuck ? 1 : 2, tone: stuck ? 'warn' : '', idle: !moving && !stuck,
        hint: it.problem || '',
        note: stuck ? 'Stuck' : paused ? 'Paused' : moving ? formatLeft(it.eta) || formatPercent(it.progress) : sentence(it.status),
      };
      const had = seen.get(it.title);
      if (!had || row.rank < had.rank || (row.rank === had.rank && it.progress > had.progress)) seen.set(it.title, { ...row, progress: it.progress });
    }
  }
  const rows = [...seen.values()].sort((a, b) => a.rank - b.rank || b.progress - a.progress);
  const count = (rank) => rows.filter((r) => r.rank === rank).length;
  const active = count(0);
  // What is stuck is said where it needs someone, in the rows above: the summary stays short.
  const summary = [
    pausedOnly && !active ? 'Paused' : '',
    active ? `${active} active` : '',
    count(2) ? `${count(2)} queued` : '',
    speed > 0 ? formatRate(speed) : '',
  ].filter(Boolean).join(' · ') || (rows.length ? plural(rows.length, 'download') : 'Nothing in the queue');
  return { any, rows, summary };
}

// playing lists what media servers play for the Overview: { rows, total, summary }, each row { key, title, note,
// tone, pct, idle }; total counts every stream, listed or not. A transcode is marked: it is what costs the server.
export function playing(services) {
  const rows = [];
  let bitrate = 0;
  let total = 0;
  for (const svc of services) {
    const data = svc.kinds?.includes('streams') ? streamsOf(svc) : null;
    if (!data) continue;
    bitrate += data.bitrate;
    total += data.total;
    for (const s of data.streams) {
      rows.push({
        key: `${svc.id}|${s.user}|${s.client}|${s.device ?? ''}|${s.title}`,
        title: [s.title, s.user].filter(Boolean).join(' · '), raw: s.title, pct: formatPercent(s.progress), idle: s.paused,
        hint: s.reasons?.join(', ') ?? '',
        note: s.paused ? 'Paused' : s.method === 'transcode' ? 'Transcode' : 'Direct',
        tone: !s.paused && s.method === 'transcode' ? 'warn' : '',
      });
    }
  }
  const summary = [plural(total, 'viewer'), bitrate > 0 ? formatRate(bitrate / 8) : ''].filter(Boolean).join(' · ');
  return { rows, total, summary };
}
