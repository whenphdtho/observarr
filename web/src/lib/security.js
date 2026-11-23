// Helpers for the Security view and the "security" shape widget. They read only the generic shape
// (shape.Security: counts and decisions), never anything app-specific.

// securityServices are the services whose adapter fills the "security" shape.
export function securityServices(services) {
  return services.filter((s) => s.kinds?.includes('security'));
}

// countOf is the count of name in a shape.SecurityCount list, 0 if it is not there.
export function countOf(list, name) {
  return (list ?? []).find((c) => c.name === name)?.count ?? 0;
}

// activeBans sums the "ban" decisions of every security reading; capped says a count stopped early.
export function activeBans(services) {
  let bans = 0;
  let capped = false;
  let any = false;
  for (const s of services) {
    const data = s.readings?.security?.data;
    if (!data) continue;
    any = true;
    bans += countOf(data.byType, 'ban');
    capped ||= data.capped;
  }
  return { bans, capped, any };
}

// formatCount renders a whole number with thousands separators, and "+" when it is a lower bound.
export function formatCount(n, capped = false) {
  if (!Number.isFinite(n)) return '—';
  return `${n.toLocaleString('en-US')}${capped ? '+' : ''}`;
}

// formatLeft renders a span of seconds as "45 s", "12 min", "5 h", "3 d".
export function formatLeft(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '0 s';
  if (seconds < 60) return `${Math.ceil(seconds)} s`;
  const min = Math.ceil(seconds / 60);
  if (min < 60) return `${min} min`;
  const h = Math.round(min / 60);
  if (h < 48) return `${h} h`;
  return `${Math.round(h / 24)} d`;
}

// formatUntil renders the time left until at (an ISO time) as "in 12 min", or "" if at is unset; past
// times read "ended".
export function formatUntil(at, nowMs) {
  if (!at) return '';
  const t = new Date(at).getTime();
  if (!Number.isFinite(t)) return '';
  const left = (t - nowMs) / 1000;
  return left > 0 ? `in ${formatLeft(left)}` : 'ended';
}

// formatTime renders an ISO time as a short local date and time, for a title.
export function formatTime(at) {
  if (!at) return '';
  const d = new Date(at);
  return Number.isFinite(d.getTime()) ? d.toLocaleString() : '';
}
