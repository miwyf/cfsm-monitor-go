import { initNotifications } from './notifications.js';
import { dateInZone, shiftDate } from './traffic.mjs';
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const number = (n, digits = 2) => n == null ? '—' : Number(n).toLocaleString('zh-CN', {maximumFractionDigits:digits});
let status = null, offset = 0, query = null, loggedIn = false, refreshing = false, hasMore = false, epoch = 0;
function message(text, error = false) { $('message').textContent = text; $('message').hidden = !text; $('message').classList.toggle('is-error', error); }
let notifications;
let updateTarget = '', upgrading = false;
function showLogin() {
  notifications?.clear();
  $('setup-panel').hidden=true; $('setup-form').reset(); $('dashboard-link').hidden=false;
  epoch++; loggedIn = false; status = null; query=null; offset=0; hasMore=false; $('admin-layout').hidden = true; $('logout').hidden = true; $('login-panel').hidden = false;
  $('source-form').reset(); $('account-form').reset(); $('history-form').reset();
  updateTarget = '';
  $('apply-update').hidden = true;
  for (const id of ['history-node','history-rows','status-grid','collection-details','database-details']) $(id).innerHTML = '';
}
function showSetup() {
  showLogin(); $('login-panel').hidden=true; $('setup-panel').hidden=false; $('dashboard-link').hidden=true;
}
async function api(path, body) {
  const response = await fetch(path, {credentials:'same-origin',cache:'no-store',headers:body === undefined ? {} : {'Content-Type':'application/json','X-CFSM-Request':'1'},...(body === undefined ? {} : {method:'POST',body:JSON.stringify(body)})});
  const result = await response.json();
  if (!response.ok) { if (response.status === 401) showLogin(); const error=new Error(result.error || '请求失败'); error.status=response.status; throw error; }
  return result;
}
const time = (value) => value ? new Date(value).toLocaleString('zh-CN',{hour12:false}) : '—';
const details = (pairs) => pairs.map(([label,value]) => `<div><dt>${esc(label)}</dt><dd>${esc(value)}</dd></div>`).join('');
function renderStatus(s) {
  status = s;
  const state = s.collecting ? '采集中' : !s.configured ? '等待配置' : s.available ? '正常' : '采集失败';
  $('status-grid').innerHTML = [['状态',state],['节点',`${s.nodes.length} · 隐藏 ${s.nodes.filter((n) => n.hidden).length}`],['本次运行采集',s.fetch_count],['连续失败',s.failures]].map(([label,value]) => `<article><span>${esc(label)}</span><strong>${esc(value)}</strong></article>`).join('');
  $('collection-details').innerHTML = details([['最近成功',time(s.last_success_at)],['最近尝试',time(s.last_attempt_at)],['下次采集',s.configured ? time(s.next_poll_at) : '—'],['采集间隔',`${s.poll_seconds} 秒`],['启动时间',time(s.started_at)],['时区',s.time_zone]]);
  $('collection-error').textContent = s.last_error; $('collection-error').hidden = !s.last_error;
  const recent = s.last_attempt_at && Date.now() - new Date(s.last_attempt_at).getTime() < 30000;
  $('collect').disabled = !s.configured || s.collecting || recent;
  $('database-details').innerHTML = details([['数据库',`SQLite ${s.database.version}`],['文件',s.database.file],['日志模式',s.database.journal_mode.toUpperCase()],['占用空间',`${number(s.database.bytes / 1024 ** 2)} MiB`],['每日记录',number(s.database.records,0)],['日期范围',s.database.first_day ? `${s.database.first_day} — ${s.database.last_day}` : '—']]);
  const selected = $('history-node').value;
  $('history-node').innerHTML = s.nodes.map((n) => `<option value="${esc(n.id)}">${esc(n.name)}${n.hidden ? ' · 隐藏' : ''}</option>`).join('');
  if (s.nodes.some((n) => n.id === selected)) $('history-node').value = selected;
}
async function refreshStatus() {
  if (refreshing || !loggedIn || upgrading) return;
  refreshing = true; const current=epoch;
  try { const next=await api('/api/admin/status'); if(loggedIn && current===epoch) renderStatus(next); } catch (e) { message(e.message,true); } finally { refreshing = false; }
}
async function loadAll() {
  const current=epoch;
  const [s, config] = await Promise.all([api('/api/admin/status'),api('/api/settings')]);
  if(current!==epoch) return; loggedIn = true; $('setup-panel').hidden=true; $('dashboard-link').hidden=false; $('login-panel').hidden = true; $('admin-layout').hidden = false; $('logout').hidden = false;
  renderStatus(s);
  $('source-url').value = config.url; $('source-key').value = ''; $('source-key').required = !config.key_configured;
  $('key-hint').textContent = config.key_configured ? '已保存，留空不改' : 'CFSM → 安全设置 → App 访问口令';
  $('source-interval').value = config.poll_seconds; $('retention-days').value = config.retention_days; $('account-username').value = s.username;
  const today = dateInZone(new Date(), s.time_zone); $('history-to').value = today; $('history-from').value = shiftDate(today,-29);
}
async function busy(button, action) { button.disabled = true; message(''); try { await action(); } catch (e) { message(e.message,true); } finally {
 if(button.id==='collect') button.disabled=!status || !status.configured || status.collecting || (status.last_attempt_at && Date.now()-new Date(status.last_attempt_at).getTime()<30000);
 else if(button.id==='previous-page') button.disabled=offset===0;
 else if(button.id==='next-page') button.disabled=!hasMore;
 else button.disabled=false;
} }
document.querySelectorAll('[data-panel]').forEach((button) => button.addEventListener('click', () => {
  document.querySelectorAll('[data-panel]').forEach((b) => b.setAttribute('aria-current',b === button ? 'page' : 'false'));
  document.querySelectorAll('[data-view]').forEach((view) => { view.hidden = view.dataset.view !== button.dataset.panel; });
  message('');
  if (button.dataset.panel === 'update' && !upgrading) loadUpdateStatus();
}));
async function loadUpdateStatus() {
  $('check-update').disabled = true;
  $('apply-update').hidden = true;
  try {
    const info = await api('/api/admin/update/status');
    $('update-versions').innerHTML = details([['当前版本', `v${info.current}`]]);
    $('update-progress').textContent = info.enabled ? '' : '首次请更新到支持应用内升级的 Docker 镜像。';
    $('check-update').disabled = !info.enabled;
  } catch (error) {
    $('update-progress').textContent = error.message;
  }
}
$('check-update').addEventListener('click', () => busy($('check-update'), async () => {
  $('apply-update').hidden = true;
  $('update-progress').textContent = '正在检查 GitHub 发布版本…';
  try {
    const info = await api('/api/admin/update');
    $('update-versions').innerHTML = details([['当前版本', `v${info.current}`], ['最新正式版', info.latest ? `v${info.latest}` : '—']]);
    $('update-progress').textContent = info.needs_image ? '这个版本需要更新 Docker 镜像。' :
      info.available ? '升级时服务会短暂中断，完成后请重新登录。' :
      info.latest ? '当前已是最新版本。' : '暂无可用的正式版本。';
    updateTarget = info.available ? info.latest : '';
    $('apply-update').hidden = !updateTarget;
  } catch (error) {
    $('update-progress').textContent = error.message;
  }
}));
$('apply-update').addEventListener('click', async () => {
  const target = updateTarget;
  if (!target || upgrading || !window.confirm(`升级到 v${target}？服务会短暂重启。`)) return;
  upgrading = true;
  $('check-update').disabled = $('apply-update').disabled = true;
  try {
    await api('/api/admin/update', {version: target});
    $('update-progress').textContent = '正在下载并校验升级程序…';
    const deadline = Date.now() + 240000;
    let restartingAt = 0;
    let consecutiveReady = 0;
    while (Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 1500));
      const options = {cache:'no-store', signal: AbortSignal.timeout(4000)};
      const version = await fetch('/api/version', options).then((res) => res.json()).catch(() => null);
      consecutiveReady = version?.version === target ? consecutiveReady + 1 : 0;
      if (consecutiveReady === 4) {
        sessionStorage.setItem('cfsm-upgraded', target);
        window.location.reload();
        return;
      }
      const progress = await fetch('/api/admin/update/status', options).then(async (res) => ({status:res.status, ...await res.json()})).catch(() => null);
      if (progress?.stage === 'error') throw new Error(progress.error);
      if (progress?.stage === 'restarting' || progress?.status === 401) {
        restartingAt ||= Date.now();
        $('update-progress').textContent = '正在切换版本并等待服务恢复…';
      }
      if (restartingAt && Date.now() - restartingAt > 35000) break;
    }
    throw new Error('未确认新版启动，请刷新页面核对当前版本。');
  } catch (error) {
    $('update-progress').textContent = error.message;
    $('check-update').disabled = $('apply-update').disabled = false;
  } finally {
    upgrading = false;
  }
});
$('login-form').addEventListener('submit',(e) => { e.preventDefault(); busy(e.submitter,async () => { await api('/api/login',{username:$('login-username').value,password:$('login-password').value}); $('login-password').value=''; await loadAll(); }); });
$('setup-form').addEventListener('submit',(e) => { e.preventDefault(); busy(e.submitter,async () => {
  if ($('setup-password').value !== $('setup-confirm').value) throw new Error('两次密码不一致');
  const username=$('setup-username').value;
  try { await api('/api/setup',{username,password:$('setup-password').value,confirm_password:$('setup-confirm').value}); }
  catch (error) { if(error.status===409) await start(); throw error; }
  $('setup-form').reset(); $('login-username').value=username; await loadAll(); document.querySelector('[data-panel=source]').click(); message('管理员已创建');
}); });
$('logout').addEventListener('click',() => busy($('logout'),async () => { await api('/api/logout',{}); showLogin(); message(''); }));
$('reload-status').addEventListener('click',refreshStatus);
$('collect').addEventListener('click',() => busy($('collect'),async () => { try { await api('/api/admin/collect',{}); message('采集完成'); } finally { await refreshStatus(); } }));
$('source-form').addEventListener('submit',(e) => { e.preventDefault(); busy(e.submitter,async () => {
  await api('/api/settings',{url:$('source-url').value,key:$('source-key').value,poll_seconds:Number($('source-interval').value)});
  $('source-key').value=''; $('source-key').required=false; $('key-hint').textContent='已保存，留空不改'; message('数据源已保存'); await refreshStatus();
}); });
$('retention-form').addEventListener('submit',(e) => { e.preventDefault(); busy(e.submitter,async () => { await api('/api/admin/retention',{retention_days:Number($('retention-days').value)}); message('保留天数已保存，过期记录已清理'); await refreshStatus(); }); });
async function loadHistory() {
  if (!query) return; const current=epoch;
  const result = await api('/api/admin/history?' + new URLSearchParams({...query,offset:String(offset)}));
  if (current !== epoch || !loggedIn) return;
  $('history-rows').innerHTML = result.days.map((d) => `<tr><td>${esc(d.date)}</td><td>${d.samples ? number(d.used_bytes / 1024 ** 3,3) : '—'}</td><td>${number(d.samples,0)}</td><td>${d.partial ? '部分记录' : ''}${d.estimated ? (d.partial ? ' · ' : '') + '跨日估算' : ''}${!d.partial && !d.estimated ? '完整' : ''}</td></tr>`).join('') || '<tr><td colspan="4" class="empty">暂无记录</td></tr>';
  $('history-count').textContent = `${offset + 1}–${offset + result.days.length} 条`; if (!result.days.length) $('history-count').textContent='0 条';
  hasMore=result.has_more; $('previous-page').disabled = offset === 0; $('next-page').disabled = !hasMore;
}
$('history-form').addEventListener('submit',(e) => { e.preventDefault(); busy(e.submitter,async () => { offset=0; query={node:$('history-node').value,from:$('history-from').value,to:$('history-to').value}; await loadHistory(); }); });
$('previous-page').addEventListener('click',() => busy($('previous-page'),async () => { offset=Math.max(0,offset-100); await loadHistory(); }));
$('next-page').addEventListener('click',() => busy($('next-page'),async () => { offset+=100; await loadHistory(); }));
$('clear-open').addEventListener('click',() => $('clear-dialog').showModal());
$('clear-cancel').addEventListener('click',() => $('clear-dialog').close());
$('clear-confirm').addEventListener('click',() => busy($('clear-confirm'),async () => { await api('/api/admin/clear-history',{confirm:'CLEAR'}); $('clear-dialog').close(); $('history-rows').innerHTML=''; $('history-count').textContent=''; query=null; offset=0; hasMore=false; $('previous-page').disabled=true; $('next-page').disabled=true; message('历史已清空'); await refreshStatus(); }));
$('account-form').addEventListener('submit',(e) => { e.preventDefault(); busy(e.submitter,async () => {
  if ($('account-password').value !== $('account-confirm').value) throw new Error('两次新密码不一致');
  const username=$('account-username').value;
  await api('/api/admin/account',{username,current_password:$('account-current').value,password:$('account-password').value});
  showLogin(); $('login-username').value=username; message('账号已更新，请重新登录');
}); });
async function start() { try { const session=await api('/api/session'); if (session.setup_required) showSetup(); else if (session.admin) await loadAll(); else showLogin(); } catch (e) { showLogin(); message(e.message,true); } }
notifications=initNotifications({api,message,busy,epoch:()=>epoch,loggedIn:()=>loggedIn,nodes:()=>status?.nodes||[],zone:()=>status?.time_zone||'Asia/Shanghai'});
start(); setInterval(refreshStatus,10000);
const upgraded = sessionStorage.getItem('cfsm-upgraded');
if (upgraded) {
  sessionStorage.removeItem('cfsm-upgraded');
  message(`已升级到 v${upgraded}，请重新登录。`);
}
api('/api/version').then((result) => { if (result.version) $('app-version').textContent = `v${result.version}`; }).catch(() => {});
window.addEventListener('pageshow',(e) => { if (e.persisted) { showLogin(); start(); } });
