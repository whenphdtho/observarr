import { formatRate } from './format.js';

// Summaries of what media servers play, shared by the streams widget, the Overview, the cards and the Network view.
// They read only the generic "streams" shape, never anything app-specific.

// METHODS names each play method, as the shape gives it, the way the method chip shows it.
export const METHODS = { direct: 'Direct play', directstream: 'Direct stream', transcode: 'Transcode' };

// streamServices are the services that declare the "streams" shape.
export function streamServices(services) {
  return services.filter((s) => s.kinds?.includes('streams'));
}

// streamsOf is a service's latest usable streams reading, or null.
export function streamsOf(svc) {
  const r = svc.readings?.streams;
  return r?.data && !r.error ? r.data : null;
}

// streamSummary adds up the latest streams readings of services: streams in all, paused and transcoding, and their
// bitrate in bits/s. readings counts the services with a usable reading.
export function streamSummary(services) {
  const out = { readings: 0, total: 0, paused: 0, transcodes: 0, bitrate: 0 };
  for (const svc of services) {
    const data = streamsOf(svc);
    if (!data) continue;
    out.readings += 1;
    out.total += data.total;
    out.paused += data.paused;
    out.transcodes += data.transcodes;
    out.bitrate += data.bitrate;
  }
  return out;
}

// streamText says what a summary holds, e.g. "2 playing · 1 paused · 1 transcoding", or "" when nothing plays.
export function streamText(sum) {
  const playing = sum.total - sum.paused;
  return [
    playing ? `${playing} playing` : '',
    sum.paused ? `${sum.paused} paused` : '',
    sum.transcodes ? `${sum.transcodes} transcoding` : '',
  ].filter(Boolean).join(' · ');
}

// formatBitrate renders bits/s as e.g. "10 Mbit/s"; 0 (unknown) renders as "".
export function formatBitrate(bps) {
  return bps > 0 ? formatRate(bps / 8) : '';
}

// codecText says what a stream's codecs are and what the server converts them to, e.g. "hevc → h264 · aac".
export function codecText(s) {
  const one = (from, to) => (to && to !== from ? `${from || '?'} → ${to}` : from);
  return [one(s.videoCodec, s.toVideoCodec), one(s.audioCodec, s.toAudioCodec)].filter(Boolean).join(' · ');
}

// streamKey tells a stream from the others of its reading, which carries no ids: who plays what where.
export function streamKey(s) {
  return `${s.user}|${s.client}|${s.device ?? ''}|${s.title}`;
}
