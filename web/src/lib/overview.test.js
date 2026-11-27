import { test } from 'node:test';
import assert from 'node:assert/strict';
import { situation, tally, since, issues, mutedText, actions, downloads, playing } from './overview.js';
import { serviceLine, cardSummary } from './status.js';
import { bucket } from './chart.js';
import { byPurpose } from './traffic.js';

const item = (severity, extra = {}) => ({ key: `${severity}/${extra.title ?? ''}`, rule: 'health', severity, name: 'Sintel',
  title: 'Sintel reports a problem', message: 'IndexerSearchCheck: No indexers available', view: '#/apps', ...extra });

test('the title states the situation', () => {
  assert.deepEqual(situation(null, 3, true), { title: 'Overview', count: 0, tone: '' });
  assert.equal(situation([item('critical'), item('critical'), item('warning')], 10, true).title, '2 problems need you');
  assert.equal(situation([item('critical')], 10, true).title, '1 problem needs you');
  assert.deepEqual(situation([item('warning'), item('warning'), item('info')], 10, true),
    { title: '2 warnings to look at', count: 2, tone: 'warn' });
  assert.equal(situation([item('info')], 10, true).title, 'All 10 apps are fine');
  assert.equal(situation([], 1, true).title, 'Your app is fine');
});

test('no all-clear before everything was checked, or without apps', () => {
  assert.equal(situation([], 0, true).title, 'Overview');
  assert.equal(situation([], 4, false).title, 'Checking 4 apps…');
  assert.equal(situation([], 4, false).tone, '');
});

test('one row per app, the worst thing first', () => {
  const now = Date.parse('2026-11-27T12:00:00Z');
  const { rows, notes } = issues([
    item('warning', { key: 'w1', service: 2, name: 'Mounts', title: 'Mounts: 1 file not uploaded', message: 'media: 1 cached file could not be uploaded' }),
    item('info', { rule: 'requests', name: 'Requests', message: '3 requests wait for approval' }),
    item('critical', { key: 'd4', rule: 'down', service: 4, title: 'Sintel is not reachable', message: 'connection refused',
      since: '2026-11-27T11:48:10Z' }),
    item('critical', { key: 'c2', service: 2, name: 'Mounts', title: 'Mounts: cache is full', message: 'cloud:archive: the cache disk is out of space' }),
    item('warning', { key: 'disk', rule: 'storage', title: 'Disk / nearly full', name: '/', message: '92% used', value: '92%', view: '#/storage' }),
    item('warning', { key: 'h3', service: 3, name: 'Getter', title: 'Getter: automatic search is off', message: 'IndexerSearchCheck: No indexers available' }),
    item('warning', { key: 'q3', service: 3, name: 'Getter', title: 'Getter: a download is stuck', rule: 'downloads',
      message: 'Big.Buck.Bunny.2008.2160p.WEB: no files found' }),
  ], now);
  assert.deepEqual(rows.map((r) => [r.title, r.tone, r.value, r.more]), [
    ['Mounts: cache is full', 'fail', '', 1],
    ['Sintel is not reachable', 'fail', '11 min', 0],
    ['Disk / nearly full', 'warn', '92%', 0],
    ['Getter: automatic search is off', 'warn', '', 1],
  ]);
  // The line says the first thing in full, readable; the opened row lists every one, worst first, with the app's
  // own words in full.
  assert.equal(rows[0].detail, 'Cloud:archive: the cache disk is out of space');
  assert.equal(rows[3].detail, 'No indexers available');
  assert.deepEqual(rows[0].items.map((i) => [i.tone, i.title]), [['fail', 'Cache is full'], ['warn', '1 file not uploaded']]);
  assert.deepEqual(rows[3].items.map((i) => i.text), [
    'IndexerSearchCheck: No indexers available. The app reports this about itself. Open it to see what it suggests.',
    'Big.Buck.Bunny.2008.2160p.WEB: no files found. It will not finish on its own. Open the app to retry it or remove it.',
  ]);
  assert.equal(rows[1].items[0].text, 'Connection refused. Observarr gets no answer from it. It may have stopped or be restarting. Since ' +
    `${new Date('2026-11-27T11:48:10Z').toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}.`);
  assert.equal(rows[0].key, 'app/2');
  assert.deepEqual(notes.map((n) => n.text), ['Requests: 3 requests wait for approval']);
  assert.deepEqual(issues(null, now), { rows: [], notes: [] });
  // The title counts apps with a problem, then those with only warnings.
  assert.equal(situation(rows.length ? [
    item('critical', { key: 'c2', service: 2 }), item('warning', { key: 'w1', service: 2 }), item('warning', { key: 'h3', service: 3 }),
  ] : [], 10, true).title, '1 problem needs you');
});

