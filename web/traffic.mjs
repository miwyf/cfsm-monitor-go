const GIB = 1024 ** 3;

export function dateInZone(now, zone) {
  const parts = Object.fromEntries(new Intl.DateTimeFormat('en-CA', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(now).map((p) => [p.type, p.value]));
  return `${parts.year}-${parts.month}-${parts.day}`;
}

export function shiftDate(date, offset) {
  const d = new Date(`${date}T12:00:00Z`);
  d.setUTCDate(d.getUTCDate() + offset);
  return d.toISOString().slice(0, 10);
}

export function dayLabel(date) { return `${Number(date.slice(5, 7))}/${Number(date.slice(8, 10))}`; }

function observation(row) {
  const valid = row && row.samples > 0 && Number.isFinite(row.used_bytes) && row.used_bytes >= 0;
  return { ...row, value: valid ? row.used_bytes / GIB : null, partial: Boolean(row?.partial), estimated: Boolean(row?.estimated) };
}

// Only preceding calendar days with complete local records enter a baseline.
// Today's running total is never extrapolated to a full day.
function baseline(rows, date, today) {
  const first = shiftDate(date, -7);
  const values = rows.filter((d) => d.date >= first && d.date < date && d.date < today && d.value !== null && !d.partial).map((d) => d.value).sort((a, b) => a - b);
  if (values.length < 3) return { count: values.length, median: null, average: null };
  const middle = Math.floor(values.length / 2);
  return { count: values.length, median: values.length % 2 ? values[middle] : (values[middle - 1] + values[middle]) / 2, average: values.reduce((a, b) => a + b, 0) / values.length };
}

export function analyze(server, today, now = new Date()) {
  const rows = (server.days || []).filter((d) => d.date <= today).map(observation).sort((a, b) => a.date.localeCompare(b.date));
  const days = rows.map((d) => {
    const base = baseline(rows, d.date, today);
    const spike = d.value !== null && !d.partial && base.median !== null && d.value >= 1 && d.value > base.median * 3;
    return { ...d, baseline: base, spike, multiple: spike && base.median > 0 ? d.value / base.median : null };
  });
  const current = days.find((d) => d.date === today) || observation({ date: today });
  const yesterday = days.find((d) => d.date === shiftDate(today, -1)) || observation({ date: shiftDate(today, -1) });
  const base = baseline(rows, today, today);
  const recentSpikes = days.filter((d) => d.spike && d.date >= shiftDate(today, -13));
  const resetTime = server.reset ? new Date(server.reset).getTime() : NaN;
  const resetDays = Number.isFinite(resetTime) ? Math.max(0, Math.ceil((resetTime - now.getTime()) / 86400000)) : null;
  const exhausted = server.remaining_gib !== null && server.remaining_gib !== undefined && server.limit_gib > 0 && server.remaining_gib <= 0;
  const projected = !exhausted && server.remaining_gib != null && resetDays > 0 && base.average > 0 && server.remaining_gib < base.average * resetDays;
  const low = !exhausted && server.limit_gib > 0 && server.remaining_gib != null && server.remaining_gib / server.limit_gib < 0.1;
  let status;
  if (exhausted) status = { rank: 0, kind: 'danger', label: '配额已用尽' };
  else if (current.spike) status = { rank: 1, kind: 'danger', label: '今日用量突增' };
  else if (projected) status = { rank: 2, kind: 'warning', label: '可能提前用尽' };
  else if (low) status = { rank: 3, kind: 'warning', label: '余量不足 10%' };
  else if (recentSpikes.length) status = { rank: 4, kind: 'warning', label: '近期有用量突增' };
  else if (base.median === null) status = { rank: 5, kind: 'muted', label: '积累基准中' };
  else status = { rank: 6, kind: 'normal', label: '未发现用量突增' };
  return { days, today: current, yesterday, baseline: base, recentSpikes, resetDays, projected, status };
}
