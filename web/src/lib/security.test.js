import { test } from 'node:test';
import assert from 'node:assert/strict';
import { blocked } from './security.js';

const reading = (total, byType, capped = false) => ({ readings: { security: { data: { total, byType, capped } } } });

test('blocked counts every decision, whatever its type, the way each section does', () => {
  const b = blocked([
    reading(15038, [{ name: 'ban', count: 15032 }, { name: 'captcha', count: 6 }]),
    reading(4, [{ name: 'ban', count: 4 }], true),
    { readings: {} },
  ]);
  assert.equal(b.total, 15042);
  assert.deepEqual(b.byType, [{ name: 'ban', count: 15036 }, { name: 'captcha', count: 6 }]);
  assert.equal(b.capped, true);
  assert.equal(b.any, true);
});

test('blocked has nothing before the first reading', () => {
  assert.deepEqual(blocked([{ readings: {} }]), { total: 0, byType: [], capped: false, any: false });
});