test('what is muted stays listed, last, and counts for nothing', () => {
  const now = Date.parse('2026-11-27T12:00:00Z');
  const later = '2026-11-27T13:00:00Z';
  const list = [
    item('critical', { key: 'd4', rule: 'down', service: 4, name: 'Sintel', title: 'Sintel is not reachable', message: 'connection refused', mutedUntil: later }),
    item('critical', { key: 'c2', service: 2, name: 'Mounts', title: 'Mounts: cache is full', mutedUntil: later }),
    item('warning', { key: 'w2', service: 2, name: 'Mounts', title: 'Mounts: 1 file not uploaded' }),
    item('warning', { key: 'h3', service: 3, name: 'Getter', title: 'Getter: automatic search is off', mutedUntil: '2026-11-27T11:00:00Z' }),
  ];
  const { rows } = issues(list, now);
  assert.deepEqual(rows.map((r) => [r.title, r.tone, r.muted]), [
    // The muted error no longer leads its app's row: what still needs someone does.
    ['Mounts: 1 file not uploaded', 'warn', false],
    ['Getter: automatic search is off', 'warn', false], // its mute has ended
    ['Sintel is not reachable', 'idle', true],
  ]);
  assert.equal(rows[2].detail, mutedText(Date.parse(later)));
  assert.match(rows[2].detail, /^Muted until \d/);
  assert.deepEqual(rows[0].items.map((i) => [i.key, i.mutedUntil > 0]), [['w2', false], ['c2', true]]);
  // The title, the tab and the bell do not count what is muted; the title says that something is.
  assert.deepEqual(tally(list.slice(0, 3), now), { fails: 0, warns: 1, muted: 2 });
  assert.deepEqual(situation(list.slice(0, 3), 10, true, now), { title: '1 warning to look at · 2 muted', count: 1, tone: 'warn' });
  assert.deepEqual(situation(list.slice(0, 1), 10, true, now), { title: 'All 10 apps are fine · 1 muted', count: 0, tone: 'ok' });
  assert.equal(situation([...list, item('critical', { key: 'x', title: 'Disk / nearly full' })], 10, true, now).title, '1 problem needs you · 2 muted');
  // A mute that has ended counts again.
  assert.deepEqual(tally(list.slice(0, 1), Date.parse(later) + 1), { fails: 1, warns: 0, muted: 0 });
});

test('since is coarse', () => {
  const now = 1e12;
  assert.equal(since(new Date(now - 40e3).toISOString(), now), '40 s');
  assert.equal(since(new Date(now - 725e3).toISOString(), now), '12 min');
  assert.equal(since(new Date(now - 3 * 3600e3 - 330e3).toISOString(), now), '3 h 5 min');
  // The shared duration words, whatever the length.
  assert.equal(since(new Date(now - (12 * 86400 + 4 * 3600 + 70) * 1000).toISOString(), now), '12 days 4 h');
  assert.equal(since('0001-01-01T00:00:00Z', now), '');
  assert.equal(since(undefined, now), '');
});

