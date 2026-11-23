// Writes a gzip (.gz) and a Brotli (.br) copy next to every compressible file in dist/assets, so the server can
// send them as they are instead of compressing on each request. A copy that is not smaller is not kept.
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { brotliCompressSync, constants, gzipSync } from 'node:zlib';

const dir = 'dist/assets';
const compressible = /\.(js|css|svg|json)$/;

for (const name of readdirSync(dir)) {
  if (!compressible.test(name)) continue;
  const file = join(dir, name);
  const data = readFileSync(file);
  const copies = {
    gz: gzipSync(data, { level: 9 }),
    br: brotliCompressSync(data, {
      params: {
        [constants.BROTLI_PARAM_QUALITY]: constants.BROTLI_MAX_QUALITY,
        [constants.BROTLI_PARAM_SIZE_HINT]: data.length,
      },
    }),
  };
  for (const [ext, out] of Object.entries(copies)) {
    if (out.length < data.length) writeFileSync(`${file}.${ext}`, out);
  }
}
