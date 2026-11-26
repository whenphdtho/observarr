import { test } from 'node:test';
import assert from 'node:assert/strict';
import { keyed } from './motion.js';

test('keyed gives every item its own key, even where a title looks like a repeat', () => {
  const keys = keyed(['A', 'A', 'A#1', 'B', 'A'], (t) => t).map((k) => k.key);
  assert.equal(new Set(keys).size, keys.length, `keys ${keys}`);
  assert.deepEqual(keys.slice(0, 2), ['A', 'A#1']);
  assert.deepEqual(keyed(['A', 'A', 'A#1'], (t) => t).map((k) => k.item), ['A', 'A', 'A#1']);
});
