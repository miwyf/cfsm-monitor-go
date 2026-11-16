'use strict';
import { analyze, dateInZone, dayLabel } from './traffic.mjs';
import { nodeColors, quotaSegments, quotaUsageColor } from './theme.mjs';
const $ = (id) => document.getElementById(id);
const state = { admin: false, servers: [], filter: '', search: '', sort: 'attention', range: 14, nodeView: 'cards', collapsed: [], chartNode: '', chartDay: '', tableExpanded: false, selectedDay: '', zone: 'Asia/Shanghai', busy: false, epoch: 0 };
try { if (localStorage.getItem('cfsm-node-view') === 'list') state.nodeView = 'list'; } catch {}
try {
  const collapsed = JSON.parse(localStorage.getItem('cfsm-collapsed-sections'));
  if (Array.isArray(collapsed)) state.collapsed = collapsed.filter((id) => ['cards-panel', 'details'].includes(id));
} catch {}
const colors = nodeColors;
const esc = (v) => String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const num = (v, digits = 1) => v == null ? '—' : Number(v).toLocaleString('zh-CN', { maximumFractionDigits: digits });
const gib = (v) => `${num(v, 2)} <small>GiB</small>`;
const width = (v) => Math.max(0, Math.min(100, Number(v) || 0));

async function api(path, body) {
  const response = await fetch(path, { credentials: 'same-origin', cache: 'no-store', headers: body === undefined ? {} : { 'Content-Type': 'application/json', 'X-CFSM-Request': '1' }, ...(body === undefined ? {} : { method: 'POST', body: JSON.stringify(body) }) });
  const result = await response.json();
  if (!response.ok) { if (response.status === 401) setAdmin(false); throw new Error(result.error || '请求失败'); }
  return result;
}
function setAdmin(admin) {
  state.admin = admin;
  $('admin-link').hidden = !admin;
  $('auth-button').textContent = admin ? '退出登录 ↗' : '管理员登录 ↗';
  $('view-mode').textContent = admin ? '管理员视图' : '公开视图';
  if (!admin) { state.servers = state.servers.filter((s) => !s.hidden); render(); }
}
async function refresh() {
  if (state.busy) return;
  state.busy = true; const epoch = state.epoch; $('refresh').disabled = true;
  try {
    const result = await api('/api/servers');
    if (epoch !== state.epoch) return;
    state.servers = result.servers; state.zone = result.time_zone; setAdmin(result.admin);
    $('updated').textContent = new Date(result.fetched_at).toLocaleString('zh-CN', { hour12: false });
    $('poll-hint').textContent = `${result.poll_seconds}s · ${result.time_zone}`; $('notice').hidden = true;
  } catch (error) {
    if (epoch !== state.epoch) return;
    state.servers = []; $('updated').textContent = '数据暂不可用'; $('notice').textContent = error.message; $('notice').hidden = false;
    try { setAdmin((await api('/api/session')).admin); } catch { setAdmin(false); }
  } finally { if (epoch === state.epoch) { state.busy = false; $('refresh').disabled = false; render(); } }
}
function render() {
  document.querySelectorAll('[data-collapse]').forEach((button) => {
    const id = button.dataset.collapse, collapsed = state.collapsed.includes(id);
    $(button.getAttribute('aria-controls')).hidden = collapsed;
    $(id).classList.toggle('is-collapsed', collapsed);
    button.textContent = collapsed ? '展开' : '收起';
    button.setAttribute('aria-expanded', String(!collapsed));
    button.setAttribute('aria-label', `${collapsed ? '展开' : '收起'}${button.dataset.label}`);
    document.querySelector(`[data-block-tools="${id}"]`).hidden = collapsed;
  });
  if (state.filter && !state.servers.some((s) => s.id === state.filter)) state.filter = '';
  $('node-filter').innerHTML = '<option value="">全部节点</option>' + state.servers.map((s) => `<option value="${esc(s.id)}">${esc(s.name)}</option>`).join('');
  $('node-filter').value = state.filter;
  const today = dateInZone(new Date(), state.zone), query = state.search.toLowerCase();
  const items = state.servers.filter((s) => (!state.filter || state.filter === s.id) && `${s.name} ${s.region} ${s.group}`.toLowerCase().includes(query)).map((s) => ({ ...s, color: colors[state.servers.indexOf(s) % colors.length], traffic: analyze(s, today) }));
  items.sort((a, b) => state.sort === 'remaining' ? (a.remaining_gib ?? Infinity) - (b.remaining_gib ?? Infinity) : state.sort === 'today' ? (b.traffic.today.value ?? -1) - (a.traffic.today.value ?? -1) : a.traffic.status.rank - b.traffic.status.rank || (b.traffic.today.value ?? -1) - (a.traffic.today.value ?? -1));
  $('node-count').textContent = String(items.length);
  const spikes = items.filter((s) => s.traffic.today.spike).length, risks = items.filter((s) => s.traffic.status.rank === 0 || s.traffic.projected).length, collecting = items.filter((s) => s.traffic.baseline.median === null).length;
  $('attention-note').innerHTML = `<span class="${spikes ? 'danger' : ''}">今日突增 <b>${spikes}</b></span><span class="${risks ? 'warning' : ''}">配额风险 <b>${risks}</b></span>${collecting ? `<span>基准不足 <b>${collecting}</b></span>` : ''}`;
  const list = state.nodeView === 'list';
  document.querySelectorAll('[data-node-view]').forEach((button) => button.setAttribute('aria-pressed', String(button.dataset.nodeView === state.nodeView)));
  $('cards').hidden = list; $('node-list-wrap').hidden = !list;
  $('cards').innerHTML = list ? '' : items.length ? items.map(nodeCard).join('') : '<div class="empty">暂无节点</div>';
  $('node-list').innerHTML = !list ? '' : items.length ? items.map(nodeListRow).join('') : '<tr><td colspan="9" class="empty">暂无节点</td></tr>';
  if ($('traffic-dialog').open) {
    const server = state.servers.find((s) => s.id === state.chartNode);
    if (server) drawHistory([{ ...server, color: colors[state.servers.indexOf(server) % colors.length], traffic: analyze(server, today) }]);
    else { clearNodeChart(); $('traffic-dialog').close(); }
  }
  drawTable(items);
  document.querySelectorAll('[data-width]').forEach((bar) => { bar.style.width = `${width(bar.dataset.width)}%`; });
  document.querySelectorAll('[data-color]').forEach((mark) => { mark.style.backgroundColor = mark.dataset.color; });
  document.querySelectorAll('[data-quota-color]').forEach((mark) => { mark.style.background = mark.dataset.quotaColor; });
  document.querySelectorAll('[data-usage-color]').forEach((mark) => { mark.style.color = mark.dataset.usageColor; });
}
function readingNote(d) { return d.value === null ? '无样本' : d.partial ? '部分记录' : d.estimated ? '跨日估算' : ''; }
function nodeListRow(s) {
  const t = s.traffic, percent = s.limit_gib && s.used_gib != null ? s.used_gib / s.limit_gib * 100 : null;
  const remainingKind = s.limit_gib > 0 && s.remaining_gib === 0 ? 'danger' : t.projected || (percent !== null && percent > 90) ? 'warning' : '';
  const status = t.status.rank === 6 ? '—' : `<span class="row-status ${t.status.kind}">${t.status.rank === 5 ? '基准不足' : esc(t.status.label)}</span>`;
  const reading = (d) => `${num(d.value, 2)}${d.partial && d.value !== null ? '*' : ''}`;
  return `<tr class="node-flow-row" data-node="${esc(s.id)}" data-open-node="${esc(s.id)}"><th scope="row"><button class="node-name" data-open-node="${esc(s.id)}" title="${esc(s.name)}"><i data-color="${s.color}"></i><span>${esc(s.name)}</span></button>${s.hidden ? '<span class="hidden-tag">隐藏</span>' : ''}<small>${esc(s.group || s.region || '')}${!s.online ? ' · 上报过期' : ''}</small></th><td class="${t.today.spike ? 'danger' : ''}" title="${esc(readingNote(t.today))}">${reading(t.today)}</td><td title="${esc(readingNote(t.yesterday))}">${reading(t.yesterday)}</td><td>${num(s.used_gib, 2)}</td><td class="list-remaining ${remainingKind}">${num(s.remaining_gib, 2)}</td><td>${num(s.limit_gib, 2)}</td><td><div class="list-usage"><strong data-usage-color="${quotaUsageColor(percent === null ? null : percent / 100)}">${percent === null ? '—' : `${num(percent, 1)}%`}</strong><div class="quota-gauge" role="meter" aria-label="${esc(s.name)}配额使用率" aria-valuemin="0" aria-valuemax="100" ${percent === null ? 'aria-valuetext="未设置配额"' : `aria-valuenow="${width(percent)}" aria-valuetext="已用 ${num(percent, 1)}%"`}>${quotaSegments(percent === null ? null : percent / 100).map((color) => `<i data-quota-color="${color}"></i>`).join('')}</div></div></td><td>${s.reset ? esc(s.reset.slice(0, 10).replaceAll('-', '/')) : '—'}${t.resetDays === null ? '' : `<small>${t.resetDays} 天后</small>`}</td><td class="list-status">${status}${t.projected ? `<small>约 ${num(s.remaining_gib / t.baseline.average)} 天用尽</small>` : ''}</td></tr>`;
}
function nodeCard(s) {
  const t = s.traffic, percent = s.limit_gib && s.used_gib != null ? s.used_gib / s.limit_gib * 100 : null;
  const remainingKind = s.limit_gib > 0 && s.remaining_gib === 0 ? 'danger' : t.projected || (percent !== null && percent > 90) ? 'warning' : '';
  const status = t.status.rank === 6 ? '' : `<span class="row-status ${t.status.kind}">${t.status.rank === 5 ? '基准不足' : esc(t.status.label)}</span>`;
  return `<article class="node-card" data-node="${esc(s.id)}" data-open-node="${esc(s.id)}"><div class="card-head"><div><button class="node-name" data-open-node="${esc(s.id)}"><i data-color="${s.color}"></i>${esc(s.name)}</button>${s.hidden ? '<span class="hidden-tag">隐藏</span>' : ''}<small>${esc(s.group || s.region || '')}${!s.online ? ' · 上报过期' : ''}</small></div>${status}</div><div class="card-quota"><span>本周期已用</span><div><strong data-usage-color="${quotaUsageColor(percent === null ? null : percent / 100)}">${num(s.used_gib, 2)}</strong><span> / ${num(s.limit_gib, 2)} GiB</span></div></div><div class="quota-gauge" role="meter" aria-label="配额使用率" aria-valuemin="0" aria-valuemax="100" ${percent === null ? 'aria-valuetext="未设置配额"' : `aria-valuenow="${width(percent)}" aria-valuetext="已用 ${num(percent, 1)}%"`}>${quotaSegments(percent === null ? null : percent / 100).map((color) => `<i data-quota-color="${color}"></i>`).join('')}</div><div class="quota-meta"><span>总配额 <b>${num(s.limit_gib, 2)} GiB</b></span><span class="${remainingKind}">剩余 <b>${num(s.remaining_gib, 2)} GiB</b></span></div><div class="card-numbers"><div class="${t.today.spike ? 'danger' : ''}"><span>今日 GiB</span><strong>${num(t.today.value, 2)}</strong><small>${t.today.spike ? (t.today.multiple ? `${num(t.today.multiple)} 倍` : '基准 0 GiB') : readingNote(t.today)}</small></div><div><span>昨日 GiB</span><strong>${num(t.yesterday.value, 2)}</strong><small>${readingNote(t.yesterday)}</small></div><div><span>近 7 日均 GiB</span><strong>${num(t.baseline.average, 2)}</strong><small>${t.baseline.average === null ? '基准不足' : ''}</small></div></div><div class="card-bottom"><div><span>下次重置</span><strong>${s.reset ? esc(s.reset.slice(0, 10).replaceAll('-', '/')) : '—'}</strong></div><span class="${t.projected ? 'warning' : ''}">${t.projected ? `约 ${num(s.remaining_gib / t.baseline.average)} 天用尽` : t.resetDays === null ? '' : `${t.resetDays} 天后`}</span></div></article>`;
}
function dateRows(items, count) {
  return [...new Set(items.flatMap((s) => s.traffic.days.map((d) => d.date)))].sort().slice(-count).map((date) => {
    const parts = items.map((server) => ({ server, day: server.traffic.days.find((d) => d.date === date) }));
    const known = parts.filter((p) => p.day?.value != null);
    return { date, parts, value: known.length ? known.reduce((sum, p) => sum + p.day.value, 0) : null, partial: known.length !== items.length || known.some((p) => p.day.partial), spike: known.some((p) => p.day.spike) };
  });
}
function drawHistory(items) {
  const theme = getComputedStyle(document.documentElement);
  const gridColor = theme.getPropertyValue('--border-strong').trim(), axisColor = theme.getPropertyValue('--text-secondary').trim();
  const selectedColor = theme.getPropertyValue('--chart-selection').trim(), spikeColor = theme.getPropertyValue('--status-error').trim();
  document.querySelectorAll('[data-range]').forEach((b) => b.setAttribute('aria-pressed', String(Number(b.dataset.range) === state.range)));
  $('history-title').textContent = items[0].name;
  $('chart-color').dataset.color = items[0].color;
  $('history-label').hidden = items.length !== 1;
  const base = items[0]?.traffic.baseline;
  $('history-label').textContent = !base ? '' : base.median === null ? `基准 ${base.count}/3 天` : `日均 ${num(base.average)} GiB · 中位数 ${num(base.median)} GiB`;
  const data = dateRows(items, state.range);
  if (!data.length) { $('chart').innerHTML = '<div class="empty">暂无记录</div>'; $('day-detail').textContent = ''; return; }
  if (!data.some((d) => d.date === state.chartDay)) state.chartDay = data.at(-1).date;
  const maximum = Math.max(1, ...data.map((d) => d.value || 0)), magnitude = 10 ** Math.floor(Math.log10(maximum)), top = Math.ceil(maximum / magnitude / 0.5) * magnitude * 0.5;
  const w = Math.max(280, $('chart').clientWidth), h = 266, left = 42, right = 10, plotTop = 20, plotHeight = 196;
  const column = (w - left - right) / data.length, barWidth = Math.min(40, column * 0.58);
  let svg = '';
  for (let i = 0; i <= 4; i++) {
    const y = plotTop + plotHeight - i * plotHeight / 4;
    svg += `<line x1="${left}" x2="${w - right}" y1="${y}" y2="${y}" stroke="${gridColor}" stroke-dasharray="3 5"/><text x="${left - 9}" y="${y + 4}" text-anchor="end" fill="${axisColor}" font-size="11">${num(top * i / 4, 1)}</text>`;
  }
  data.forEach((row, index) => {
    const x = left + index * column + (column - barWidth) / 2;
    let bottom = plotTop + plotHeight;
    const title = `${row.date}\n${row.parts.map((p) => `${p.server.name}: ${num(p.day?.value, 2)} GiB${p.day?.partial ? '*' : ''}`).join('\n')}\n已记录合计: ${num(row.value, 2)} GiB${row.partial ? '*' : ''}`;
    svg += `<g data-day="${row.date}" tabindex="0" role="button" aria-label="${esc(title)}"><title>${esc(title)}</title>`;
    if (state.chartDay === row.date) svg += `<rect x="${left + index * column + 1}" y="5" width="${Math.max(1, column - 2)}" height="${plotHeight + 43}" rx="5" fill="${selectedColor}"/>`;
    row.parts.forEach(({ server, day }) => {
      if (day?.value == null) return;
      const height = day.value === 0 ? 1 : Math.max(1, day.value / top * plotHeight); bottom -= height;
      svg += `<rect class="bar-segment${day.spike ? ' spike' : ''}" x="${x}" y="${bottom}" width="${barWidth}" height="${height}" rx="2" fill="${day.spike ? spikeColor : server.color}"/>`;
    });
    if (row.value === null) svg += `<line x1="${x}" x2="${x + barWidth}" y1="${bottom}" y2="${bottom}" stroke="#a1a1aa" stroke-width="2"/>`;
    svg += `<rect x="${left + index * column}" y="${plotTop}" width="${column}" height="${plotHeight}" fill="transparent"/>`;
    const every = w < 600 ? (data.length > 14 ? 7 : data.length > 7 ? 3 : 1) : data.length > 14 ? 3 : 1;
    if ((index % every === 0 && index < data.length - (w < 600 ? 2 : 1)) || index === data.length - 1) svg += `<text x="${x + barWidth / 2}" y="${plotTop + plotHeight + 26}" text-anchor="middle" fill="${axisColor}" font-size="11">${index === data.length - 1 ? '今天' : dayLabel(row.date)}</text>`;
    svg += '</g>';
  });
  $('chart').innerHTML = `<svg class="traffic-chart" viewBox="0 0 ${w} ${h}" aria-label="每日流量柱状图">${svg}</svg>`;
  const selected = data.find((d) => d.date === state.chartDay);
  $('day-detail').innerHTML = `<div><span>${esc(selected.date)}</span><strong class="${selected.spike ? 'danger' : ''}">${gib(selected.value)}${selected.partial && selected.value !== null ? '*' : ''}</strong></div><p>${selected.parts[0]?.day ? esc(readingNote(selected.parts[0].day)) : '无样本'}</p>`;
}
function drawTable(items) {
  $('table-range').textContent = state.tableExpanded ? '收起为 7 天' : '查看 30 天';
  $('daily-head').innerHTML = `<tr><th scope="col">日期</th>${items.map((s) => `<th scope="col"><button data-open-node="${esc(s.id)}"><i data-color="${s.color}"></i>${esc(s.name)}</button></th>`).join('')}<th scope="col">已记录合计</th></tr>`;
  const today = dateInZone(new Date(), state.zone);
  $('nodes').innerHTML = dateRows(items, state.tableExpanded ? 30 : 7).reverse().map((row) => `<tr class="daily-row ${row.date === state.selectedDay ? 'selected' : ''}" data-day="${row.date}"><th scope="row">${row.date}${row.date === today ? '<small>今日</small>' : ''}</th>${row.parts.map(({ day }) => `<td class="${day?.spike ? 'danger' : ''}" title="${day ? esc(readingNote(day)) : '无样本'}">${num(day?.value, 2)}${day?.partial && day.value !== null ? '*' : ''}</td>`).join('')}<td class="daily-total ${row.spike ? 'danger' : ''}" title="${row.partial ? '部分记录' : ''}">${num(row.value, 2)}${row.partial && row.value !== null ? '*' : ''}</td></tr>`).join('') || `<tr><td class="empty" colspan="${items.length + 2}">暂无记录</td></tr>`;
}
$('refresh').addEventListener('click', refresh);
$('search').addEventListener('input', (e) => { state.search = e.target.value; render(); });
$('node-filter').addEventListener('change', (e) => { state.filter = e.target.value; render(); });
$('sort').addEventListener('change', (e) => { state.sort = e.target.value; render(); });
document.querySelectorAll('[data-node-view]').forEach((button) => button.addEventListener('click', () => {
  state.nodeView = button.dataset.nodeView;
  try { localStorage.setItem('cfsm-node-view', state.nodeView); } catch {}
  render();
}));
document.querySelectorAll('[data-collapse]').forEach((button) => button.addEventListener('click', () => {
  const id = button.dataset.collapse;
  state.collapsed = state.collapsed.includes(id) ? state.collapsed.filter((value) => value !== id) : [...state.collapsed, id];
  try { localStorage.setItem('cfsm-collapsed-sections', JSON.stringify(state.collapsed)); } catch {}
  render();
}));
function clearNodeChart() {
  state.chartNode = ''; state.chartDay = '';
  for (const id of ['history-title', 'history-label', 'chart', 'day-detail']) $(id).textContent = '';
}
function openNodeChart(id) {
  if (!state.servers.some((s) => s.id === id)) return;
  state.chartNode = id; state.chartDay = '';
  if (!$('traffic-dialog').open) $('traffic-dialog').showModal();
  render();
}
document.querySelector('main').addEventListener('click', (e) => {
  const node = e.target.closest('[data-open-node]');
  if (node) openNodeChart(node.dataset.openNode);
});
function selectChartDay(e) {
  const day = e.target.closest('[data-day]'); if (!day) return;
  state.chartDay = day.dataset.day; render();
}
$('chart').addEventListener('click', selectChartDay);
$('chart').addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectChartDay(e); } });
$('nodes').addEventListener('click', (e) => {
  const day = e.target.closest('[data-day]'); if (!day) return;
  state.selectedDay = day.dataset.day; render();
});
$('traffic-dialog').addEventListener('close', clearNodeChart);
$('traffic-dialog').addEventListener('click', (e) => {
  const rect = $('traffic-dialog').getBoundingClientRect();
  if (e.target === $('traffic-dialog') && (e.clientX < rect.left || e.clientX > rect.right || e.clientY < rect.top || e.clientY > rect.bottom)) $('traffic-dialog').close();
});
$('table-range').addEventListener('click', () => { state.tableExpanded = !state.tableExpanded; render(); });
let resizeFrame; window.addEventListener('resize', () => { cancelAnimationFrame(resizeFrame); resizeFrame = requestAnimationFrame(render); });
document.querySelectorAll('[data-range]').forEach((b) => b.addEventListener('click', () => { state.range = Number(b.dataset.range); render(); }));
document.querySelectorAll('[data-close]').forEach((button) => button.addEventListener('click', () => $(button.dataset.close).close()));
$('login-dialog').addEventListener('close', () => { $('password').value = ''; $('login-error').textContent = ''; });
$('auth-button').addEventListener('click', async () => {
  if (!state.admin) { $('login-dialog').showModal(); return; }
  state.epoch++; state.servers = []; state.busy = true; render();
  try { await api('/api/logout', {}); setAdmin(false); } catch (error) { $('notice').textContent = error.message; $('notice').hidden = false; }
  finally { state.busy = false; await refresh(); }
});
$('login-form').addEventListener('submit', async (e) => {
  e.preventDefault(); const button = e.submitter; button.disabled = true; $('login-error').textContent = '';
  try { await api('/api/login', { username: $('username').value, password: $('password').value }); state.epoch++; state.busy = false; setAdmin(true); $('login-dialog').close(); await refresh(); }
  catch (error) { $('login-error').textContent = error.message; } finally { button.disabled = false; }
});
window.addEventListener('pageshow', (e) => { if (e.persisted) { state.servers = []; setAdmin(false); refresh(); } });
document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
document.addEventListener('themechange', render);
render(); refresh(); setInterval(refresh, 30000);
api('/api/version').then((result) => { if (result.version) $('app-version').textContent = `v${result.version}`; }).catch(() => {});
