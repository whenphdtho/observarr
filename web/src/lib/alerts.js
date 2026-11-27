import { derived } from 'svelte/store';
import { attention } from './live.js';

// What the alert views share: the channel types and their fields, the rules, and the alerts firing now (which the
// shell's bell and the System view's Alerts section both show).

// TYPES describes each channel type's form: its settings and secrets, in the order the form shows them. A field
// is { key, label, secret?, optional?, type?, placeholder?, hint?, check? }; check(value) returns what is wrong,
// or ''.
const url = (v) => (/^https?:\/\/[^\s/?#@]+/i.test(v) ? '' : 'An http:// or https:// address.');
const required = (v) => (v.trim() ? '' : 'Required.');
const atLeast = (n) => (v) => (!v || [...v].length >= n ? '' : `At least ${n} characters.`);
export const TYPES = {
  ntfy: {
    label: 'ntfy', fields: [
      { key: 'server', label: 'Server', placeholder: 'https://ntfy.sh', optional: true, check: (v) => (v ? url(v) : '') },
      { key: 'topic', label: 'Topic', secret: true, hint: 'Anyone who knows it can read it: kept like a password.',
        check: (v) => (/^[-_A-Za-z0-9]{1,64}$/.test(v) ? '' : 'Letters, digits, - or _.') },
      { key: 'token', label: 'Access token', secret: true, optional: true, check: atLeast(8) },
    ],
  },
  discord: {
    label: 'Discord', fields: [
      { key: 'url', label: 'Webhook address', secret: true, placeholder: 'https://discord.com/api/webhooks/…',
        check: (v) => (/^https:\/\/[^\s/]+\/api\/webhooks\//.test(v) ? '' : 'The https://…/api/webhooks/… address.') },
    ],
  },
  webhook: {
    label: 'Webhook', fields: [
      { key: 'url', label: 'Address', secret: true, placeholder: 'https://…', check: url },
      { key: 'secret', label: 'Signing secret', secret: true, optional: true, check: atLeast(16),
        hint: 'Signs each request: X-Observarr-Signature: sha256=…' },
    ],
  },
  email: {
    label: 'Email', fields: [
      { key: 'host', label: 'Mail server', placeholder: 'smtp.example.com', check: required },
      { key: 'security', label: 'Encryption', choices: [['starttls', 'STARTTLS'], ['tls', 'TLS']] },
      { key: 'port', label: 'Port', type: 'number', optional: true, placeholder: '587',
        check: (v) => (v === '' || (Number(v) >= 1 && Number(v) <= 65535) ? '' : '1 to 65535.') },
      { key: 'username', label: 'User name', optional: true },
      { key: 'password', label: 'Password', secret: true, optional: true, check: atLeast(8) },
      { key: 'from', label: 'From', placeholder: 'Observarr <observarr@example.com>',
        check: (v) => (v.includes('@') ? '' : 'An address.') },
      { key: 'to', label: 'To', placeholder: 'you@example.com', hint: 'Up to 10, separated by commas.',
        check: (v) => (v.includes('@') ? '' : 'At least one address.') },
      { key: 'helo', label: 'Greeting name', optional: true, placeholder: 'the From domain',
        hint: 'The host name Observarr gives the mail server.' },
    ],
  },
  gotify: {
    label: 'Gotify', fields: [
      { key: 'server', label: 'Server', placeholder: 'http://gotify.lan', check: url },
      { key: 'token', label: 'App token', secret: true, check: atLeast(8) },
    ],
  },
  telegram: {
    label: 'Telegram', fields: [
      { key: 'token', label: 'Bot token', secret: true, placeholder: '123456:ABC…',
        check: (v) => (/^\d{3,20}:[A-Za-z0-9_-]{20,100}$/.test(v) ? '' : 'The token the bot father gave, 123456:ABC….') },
      { key: 'chatId', label: 'Chat', placeholder: '-1001234567890 or @channel',
        check: (v) => (/^(-?\d{1,20}|@[A-Za-z0-9_]{5,32})$/.test(v) ? '' : 'A chat number or @channel.') },
    ],
  },
};

// RULES names each rule, with the threshold it has, if any: { key, label, unit, min, max } of the rules document.
export const RULES = [
  { id: 'down', label: 'App not reachable, container down', threshold: { key: 'downMinutes', label: 'for', unit: 'min', min: 0, max: 1440 } },
  { id: 'health', label: 'Health warnings and errors' },
  { id: 'storage', label: 'Disk or cache nearly full, mount problems', threshold: { key: 'fullPercent', label: 'at', unit: '%', min: 50, max: 100 } },
  { id: 'traffic', label: 'Traffic budget at 80% and 100%' },
  { id: 'security', label: 'Sign-in lockouts and failed sign-ins', threshold: { key: 'failedSignIns', label: 'from', unit: 'in 15 min', min: 3, max: 1000 } },
  { id: 'downloads', label: 'Downloads stuck or failed' },
  { id: 'requests', label: 'Requests waiting for approval' },
];

export const RULE_NAMES = Object.fromEntries(RULES.map((r) => [r.id, r.label]));

// severityTone maps an alert's severity to the shared status classes.
export function severityTone(severity) {
  return severity === 'critical' ? 'fail' : severity === 'warning' ? 'warn' : 'ok';
}

// firing holds the alerts firing now, worst first, from the attention list the live stream carries; null until
// it arrived. A muted one sends nothing, so it is not among them.
export const firing = derived(attention, (list) => list?.filter((it) => it.firing && !it.mutedUntil) ?? null);

// scrollToAlerts brings the System view's Alerts section into view.
export function scrollToAlerts() {
  const el = document.getElementById('alerts');
  if (!el) return;
  const smooth = !matchMedia('(prefers-reduced-motion: reduce)').matches;
  el.scrollIntoView({ block: 'start', behavior: smooth ? 'smooth' : 'auto' });
}
