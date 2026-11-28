// Helpers for the Security view and the "security" shape widget. They read only the generic shape
// (shape.Security: counts and decisions), never anything app-specific.

// securityServices are the services whose adapter fills the "security" shape.
export function securityServices(services) {
  return services.filter((s) => s.kinds?.includes('security'));
}

// blocked sums the security readings of services: every active decision (a ban, a captcha, …), and how many of each
// type, largest first. capped says a count stopped early; any that at least one service has reported.
export function blocked(services) {
  let total = 0;
  let capped = false;
  let any = false;
  const types = new Map();
  for (const s of services) {
    const data = s.readings?.security?.data;
    if (!data) continue;
    any = true;
    total += data.total;
    capped ||= data.capped;
    for (const t of data.byType ?? []) types.set(t.name, (types.get(t.name) ?? 0) + t.count);
  }
  const byType = [...types].map(([name, count]) => ({ name, count })).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  return { total, byType, capped, any };
}
