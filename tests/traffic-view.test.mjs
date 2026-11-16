import test from 'node:test';
import assert from 'node:assert/strict';
import { analyze, dateInZone, shiftDate } from '../web/traffic.mjs';

const today = '2026-11-16';
const now = new Date('2026-11-16T04:00:00Z');
const row = (date, value, extras = {}) => ({ date, used_bytes: value * 1024 ** 3, samples: 10, partial: false, estimated: false, ...extras });
const history = (value = 10) => Array.from({ length: 7 }, (_, i) => row(shiftDate(today, i - 7), value));
const server = (days, extras = {}) => ({ days, reset: '2026-11-22T00:00:00+08:00', limit_gib: 1000, remaining_gib: 800, ...extras });

test('today is cumulative; a full daily baseline is not prorated by time of day', () => {
  const result = analyze(server([...history(), row(today, 15)]), today, now);
  assert.equal(result.today.value, 15);
  assert.equal(result.today.spike, false);
  assert.equal(result.yesterday.value, 10);
  assert.equal(result.baseline.median, 10);
});

test('highlight a real spike against preceding seven complete days', () => {
  const result = analyze(server([...history(), row(today, 35)]), today, now);
  assert.equal(result.today.spike, true);
  assert.equal(result.today.multiple, 3.5);
  assert.equal(result.status.label, '今日用量突增');
  assert.equal(result.recentSpikes.length, 1);
});

test('missing, partial and current-day samples cannot invent a normal baseline', () => {
  const result = analyze(server([row('2026-11-13', 100, { partial: true }), row('2026-11-14', 0, { samples: 0 }), row('2026-11-15', 5), row(today, 500)]), today, now);
  assert.equal(result.baseline.count, 1);
  assert.equal(result.baseline.median, null);
  assert.equal(result.today.spike, false);
  assert.equal(result.status.label, '积累基准中');
  assert.equal(result.days.find((d) => d.date === '2026-11-14').value, null);
});

test('a partial day is displayed but not called an anomaly', () => {
  const result = analyze(server([...history(), row(today, 100, { partial: true })]), today, now);
  assert.equal(result.today.value, 100);
  assert.equal(result.today.spike, false);
});

test('legitimate cross-midnight estimates remain eligible baseline records', () => {
  const days = history().map((d) => ({ ...d, estimated: true }));
  assert.equal(analyze(server([...days, row(today, 50)]), today, now).today.spike, true);
});

test('ignore tiny spikes, but a zero baseline can flag at least one GiB', () => {
  assert.equal(analyze(server([...history(0), row(today, 0.5)]), today, now).today.spike, false);
  assert.equal(analyze(server([...history(0), row(today, 1)]), today, now).today.spike, true);
});

test('budget risk uses complete prior days and reset date; unknown quotas stay unknown', () => {
  const risk = analyze(server(history(), { remaining_gib: 20 }), today, now);
  assert.equal(risk.projected, true);
  assert.equal(risk.status.label, '可能提前用尽');
  assert.equal(risk.resetDays, 6);
  assert.equal(analyze(server(history(), { remaining_gib: null, limit_gib: null }), today, now).projected, false);
  assert.equal(analyze(server(history(), { remaining_gib: 0 }), today, now).status.label, '配额已用尽');
});

test('historical spikes use only their own earlier days, never later observations', () => {
  const days = [...history(), row(today, 40)];
  const result = analyze(server(days), today, now);
  assert.equal(result.days[0].spike, false);
  assert.equal(result.days.at(-1).spike, true);
});

test('calendar follows server timezone, including year boundaries', () => {
  assert.equal(dateInZone(new Date('2026-11-15T17:00:00Z'), 'Asia/Shanghai'), today);
  assert.equal(shiftDate('2027-01-01', -1), '2026-12-31');
});
