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
