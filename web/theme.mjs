/*
MIT License

Copyright (c) 2026 Shanyang242
Copyright (c) 2026 volcano-1025

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

*/
// Color scales adapted from CFSM-Theme-LuminaPlus v1.2.24, MIT.
// https://github.com/volcano-1025/CFSM-Theme-LuminaPlus
export const nodeColors = ['#3b82f6', '#10b981', '#8b5cf6', '#e97b35', '#ec4899', '#f59e0b', '#2f9e65', '#2f5be0'];
const stops = [
  [0, .72, .16, 150], [.1, .72, .16, 150], [.28, .8, .18, 128],
  [.44, .86, .18, 110], [.58, .8, .18, 85], [.72, .72, .19, 62],
  [.86, .65, .21, 40], [1, .6, .22, 27],
];
const clamp = (v) => Math.min(1, Math.max(0, Number.isFinite(v) ? v : 0));
export function quotaPositionColor(position) {
  const p = clamp(position);
  const end = stops.findIndex((s, i) => i > 0 && p <= s[0]);
  const a = stops[end - 1], b = stops[end];
  const weight = (p - a[0]) / (b[0] - a[0]);
  const v = a.slice(1).map((value, i) => value + (b[i + 1] - value) * weight);
  return `oklch(${v[0].toFixed(4)} ${v[1].toFixed(4)} ${v[2].toFixed(2)})`;
}
export function quotaUsageColor(fraction) {
  if (fraction == null || !Number.isFinite(fraction) || fraction <= 0) return 'var(--status-success)';
  const f = clamp(fraction);
  let h, s, l;
  if (f <= .5) { const t = f / .5; h = 150 - 6 * t; s = 58 + 4 * t; l = 46 + 2 * t; }
  else if (f <= .78) { const t = (f - .5) / .28; h = 144 - 104 * t; s = 62 + 20 * t; l = 48 + 4 * t; }
  else { const t = (f - .78) / .22; h = 40 - 34 * t; s = 82 + 4 * t; l = 52 - 6 * t; }
  return `hsl(${h.toFixed(1)} ${s.toFixed(1)}% ${l.toFixed(1)}%)`;
}
export function quotaSegments(fraction) {
  const f = clamp(fraction);
  return Array.from({ length: 18 }, (_, i) => {
    const color = quotaPositionColor((i + .5) / 18);
    if ((i + .5) / 18 <= f) return color;
    if (i === 0 && f > 0) return `linear-gradient(to right, ${color} 0%, ${color} 10%, var(--progress-bg) 10%, var(--progress-bg) 100%)`;
    return 'var(--progress-bg)';
  });
}
