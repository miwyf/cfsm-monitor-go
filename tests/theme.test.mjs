import test from 'node:test';
import assert from 'node:assert/strict';
import { quotaPositionColor, quotaUsageColor, quotaSegments } from '../web/theme.mjs';

test('matches LuminaPlus usage text at 50, 80 and 90 percent', () => {
  assert.equal(quotaUsageColor(.5), 'hsl(144.0 62.0% 48.0%)');
  assert.equal(quotaUsageColor(.8), 'hsl(36.9 82.4% 51.5%)');
  assert.equal(quotaUsageColor(.9), 'hsl(21.5 84.2% 48.7%)');
});
test('matches the full green-to-red OKLCH quota spectrum', () => {
  assert.equal(quotaPositionColor(0), 'oklch(0.7200 0.1600 150.00)');
  assert.equal(quotaPositionColor(.44), 'oklch(0.8600 0.1800 110.00)');
  assert.equal(quotaPositionColor(1), 'oklch(0.6000 0.2200 27.00)');
  assert.equal(quotaPositionColor(-1), quotaPositionColor(0));
  assert.equal(quotaPositionColor(2), quotaPositionColor(1));
});
test('quota fill depends on usage, with 18 segments and neutral unknown data', () => {
  const lit = (f) => quotaSegments(f).filter((s) => s.startsWith('oklch')).length;
  assert.equal(lit(.5), 9); assert.equal(lit(.8), 14); assert.equal(lit(.9), 16); assert.equal(lit(1), 18);
  assert.equal(quotaSegments(null).every((s) => s === 'var(--progress-bg)'), true);
  assert.equal(quotaSegments(0).every((s) => s === 'var(--progress-bg)'), true);
  assert.match(quotaSegments(.0001)[0], /10%/);
  assert.deepEqual(quotaSegments(2), quotaSegments(1));
});