test('actions lead to the app or to the view with the details', () => {
  const svc = { id: 4, name: 'Sintel', url: 'http://sintel.lan:8989' };
  const [app] = issues([item('warning', { service: 4 })], 0).rows;
  assert.deepEqual(actions(app, svc), [{ label: 'Open Sintel', href: 'http://sintel.lan:8989', external: true },
    { label: 'Show in Apps', jump: 4 }]);
  const [disk] = issues([item('warning', { view: '#/storage', service: 4 })], 0).rows;
  assert.deepEqual(actions(disk, svc).map((a) => a.label), ['View storage', 'Open Sintel']);
  assert.deepEqual(actions(disk, null), [{ label: 'View storage', href: '#/storage' }]);
  // Only a web address becomes a link.
  assert.deepEqual(actions(app, { ...svc, url: 'javascript:alert(1)' }), [{ label: 'Show in Apps', jump: 4 }]);
});

const queue = (items, extra = {}) => ({ kinds: ['health', 'queue'], up: true,
  readings: { health: { data: { warnings: [] } }, queue: { data: { total: items.length, items, ...extra } } } });
const it = (title, status, progress, extra = {}) => ({ title, status, progress, sizeBytes: 1e9, leftBytes: 1e9 * (1 - progress), ...extra });

test('downloads are listed once, what moves first', () => {
  const d = downloads([
    { id: 1, ...queue([it('Tears.of.Steel.2012.2160p.WEB', 'downloading', 0.59, { eta: '00:02:21' }), it('Sintel.2010.1080p.WEB', 'queued', 0),
      it('Cosmos.Laundromat.2015.1080p.WEB', 'completed', 1, { problem: 'No files found are eligible for import' })]) },
    { id: 2, ...queue([it('Tears.of.Steel.2012.2160p.WEB', 'downloading', 0.6)], { speed: 38 * 1024 * 1024 }) },
    { id: 3, kinds: ['health'], readings: {} },
  ]);
  assert.equal(d.any, true);
  assert.deepEqual(d.rows.map((r) => [r.title, r.note, r.tone]), [
    ['Tears of Steel (2012) · 2160p', '60%', ''],
    ['Cosmos Laundromat (2015) · 1080p', 'Stuck', 'warn'],
    ['Sintel (2010) · 1080p', 'Queued', ''],
  ]);
  assert.equal(d.rows[0].raw, 'Tears.of.Steel.2012.2160p.WEB');
  assert.equal(d.summary, '1 active · 1 queued · 319 Mbit/s');
  assert.equal(downloads([{ id: 3, kinds: ['health'], readings: {} }]).any, false);
});

test('a paused queue leads with Paused and is no warning', () => {
  const svc = { id: 7, name: 'Sintel', ...queue([it('Big.Buck.Bunny.2008.2160p.WEB', 'paused', 0.4), it('Sintel.2010.1080p.WEB', 'queued', 0)], { paused: true }) };
  const d = downloads([svc]);
  assert.equal(d.summary, 'Paused · 2 queued');
  assert.deepEqual(d.rows.map((r) => [r.note, r.tone, r.idle]), [['Paused', '', true], ['Paused', '', true]]);
  assert.deepEqual(cardSummary(svc, []).summary, { text: 'Paused · 2 queued', tone: '' });
  assert.deepEqual(serviceLine(svc, []), { text: 'Paused', tone: 'idle' });
});

test('the line under an app says one state in one word', () => {
  const up = { id: 1, up: true, kinds: ['health'], readings: { health: { data: { warnings: [] } } } };
  assert.deepEqual(serviceLine({ ...up, up: false }, []), { text: 'Not reachable', tone: 'fail' });
  assert.deepEqual(serviceLine(up, []), { text: 'Fine', tone: 'ok' });
  assert.deepEqual(serviceLine(up, null), { text: 'Checking…', tone: 'unknown' });
  assert.deepEqual(serviceLine(up, [item('warning', { service: 1 }), item('warning', { service: 1, title: 'b' })]),
    { text: '2 warnings', tone: 'warn' });
  assert.deepEqual(serviceLine(up, [item('critical', { service: 1, message: 'cache out of space' })]),
    { text: 'Cache out of space', tone: 'fail' });
  assert.deepEqual(serviceLine({ ...up, readings: { ...up.readings, requests: { data: { counts: { pending: 3 } } } } }, []),
    { text: '3 waiting', tone: 'idle' });
  assert.deepEqual(serviceLine({ ...up, readings: { ...up.readings, security: { data: { total: 15038, capped: false } } } }, []),
    { text: '15,038 blocked', tone: 'ok' });
});

