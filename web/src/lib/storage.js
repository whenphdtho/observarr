// Summaries of disks and mounts shared by the Storage view, the System view and the mounts widget. They read
// only the host snapshot and the generic "mounts" shape, never anything app-specific.

// percentOf is part of whole as a whole percentage, 0 when whole is 0.
export function percentOf(part, whole) {
  return whole > 0 ? Math.round(Math.min(1, Math.max(0, part / whole)) * 100) : 0;
}

// diskParts splits a disk the way df does: used is total minus free, and what is left for new writes is "avail"
// (a file system can reserve part of its free space). Used + reserved + available is the whole disk; pct is how
// full it is for new writes.
export function diskParts(d) {
  const used = d.total - d.free;
  const reserved = Math.max(0, d.free - d.avail);
  return { used, reserved, avail: d.avail, pct: percentOf(used, used + d.avail) };
}

// diskTotals adds up the disks. Two paths on one file system report the same figures; they are counted once.
export function diskTotals(disks) {
  const seen = new Set();
  let used = 0;
  let avail = 0;
  let total = 0;
  for (const d of disks) {
    const key = `${d.total}/${d.free}/${d.avail}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const p = diskParts(d);
    used += p.used;
    avail += p.avail;
    total += d.total;
  }
  return { used, avail, total, count: seen.size, pct: percentOf(used, used + avail) };
}

// mountProblem is what is wrong with a mount, worst first, or '' when nothing is: { text, tone }.
export function mountProblem(m) {
  if (m.state === 'unmounted') return { text: 'not mounted', tone: 'fail' };
  if (m.outOfSpace) return { text: 'cache out of space', tone: 'fail' };
  if (m.erroredFiles > 0) return { text: `${m.erroredFiles} errored`, tone: 'fail' };
  return null;
}

// mountServices are the services that declare the "mounts" shape.
export function mountServices(services) {
  return services.filter((s) => s.kinds?.includes('mounts'));
}

// mountSummary adds up the latest mounts readings of services: mounts healthy, listed and in all (a reading lists
// only the first ones, so health is known for those only), cache used and its limit (limit 0 when any cache has
// none), transfer speed and transfers in progress. readings counts the services with a usable reading.
export function mountSummary(services) {
  const out = { readings: 0, total: 0, listed: 0, healthy: 0, caches: 0, cacheUsed: 0, cacheMax: 0, unlimited: false,
    speed: 0, active: 0 };
  for (const svc of services) {
    const data = svc.readings?.mounts?.data;
    if (!data || svc.readings.mounts.error) continue;
    out.readings += 1;
    out.total += data.total;
    out.listed += data.mounts.length;
    out.speed += data.speed;
    out.active += data.activeTransfers;
    for (const m of data.mounts) {
      if (!mountProblem(m)) out.healthy += 1;
      if (!m.cache) continue;
      out.caches += 1;
      out.cacheUsed += m.cacheUsedBytes;
      if (m.cacheMaxBytes > 0) out.cacheMax += m.cacheMaxBytes;
      else out.unlimited = true;
    }
  }
  if (out.unlimited) out.cacheMax = 0;
  return out;
}

// mountAttention lists the mount problems of services for the Overview's "Needs attention", in the form
// attention() uses. A problem the service already reports as a health warning about that mount (its source) is
// left out, so it is not listed twice.
export function mountAttention(services) {
  const out = [];
  for (const svc of mountServices(services)) {
    const data = svc.readings?.mounts?.data;
    if (!data || svc.readings.mounts.error) continue;
    const warned = new Set((svc.readings?.health?.data?.warnings ?? []).map((w) => w.source).filter(Boolean));
    for (const [i, m] of data.mounts.entries()) {
      const p = mountProblem(m);
      if (!p || warned.has(m.remote)) continue;
      out.push({ key: `mount-${svc.id}-${i}`, tone: p.tone, title: `${svc.name}: ${m.remote}`, detail: p.text, href: '#/storage' });
    }
  }
  return out;
}
