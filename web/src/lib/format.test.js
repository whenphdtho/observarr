import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  formatMbit, formatRate, formatDisk, formatSize, formatPart, formatPercent, formatPct, formatChange, formatCount, formatSpan,
  formatUptime, clockSeconds, formatLeft, formatAge, formatUntil, plural, prettyRelease,
} from './format.js';

test('rates are always Mbit/s', () => {
  assert.equal(formatRate(38 * 1024 * 1024), '319 Mbit/s');
  assert.equal(formatRate(687500), '5.5 Mbit/s');
  assert.equal(formatRate(0), '0 Mbit/s');
  assert.equal(formatRate(100), '<0.1 Mbit/s');
  assert.equal(formatRate(-1), '—');
  assert.equal(formatRate(undefined), '—');
  assert.equal(formatMbit(1.25e6), '10');
  // The step from one decimal to none is at 9.95 Mbit/s, so "10.0" never shows.
  assert.equal(formatMbit(9.94e6 / 8), '9.9');
  assert.equal(formatMbit(9.95e6 / 8), '10');
});

test('disk reads and writes are the one exception: MB/s', () => {
  assert.equal(formatDisk(4.8e6), '4.8');
  assert.equal(formatDisk(120e6), '120');
  assert.equal(formatDisk(0), '0');
  assert.equal(formatDisk(1000), '<0.1');
  assert.equal(formatDisk(-1), '—');
  // The same bytes a second over a network read eight times the number, in Mbit/s.
  assert.equal(formatMbit(4.8e6), '38');
});

test('sizes are GB and TB', () => {
  assert.equal(formatSize(10e9), '10 GB');
  assert.equal(formatSize(1.5e9), '1.5 GB');
  assert.equal(formatSize(80 * 1024 * 1024), '0.08 GB');
  assert.equal(formatSize(1000), '<0.01 GB');
  assert.equal(formatSize(0), '0 GB');
  assert.equal(formatSize(8e12), '8.0 TB');
  // GB turn into TB where they would read "1000 GB".
  assert.equal(formatSize(999.4e9), '999 GB');
  assert.equal(formatSize(999.5e9), '1.0 TB');
  assert.equal(formatSize(NaN), '—');
  assert.equal(formatPart(201e9, 250e9), '201 of 250 GB');
  assert.equal(formatPart(464e9, 1400e9), '464 of 1,400 GB');
  assert.equal(formatPart(6.4e9, 17e9), '6.4 of 17 GB');
  assert.equal(formatPart(900e9, 20e12), '0.90 of 20 TB');
  assert.equal(formatPart(48.6e12, 60e12), '49 of 60 TB');
  assert.equal(formatPart(NaN, 1), '—');
});

test('percent has no space', () => {
  assert.equal(formatPercent(0.594), '59%');
  assert.equal(formatPercent(7), '100%');
  assert.equal(formatPct(24.4), '24%');
  assert.equal(formatPct(-3), '0%');
});

test('a change is a signed percentage', () => {
  assert.equal(formatChange(0.12), '+12%');
  assert.equal(formatChange(-0.04), '−4%');
  assert.equal(formatChange(-0.31), '−31%');
  assert.equal(formatChange(0.001), '0%');
  assert.equal(formatChange(NaN), '—');
  assert.equal(formatChange(undefined), '—');
  assert.equal(formatChange(null), '—');
  assert.equal(formatChange(42), '+>999%');
});

test('counts carry thousands separators', () => {
  assert.equal(formatCount(15038), '15,038');
  assert.equal(formatCount(5000, true), '5,000+');
});

test('a span of time reads in its two largest units', () => {
  assert.equal(formatSpan(45), '45 s');
  assert.equal(formatSpan(141), '2 min 21 s');
  assert.equal(formatSpan(120), '2 min');
  assert.equal(formatSpan(3900), '1 h 5 min');
  assert.equal(formatSpan(7200), '2 h');
  assert.equal(formatSpan(86400), '1 day');
  assert.equal(formatSpan(12 * 86400 + 4 * 3600 + 59), '12 days 4 h');
  assert.equal(formatSpan(-1), '—');
  // Half a second rounds up into the next unit, which then stands alone.
  assert.equal(formatSpan(59.4), '59 s');
  assert.equal(formatSpan(59.5), '1 min');
  assert.equal(formatSpan(3599.4), '59 min 59 s');
  assert.equal(formatSpan(3599.5), '1 h');
  assert.equal(formatUptime((12 * 86400 + 4 * 3600) * 1e9), '12 days 4 h');
  assert.equal(formatUptime(null), '—');
  assert.equal(formatUptime(undefined), '—');
});