test('streams are listed with who plays them', () => {
  const svc = { id: 1, kinds: ['streams'], readings: { streams: { data: { total: 3, paused: 1, transcodes: 1, bitrate: 23e6, streams: [
    { title: 'Big Buck Bunny (2008)', user: 'alex', client: 'web', method: 'direct', progress: 0.74, paused: false, reasons: [] },
    { title: 'Sintel (2010)', user: 'sam', client: 'tv', method: 'transcode', progress: 0.39, paused: false, reasons: ['video codec'] },
    { title: 'Caminandes S01E02', user: 'robin', client: 'phone', method: 'directstream', progress: 0.4, paused: true, reasons: [] },
  ] } } } };
  const p = playing([svc]);
  assert.equal(p.summary, '3 viewers · 23 Mbit/s');
  assert.deepEqual(p.rows.map((r) => [r.title, r.note, r.tone, r.idle]), [
    ['Big Buck Bunny (2008) · alex', 'Direct', '', false],
    ['Sintel (2010) · sam', 'Transcode', 'warn', false],
    ['Caminandes S01E02 · robin', 'Paused', '', true],
  ]);
  assert.equal(playing([]).total, 0);
});

test('bucket gives every range the same number of points', () => {
  const times = Array.from({ length: 1440 }, (_, i) => i * 60000);
  const day = bucket(times, [times.map((_, i) => i), times.map((_, i) => (i < 720 ? null : 1))], 120);
  assert.equal(day.times.length, 120);
  assert.equal(day.times[0], 0);
  assert.equal(day.times[119], 1439 * 60000);
  assert.equal(day.lists[0].length, 120);
  assert.ok(day.lists[0][60] > 700 && day.lists[0][60] < 740);
  assert.equal(day.lists[1][10], null);
  assert.equal(day.lists[1][110], 1);
  const week = bucket(Array.from({ length: 168 }, (_, i) => i * 3600000), [new Array(168).fill(2)], 120);
  assert.equal(week.times.length, 120);
  assert.ok(week.lists[0].every((v) => v === 2));
  // Too few samples to spread over the points are left as they are.
  const few = [0, 1000, 2000];
  assert.equal(bucket(few, [[1, 2, 3]], 120).times, few);
});

test('the chart draws traffic by what it is for', () => {
  const view = { host: { series: { rx: [100e6, null, 100e6], tx: [10e6, null, 10e6] } }, containers: [
    { name: 'a', service: 'Getter', series: { rx: [50e6, null, 50e6], tx: [0, null, 0] } },
    { name: 'b', service: 'Player', series: { rx: [0, null, 0], tx: [5e6, null, 5e6] } },
    { name: 'c', service: '', series: { rx: [10e6, null, 10e6], tx: [0, null, 0] } },
  ] };
  const kinds = new Map([['Getter', ['health', 'queue']], ['Player', ['health', 'streams']]]);
  const series = byPurpose(view, kinds);
  assert.deepEqual(series.map((s) => s.label), ['Downloads', 'Streams', 'Other']);
  assert.deepEqual(series[0].values, [400, null, 400]);
  assert.deepEqual(series[1].values, [40, null, 40]);
  assert.deepEqual(series[2].values, [440, null, 440]);
  // Without an app that has a container of its own, it is the host's traffic in and out.
  assert.deepEqual(byPurpose({ ...view, containers: [] }, kinds).map((s) => [s.label, s.values[0]]), [['In', 800], ['Out', 80]]);
});
