import { test } from 'node:test';
import assert from 'node:assert/strict';
import { rulesProblem } from './alerts.js';

const rules = (extra = {}) => ({
  enabled: { down: true, health: true, storage: true, traffic: true, security: true, downloads: true, requests: false },
  downMinutes: 3, fullPercent: 90, failedSignIns: 10, repeatHours: 0, perHour: 30,
  quiet: { on: false, from: '22:00', to: '07:00' }, link: '', ...extra,
});

test('rules that can be saved have no problem', () => {
  assert.equal(rulesProblem(rules()), '');
  assert.equal(rulesProblem(rules({ link: 'https://observarr.example', quiet: { on: true, from: '22:00', to: '07:00' } })), '');
  // The threshold of a rule that is off is not looked at, nor the times of quiet hours that are off.
  assert.equal(rulesProblem(rules({ enabled: { down: false }, downMinutes: null })), '');
  assert.equal(rulesProblem(rules({ quiet: { on: false, from: '', to: '' } })), '');
});

test('a problem is said in the form\'s own words, never in a field name', () => {
  // An emptied number field holds null.
  assert.equal(rulesProblem(rules({ perHour: null })), 'Messages per channel and hour: a whole number from 1 to 600.');
  assert.equal(rulesProblem(rules({ perHour: 0 })), 'Messages per channel and hour: a whole number from 1 to 600.');
  assert.equal(rulesProblem(rules({ downMinutes: '' })), 'App not reachable, container down: a whole number from 0 to 1440 min.');
  assert.equal(rulesProblem(rules({ fullPercent: 120 })), 'Disk or cache nearly full, mount problems: a whole number from 50 to 100%.');
  assert.equal(rulesProblem(rules({ quiet: { on: true, from: '22:00', to: '22:00' } })), 'Quiet hours: the two times must differ.');
  assert.equal(rulesProblem(rules({ quiet: { on: true, from: '', to: '07:00' } })), 'Quiet hours: set both times.');
  assert.equal(rulesProblem(rules({ link: 'observarr.example' })), "Observarr's address: an http:// or https:// address.");
  for (const bad of [{ perHour: null }, { downMinutes: 1.5 }, { quiet: { on: true, from: 'x', to: 'y' } }]) {
    assert.doesNotMatch(rulesProblem(rules(bad)), /perHour|downMinutes|quiet\.|HH:MM/);
  }
});
