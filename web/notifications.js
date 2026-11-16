const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const providers={feishu:'飞书',wecom:'企业微信',dingtalk:'钉钉'};
const scopes={public:'仅公开',hidden:'仅隐藏',all:'全部节点',selected:'指定节点'};
const states={pending:'待发送',sent:'已发送',failed:'失败',cancelled:'已取消'};
const kinds={test:'测试',alert:'流量预警',daily:'每日播报'};
const defaults=()=>({id:'',name:'',provider:'feishu',enabled:true,scope:'public',node_ids:[],alerts:true,quota_thresholds:[80,90,100],daily_gib:0,spike:true,forecast:false,daily:true,daily_times:['08:00']});
export function initNotifications({api,message,busy,epoch,loggedIn,nodes,zone}) {
  let channels=[],editing=null,loading=false;
  const stamp=(v)=>v?new Date(v*1000).toLocaleString('zh-CN',{hour12:false,timeZone:zone()}):'—';
  function clear(){channels=[];editing=null;$('notification-form').reset();$('notify-nodes').replaceChildren();$('notification-dialog').close();$('notification-channels').innerHTML='';$('notification-log').innerHTML='';}
  function render(){
    $('notification-channels').innerHTML=channels.map(({config:c})=>`<article class="admin-box notification-channel"><div><h2>${esc(c.name)} <span class="row-status">${c.enabled?'启用':'停用'}</span></h2><p>${providers[c.provider]} · ${scopes[c.scope]}${c.scope==='selected'?` ${c.node_ids.length} 个`:''}</p><p>${c.alerts?'流量预警':''}${c.alerts&&c.daily?' · ':''}${c.daily?`每日 ${c.daily_times.join('、')}`:''}</p></div><div class="notification-actions"><button data-notify-edit="${c.id}">编辑</button><button data-notify-test="${c.id}">测试发送</button><button data-notify-toggle="${c.id}">${c.enabled?'停用':'启用'}</button><button data-notify-delete="${c.id}" class="danger-button">删除</button></div></article>`).join('')||'<div class="admin-box empty">暂无通知渠道</div>';
  }
  async function load(){
    if(!loggedIn()||loading)return;loading=true;const current=epoch();
    try{
      const result=await api('/api/admin/notifications');if(current!==epoch()||!loggedIn())return;
      channels=result.channels;render();$('notification-zone').textContent='时区：'+result.time_zone;
      $('notification-log').innerHTML=result.logs.map(l=>`<tr><td>${esc(stamp(l.created_at))}</td><td>${esc(l.channel)}</td><td>${kinds[l.kind]||esc(l.kind)}</td><td>${states[l.status]||esc(l.status)}</td><td>${l.attempts}</td><td>${esc(l.last_error|| (l.sent_at?stamp(l.sent_at):'—'))}</td><td>${l.status==='failed'?`<button data-notify-retry="${l.id}">重试</button>`:''}</td></tr>`).join('')||'<tr><td colspan="7" class="empty">暂无发送记录</td></tr>';
    }finally{loading=false;}
  }
  function choices(){
    $('notify-nodes-label').hidden=$('notify-scope').value!=='selected';
    $('notify-secret-label').hidden=$('notify-provider').value==='wecom';
    const same=editing&&editing.config.provider===$('notify-provider').value;
    $('notify-webhook').required=!same||!editing.webhook_configured;
    $('notify-webhook').placeholder=same&&editing.webhook_configured?'已保存，留空不改':'机器人 Webhook 地址';
    $('notify-secret').placeholder=same&&editing.secret_configured?'已保存，留空不改':'启用加签时填写';
  }
  function selectionCount(){const inputs=[...$('notify-nodes').querySelectorAll('input')];$('notify-node-count').textContent=`已选 ${inputs.filter(input=>input.checked).length}/${inputs.length}`;}
  function edit(id){
    editing=channels.find(v=>v.config.id===id)||null;const c=editing?.config||defaults();
    $('notification-error').textContent='';$('notification-form').reset();$('notification-title').textContent=editing?'编辑通知渠道':'新增通知渠道';
    for(const field of ['name','provider','scope'])$('notify-'+field).value=c[field];
    for(const field of ['enabled','alerts','spike','forecast','daily'])$('notify-'+field).checked=c[field];
    $('notify-thresholds').value=c.quota_thresholds.join(', ');$('notify-daily-gib').value=c.daily_gib;
    $('notify-times').value=c.daily_times.join(', ');$('notify-secret').placeholder=editing?.secret_configured?'已保存，留空不改':'启用加签时填写';
    const options=[...nodes()];for(const id of c.node_ids){if(!options.some(n=>n.id===id))options.push({id,name:'已移除节点 '+id});}
    $('notify-nodes').innerHTML=options.map(n=>`<label class="check-line notification-node"><input type="checkbox" value="${esc(n.id)}" ${c.node_ids.includes(n.id)?'checked':''}><span>${esc(n.name)}${n.hidden?' · 隐藏':''}</span></label>`).join('')||'<p class="notification-hint">暂无节点</p>';
    selectionCount();
    choices();$('notification-dialog').showModal();
  }
  $('notification-add').addEventListener('click',()=>edit(''));
  $('notification-reload').addEventListener('click',()=>busy($('notification-reload'),load));
  document.querySelector('[data-panel=notifications]').addEventListener('click',()=>load().catch(e=>message(e.message,true)));
  $('notify-provider').addEventListener('change',()=>{const wecom=$('notify-provider').value==='wecom';if(wecom)$('notify-secret').value='';$('notify-clear-secret').checked=wecom;choices();});$('notify-scope').addEventListener('change',choices);
  $('notify-secret').addEventListener('input',()=>{if($('notify-secret').value)$('notify-clear-secret').checked=false;});
  $('notify-nodes').addEventListener('change',selectionCount);
  for(const [id,checked] of [['notify-select-all',true],['notify-select-none',false]])$(id).addEventListener('click',()=>{$('notify-nodes').querySelectorAll('input').forEach(input=>input.checked=checked);selectionCount();});
  $('notification-cancel').addEventListener('click',()=>$('notification-dialog').close());
  $('notification-dialog').addEventListener('close',()=>{$('notify-webhook').value='';$('notify-secret').value='';});
  $('notification-form').addEventListener('submit',e=>{e.preventDefault();$('notification-error').textContent='';busy(e.submitter,async()=>{try{
    const thresholds=$('notify-thresholds').value.split(/[,，\s]+/).filter(Boolean).map(Number);
    if(!thresholds.every(v=>Number.isInteger(v)&&v>=1&&v<=100))throw new Error('配额阈值请填 1–100 的整数');
    const nodeIDs=[...$('notify-nodes').querySelectorAll('input:checked')].map(input=>input.value);
    if($('notify-scope').value==='selected'&&!nodeIDs.length)throw new Error('请至少选择一个节点');
    const c={id:editing?.config.id||'',name:$('notify-name').value,provider:$('notify-provider').value,scope:$('notify-scope').value,node_ids:nodeIDs,webhook:$('notify-webhook').value.trim(),secret:$('notify-secret').value,clear_secret:$('notify-clear-secret').checked,quota_thresholds:thresholds,daily_gib:Number($('notify-daily-gib').value),daily_times:$('notify-times').value.split(/[,，\s]+/).filter(Boolean)};
    for(const field of ['enabled','alerts','spike','forecast','daily'])c[field]=$('notify-'+field).checked;
    await api('/api/admin/notifications/save',c);$('notification-dialog').close();await load();message('通知渠道已保存');
  }catch(error){$('notification-error').textContent=error.message;}});});
  $('notification-channels').addEventListener('click',e=>{
    const button=e.target.closest('button');if(!button)return;
    if(button.dataset.notifyEdit){edit(button.dataset.notifyEdit);return;}
    busy(button,async()=>{
      if(button.dataset.notifyTest){await api('/api/admin/notifications/test',{id:button.dataset.notifyTest});message('测试消息已排队，请查看发送记录');}
      if(button.dataset.notifyToggle){const c=channels.find(v=>v.config.id===button.dataset.notifyToggle).config;await api('/api/admin/notifications/save',{...c,enabled:!c.enabled});}
      if(button.dataset.notifyDelete){if(!confirm('删除此通知渠道及其发送记录？'))return;await api('/api/admin/notifications/delete',{id:button.dataset.notifyDelete});}
      await load();
    });
  });
  $('notification-log').addEventListener('click',e=>{const button=e.target.closest('[data-notify-retry]');if(button)busy(button,async()=>{await api('/api/admin/notifications/retry',{id:Number(button.dataset.notifyRetry)});await load();message('已重新排队');});});
  setInterval(()=>{if(loggedIn()&&!document.querySelector('[data-view=notifications]').hidden)load().catch(e=>message(e.message,true));},10000);
  return {clear,load};
}
