import { tone, queuePaused } from './status.js';
import { streamsOf } from './streams.js';
import { formatSpan, formatLeft, formatPercent, formatRate, plural, prettyRelease, sentence, stripSource } from './format.js';

// What the Overview says, worked out apart from how it is drawn: the page's title, the rows that need attention
// and what each explains when opened, and the lines of the downloads and streams. It reads only the server's
// attention list and the generic shapes, never anything app-specific.

// tally counts the rows that need someone, as the title, the tab, the views list and the bell all say it:
// { fails, warns }, the apps (and things about no app) with a problem, and those with only warnings. What is
// muted is not counted.
export function tally(items) {
  const rows = issues(items, 0).rows.filter((r) => !r.muted);
  const fails = rows.filter((r) => r.tone === 'fail').length;
  return { fails, warns: rows.length - fails };
}

// situation is the Overview's title, the state of the whole in one line, and the count the tab and the views list
// carry: { title, count, tone }. items is the attention list (null before it arrived), apps how many apps are
// watched, checked whether each of them has been checked once. Errors come before warnings; an all-clear is only
// given once everything was looked at, and never for an empty list.
export function situation(items, apps, checked) {
  if (!items) return { title: 'Overview', count: 0, tone: '' };
  const { fails, warns } = tally(items);
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

const clock = (ms) => new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

// What an item says when its row is opened, after the app's own words: what it means and what helps. Told by the
// rule that raised it, so it holds for any app.
const WHY = {
  down: 'Observarr gets no answer from it. It may have stopped or be restarting.',
  container: 'Docker reports this state. The container’s log says why.',
  health: 'The app reports this about itself. Open it to see what it suggests.',
  storage: 'Free some space or raise the limit, so that new files can be written.',
  traffic: 'Everything the server sent and received counts towards the budget you set.',
  security: 'Someone keeps trying to sign in to Observarr and fails.',
  downloads: 'It will not finish on its own. Open the app to retry it or remove it.',
};

// issues turns the attention list into the rows that need someone and the notes that are only good to know.
// Everything about one app is one row: its marker has the worst severity, its title names the worst thing and
// how many more there are, its line says the first in full. A row is
// { key, tone, service, name, view, title, detail, more, value, muted, mutedUntil, items }.
//   - items are what it stands for, worst first, each { key, tone, title, text, mutedUntil }: title its short
//     name, text the app's message in full, what it means and since when, mutedUntil a time in ms or 0.
//   - value is the figure of its first item: the server's, or for a thing that is down how long it has been.
//   - muted says every item is muted (mutedUntil then is when the first mute ends): such a row is listed last,
//     counts for nothing, and its tone is "idle".
// Errors come before warnings, what is muted last.
export function issues(items, nowMs) {
  const rows = [];
  const notes = [];
  const byService = new Map();
  for (const it of items ?? []) {
    const t = tone(it.severity);
    if (!t) {
      notes.push({ key: it.key, text: `${it.name ? `${it.name}: ` : ''}${it.message}`, view: it.view, service: it.service });
      continue;
    }
    const until = new Date(it.mutedUntil ?? 0).getTime();
    const began = new Date(it.since ?? 0).getTime();
    const full = sentence(it.message ?? '');
    const one = {
      key: it.key, tone: t, mutedUntil: until > nowMs ? until : 0, line: detail(it.message ?? ''), view: it.view,
      heading: it.title,
      // The name of the app leads the row already: an item is named by the rest of its title.
      title: sentence(it.name && it.title.startsWith(`${it.name}: `) ? it.title.slice(it.name.length + 2) : it.title),
      value: it.value || (it.rule === 'down' ? since(it.since, nowMs) : ''),
      text: [full && `${full}${/[.!?…]$/.test(full) ? '' : '.'}`, WHY[it.rule === 'down' && !it.service ? 'container' : it.rule] ?? '',
        began > 0 ? `Since ${clock(began)}.` : ''].filter(Boolean).join(' '),
    };
    let row = it.service ? byService.get(it.service) : null;
    if (!row) {
      row = { key: it.service ? `app/${it.service}` : it.key, service: it.service ?? null, name: it.name, items: [] };
      if (it.service) byService.set(it.service, row);
      rows.push(row);
    }
    row.items.push(one);
  }
  const rank = (i) => (i.mutedUntil ? 2 : i.tone === 'fail' ? 0 : 1);
  for (const row of rows) {
    row.items.sort((a, b) => rank(a) - rank(b));
    const [lead] = row.items;
    const live = row.items.filter((i) => !i.mutedUntil);
    row.muted = live.length === 0;
    row.mutedUntil = row.muted ? Math.min(...row.items.map((i) => i.mutedUntil)) : 0;
    row.tone = row.muted ? 'idle' : lead.tone;
    row.more = row.items.length - 1;
    row.title = row.more ? `${lead.heading}, ${row.more} more` : lead.heading;
    // A line that only repeats the title is left out.
    row.detail = row.muted ? `Muted until ${clock(row.mutedUntil)}` : lead.line.toLowerCase() === lead.title.toLowerCase() ? '' : lead.line;
    row.value = row.muted ? '' : lead.value;
    row.view = lead.view;
  }
  const order = { fail: 0, warn: 1, idle: 2 };
  rows.sort((a, b) => order[a.tone] - order[b.tone]);
  return { rows, notes };
}

// mutedText says until when an item is muted: "Muted until 14:30".
export function mutedText(untilMs) {
  return `Muted until ${clock(untilMs)}`;
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