test('time left is read from an app\'s clock', () => {
  assert.equal(clockSeconds('00:02:21'), 141);
  assert.equal(clockSeconds('0:02:21'), 141);
  assert.equal(clockSeconds('2:21'), 141);
  assert.equal(clockSeconds('1.02:03:04'), 93784);
  assert.equal(clockSeconds('soon'), null);
  assert.equal(formatLeft('00:02:21'), '2 min 21 s left');
  assert.equal(formatLeft('00:00:00'), '');
  assert.equal(formatLeft(''), '');
  assert.equal(formatLeft('about an hour'), 'about an hour');
});

test('ages read in one unit', () => {
  const now = Date.parse('2026-11-27T12:00:00Z');
  const ago = (s) => formatAge(new Date(now - s * 1000).toISOString(), now);
  assert.equal(ago(0), 'just now');
  assert.equal(ago(12), '12 s ago');
  assert.equal(ago(240), '4 min ago');
  assert.equal(ago(3 * 3600), '3 h ago');
  assert.equal(ago(86400), '1 day ago');
  assert.equal(ago(3 * 86400), '3 days ago');
  // Whole units that have passed, never rounded up.
  assert.equal(ago(119), '1 min ago');
  assert.equal(ago(3599), '59 min ago');
  assert.equal(ago(36 * 3600), '1 day ago');
  assert.equal(ago(47 * 3600 + 3599), '1 day ago');
  assert.equal(ago(48 * 3600), '2 days ago');
  assert.equal(formatAge('', now), '');
  assert.equal(formatUntil(new Date(now + 141000).toISOString(), now), 'in 2 min 21 s');
  assert.equal(formatUntil(new Date(now - 1000).toISOString(), now), 'ended');
});

test('plural counts', () => {
  assert.equal(plural(1, 'warning'), '1 warning');
  assert.equal(plural(3, 'warning'), '3 warnings');
  assert.equal(plural(2, 'address', 'addresses'), '2 addresses');
});

test('release names are prettified for reading', () => {
  assert.equal(prettyRelease('Big.Buck.Bunny.2008.2160p.WEB'), 'Big Buck Bunny (2008) · 2160p');
  assert.equal(prettyRelease('Tears.of.Steel.2012.1080p.WEB'), 'Tears of Steel (2012) · 1080p');
  assert.equal(prettyRelease('Caminandes.S01E03.Llamigos.1080p.WEB'), 'Caminandes S01E03 · 1080p');
  assert.equal(prettyRelease('Pepper.and.Carrot.s01e05.1080p.WEB'), 'Pepper and Carrot S01E05 · 1080p');
  assert.equal(prettyRelease('Sintel_2010_720p'), 'Sintel (2010) · 720p');
  // A year that belongs to the title is kept in it.
  assert.equal(prettyRelease('Cosmos.2049.2017.1080p.WEB'), 'Cosmos 2049 (2017) · 1080p');
  // A source alone marks a release too.
  assert.equal(prettyRelease('Caminandes.Gran.Dillama.2013.WEB'), 'Caminandes Gran Dillama (2013)');
  assert.equal(prettyRelease('Sintel.2010.BluRay.x264'), 'Sintel (2010)');
});

test('what is not a release name is left alone', () => {
  assert.equal(prettyRelease('Big Buck Bunny (2008)'), 'Big Buck Bunny (2008)');
  // A title with a year in it is a title.
  assert.equal(prettyRelease('Blade Runner 2049'), 'Blade Runner 2049');
  assert.equal(prettyRelease('Cyberpunk 2077'), 'Cyberpunk 2077');
  assert.equal(prettyRelease('Blade.Runner.2049'), 'Blade.Runner.2049');
  // A date and a file name have dots and a year, and are neither.
  assert.equal(prettyRelease('01.05.2025'), '01.05.2025');
  assert.equal(prettyRelease('backup.2024.tar'), 'backup.2024.tar');
  assert.equal(prettyRelease('Caminandes.Gran.Dillama.2013'), 'Caminandes.Gran.Dillama.2013');
  assert.equal(prettyRelease('Tears of Steel 2012 1080p WEB'), 'Tears of Steel 2012 1080p WEB');
  assert.equal(prettyRelease('Chris.Zabriskie-Divider-2011-FLAC'), 'Chris.Zabriskie-Divider-2011-FLAC');
  assert.equal(prettyRelease('2160p'), '2160p');
  assert.equal(prettyRelease(''), '');
  assert.equal(prettyRelease(undefined), '');
});

