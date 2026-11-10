"use strict";
const STATUS_INTERVAL = 600000, STATUS_BLOCKS = 20;
let logPage = 1, detailRecord = null;
const pendingChecks = new Set();
Object.assign(iconPaths, {
 search:'m21 21-5-5 M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0',
 shield:'m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6z M8 12l3 3 5-6',
 lock:'M5 10h14v11H5z M8 10V6a4 4 0 0 1 8 0v4',
 stop:'M8 3h8l5 5v8l-5 5H8l-5-5V8z M9 9l6 6 M15 9l-6 6'
});
function completionRate(row) {
 const ended=row.completed+row.failed+row.incomplete;
 return ended>0?row.completed/ended:null;
}
function rateColor(rate) {
 if(rate===null)return 'var(--neutral)';
 const t=Math.max(0,Math.min(1,rate)),colors=[[239,68,68],[250,204,21],[34,197,94]],i=t<.5?0:1,f=i===0?t*2:(t-.5)*2;
 return `rgb(${colors[i].map((v,j)=>Math.round(v+(colors[i+1][j]-v)*f)).join(', ')})`;
}
function rateTone(rate) { return rate===null?'':rate>=.9?'good':rate>=.5?'warning':'bad'; }
function rateLabel(rate) { return rate===null?'—':(rate*100).toFixed(1)+'%'; }
function statusWindow(snapshot,credentialID) {
 const d=dashboardData(snapshot),end=Math.floor(d.now/STATUS_INTERVAL)*STATUS_INTERVAL+STATUS_INTERVAL,start=end-STATUS_BLOCKS*STATUS_INTERVAL;
 const scoped=snapshot.logs.analytics?.credential_scoped===true;
 const source=credentialID===undefined?d.rows:scoped?d.rows:legacyBuckets(snapshot.logs);
 const rows=source.filter(r=>(credentialID===undefined||r.credential_id===credentialID)&&Date.parse(r.started_at)<=d.now);
 const bins=series(rows,start,end,STATUS_INTERVAL),total=aggregate(bins);
 return {bins,total,rate:completionRate(total),partial:credentialID===undefined?d.partial:!scoped,now:d.now};
}
function statusBlocks(window) {
 return `<div class="status-blocks" role="group" aria-label="最近 20 个时段的请求完成率">${window.bins.map((b,i)=>{
  const rate=completionRate(b),text=clockLabel(b.time)+'–'+clockLabel(b.time+STATUS_INTERVAL)+(b.time+STATUS_INTERVAL>window.now?'（当前时段）':'')+'\n'+count(b.completed)+' 完成 · '+count(b.failed)+' 失败 · '+count(b.incomplete)+' 未完整\n'+(rate===null?'无已结束请求':'完成率 '+rateLabel(rate))+(window.partial?'\n仅覆盖保留日志':'');
  return `<button type="button" class="status-block ${rate===null?'idle':''}" style="--status-color:${rateColor(rate)}" tabindex="${i===STATUS_BLOCKS-1?0:-1}" data-tip="${esc(text)}" aria-label="${esc(text)}"></button>`;
 }).join('')}</div>`;
}
function renderMonitor() {
 const window=statusWindow(overview),active=overview.logs.active,enabled=credentials.filter(c=>!c.disabled).length;
 const tone=rateTone(window.rate),label=window.rate===null?'等待请求':tone==='good'?'近期请求正常':tone==='warning'?'近期请求需关注':'近期请求异常';
 $('monitor-orb').className='monitor-status-icon '+tone;
 setMarkup('monitor-orb',icon(window.rate===null?'activity':tone==='good'?'check':'activity'));
 $('monitor-label').textContent=!credentials.length?'等待添加 API Key':!enabled?'所有凭证已停用':label;
 $('monitor-caption').textContent=`${enabled} 个已启用 Key · ${active.length} 个进行中${window.rate===null?'':' · 完成率 '+rateLabel(window.rate)}`;
 const counts=[active.filter(r=>r.status==='streaming').length,active.filter(r=>['queued','waiting'].includes(r.status)).length,active.filter(r=>r.status==='retrying').length,window.total.failed+window.total.incomplete];
 setMarkup('signal-grid',counts.map((n,i)=>`<div class="signal"><i class="signal-lamp ${['blue','neutral','amber','red'][i]}"></i><span>${['输出中','等待上游','内部重试','近期异常'][i]}</span><strong>${count(n)}</strong></div>`).join(''));
 setMarkup('monitor-keys',credentials.length?credentials.map(c=>{
  const w=statusWindow(overview,c.id);
  return `<div class="key-monitor"><div class="key-identity"><span class="key-icon">${icon('tokens')}</span><div><strong title="${esc(c.label)}">${esc(c.label)}</strong><small>${c.disabled?'已停用':'已启用'} · ${c.active_requests>=0?c.active_requests:'—'} / ${c.max_concurrent_requests??c.max_concurrent_sessions??2} 请求 · 排队 ${c.queued_requests>=0?c.queued_requests:'—'}${w.partial?' · 保留日志':''}</small></div></div>${statusBlocks(w)}<span class="key-rate ${rateTone(w.rate)}">${rateLabel(w.rate)}</span></div>`;
 }).join(''):'<div class="monitor-empty">暂无 API Key<button class="text-button" data-open-tab="credentials">添加 API Key →</button></div>');
}
function logRecords(snapshot) {
 return [...snapshot.logs.active,...snapshot.logs.recent].sort((a,b)=>Date.parse(b.started_at)-Date.parse(a.started_at)||String(b.id).localeCompare(String(a.id),undefined,{numeric:true}));
}
function filterLogRecords(rows,{query='',status='all',key='all'}={}) {
 const text=query.trim().toLowerCase();
 return rows.filter(r=>(key==='all'||r.credential_id===key)&&(status==='all'||status==='active'&&['queued','waiting','streaming','retrying'].includes(r.status)||status==='recovered'&&r.status==='completed'&&r.attempts>1||r.status===status)&&[r.id,r.model,labelFor(r.credential_id),stateNames[r.status],r.error_code,...(r.retry_reasons||[])].join(' ').toLowerCase().includes(text));
}
function summaryCard(label,value,note,iconName,tone='blue') {
 return `<article class="card summary-card"><span class="summary-icon ${tone}">${icon(iconName)}</span><div><span>${label}</span><strong>${value}</strong><small>${note}</small></div></article>`;
}
function renderLogs() {
 if(!overview)return;
 const all=logRecords(overview),finished=all.filter(r=>!['queued','waiting','streaming','retrying'].includes(r.status));
 const selected=$('log-key').value;
 const options=[...new Map([...credentials.map(c=>[c.id,c.label]),...all.filter(r=>!credentials.some(c=>c.id===r.credential_id)).map(r=>[r.credential_id,'已移除的 API Key'])]).entries()];
 setMarkup('log-key','<option value="all">全部 API Key</option>'+options.map(([id,label])=>`<option value="${esc(id)}">${esc(label)}</option>`).join(''));
 $('log-key').value=options.some(([id])=>id===selected)?selected:'all';
 const rows=filterLogRecords(all,{query:$('log-search').value,status:$('log-status').value,key:$('log-key').value});
 const size=Number($('log-page-size').value),pages=Math.max(1,Math.ceil(rows.length/size));logPage=Math.min(logPage,pages);
 setMarkup('log-summary',summaryCard('保留记录',count(all.length),'进行中 + 最近结束','requests')+summaryCard('进行中',count(overview.logs.active.length),'等待 / 输出 / 重试','activity')+summaryCard('异常结果',count(finished.filter(r=>['failed','incomplete'].includes(r.status)).length),'失败与输出未完整','stop','red')+summaryCard('重试后完成',count(finished.filter(r=>r.status==='completed'&&r.attempts>1).length),'最终已正常完成','recovery','green'));
 $('log-result-count').textContent=`筛选结果 ${count(rows.length)} 条 / 共 ${count(all.length)} 条`;
 $('log-empty').hidden=rows.length>0;
 $('log-empty').querySelector('strong').textContent=all.length?'没有匹配的请求':'暂无请求记录';
 $('log-empty').querySelector('p').textContent=all.length?'调整筛选条件或重置后查看。':'通过 AR 插件发起请求后，日志会显示在这里。';
 setMarkup('log-rows',rows.slice((logPage-1)*size,logPage*size).map(r=>`<tr><td class="request-time">${esc(timestamp(r.started_at))}<small class="mono">#${esc(r.id)}</small></td><td class="request-model"><strong class="mono">${esc(r.model||'—')}</strong><small>${esc(labelFor(r.credential_id))}</small></td><td><span class="pill ${statusClass(r.status)}"><i></i>${esc(stateNames[r.status]||r.status)}</span>${r.error_code?`<small class="error-code">${esc(r.error_code)}</small>`:r.status==='completed'&&r.attempts>1?'<small class="recovery-note">内部恢复成功</small>':''}</td><td><span class="attempt-value">${count(r.attempts)} <small>次</small></span></td><td class="mono">${duration(r.first_output_ms)}</td><td class="mono">${duration(r.duration_ms)}</td><td class="right"><button class="text-button" data-request="${esc(r.id)}">详情 →</button></td></tr>`).join(''));
 $('log-page-label').textContent=`${logPage} / ${pages}`;$('log-prev').disabled=logPage<=1;$('log-next').disabled=logPage>=pages;
 if($('request-detail').open&&detailRecord){detailRecord=all.find(r=>r.id===detailRecord.id)||detailRecord;renderRequestDetail();}
}
function renderRequestDetail() {
 const r=detailRecord;if(!r)return;
 $('detail-request-id').textContent='#'+r.id;
 const fields=[['模型',r.model],['API Key',labelFor(r.credential_id)],['开始时间',timestamp(r.started_at)],['首段延迟',duration(r.first_output_ms)],['总耗时',duration(r.duration_ms)],['并发排队',duration(r.queue_wait_ms||0)],['上游尝试',count(r.attempts)+' 次']];
 setMarkup('request-detail-body',`<div class="detail-status"><span class="pill ${statusClass(r.status)}">${esc(stateNames[r.status]||r.status)}</span>${r.status==='completed'&&r.attempts>1?'<span class="recovery-note">内部重试后完成</span>':''}</div><dl class="request-facts">${fields.map(([k,v])=>`<div><dt>${k}</dt><dd>${esc(v||'—')}</dd></div>`).join('')}</dl><section class="detail-section"><h3>内部恢复记录</h3>${r.retry_reasons?.length?`<ol class="retry-timeline">${r.retry_reasons.map((reason,i)=>`<li><span>${i+1}</span><div><strong>${esc(reasonNames[reason]||reason)}</strong><small>第 ${i+1} 次额外尝试前的重试原因</small></div></li>`).join('')}</ol>`:'<p>未记录内部重试。</p>'}</section>${r.error_code?`<section class="detail-section"><h3>错误码</h3><code class="error-detail">${esc(r.error_code)}</code></section>`:''}`);
}
function renderHealth() {
 if(!overview)return;
 const enabled=credentials.filter(c=>!c.disabled).length,checked=credentials.filter(c=>overview.health?.[c.id]).length;
 const active=overview.logs.active.length,unhealthy=credentials.filter(c=>!c.disabled&&statusWindow(overview,c.id).rate!==null&&statusWindow(overview,c.id).rate<.9).length;
 setMarkup('health-summary',summaryCard('已启用 Key',count(enabled),`共 ${credentials.length} 个 API Key`,'tokens')+summaryCard('进行中请求',count(active),'含等待与内部重试','activity')+summaryCard('近期需关注',count(unhealthy),'已启用 Key 完成率低于 90%','stop','amber')+summaryCard('已手动检测',count(checked),'本次插件运行内','check','green'));
 setMarkup('health-cards',credentials.length?credentials.map(c=>{
  const w=statusWindow(overview,c.id),check=overview.health?.[c.id],pending=pendingChecks.has(c.auth_index);
  return `<article class="card connection-card"><div class="connection-heading"><div class="key-identity"><span class="key-icon">${icon('tokens')}</span><div><strong title="${esc(c.label)}">${esc(c.label)}</strong><small class="mono">${esc(c.masked_key)}</small></div></div><span class="pill ${c.disabled?'':'good'}">${c.disabled?'已停用':'已启用'}</span></div><div class="connection-performance"><span>近期请求完成率</span><strong class="${rateTone(w.rate)}">${rateLabel(w.rate)}</strong></div>${statusBlocks(w)}<div class="connection-counts"><span>${count(w.total.completed)} 完成</span><span>${count(w.total.failed)} 失败</span><span>${count(w.total.incomplete)} 未完整</span></div><div class="connection-check"><div><span>模型列表连接检测</span><strong class="${check?(check.ok?'text-good':'text-bad'):'muted'}">${check?(check.ok?'检测通过':'检测失败'):'尚未检测'}</strong></div><p>${check?esc(timestamp(check.checked_at))+' · '+duration(check.latency_ms)+(check.http_status?' · HTTP '+Number(check.http_status):''):'手动检测后显示结果与响应耗时'}</p>${check?`<p class="check-message">${esc(check.message)}</p>`:''}</div><div class="connection-actions"><span>${w.partial?'仅覆盖保留日志':'最近 20 个时段 · 每格 10 分钟'}</span><button data-health="${esc(c.auth_index)}" ${pending?'disabled':''}>${pending?'检测中…':'检测连接'}</button></div></article>`;
 }).join(''):'<div class="card visual-empty"><strong>暂无 API Key</strong><p>添加凭证后，可查看连接状态与请求表现。</p><button class="text-button" data-open-tab="credentials">添加 API Key →</button></div>');
 $('policy-empty').hidden=credentials.length>0;
 setMarkup('policy-rows',credentials.map(c=>`<tr><td><strong>${esc(c.label)}</strong><small>${c.disabled?'已停用':'已启用'}</small></td><td class="mono">${c.active_requests>=0?c.active_requests:'—'} / ${c.max_concurrent_requests??c.max_concurrent_sessions??2}</td><td class="mono">${c.queued_requests>=0?c.queued_requests:'—'}</td><td>${c.max_retries??4} 次</td><td>${c.retry_budget_seconds??180} 秒</td><td>${esc(c.model_prefix||'无前缀')}</td><td class="right"><button class="text-button" data-policy="${esc(c.auth_index)}">编辑配置 →</button></td></tr>`).join(''));
}
async function runHealthCheck(index) {
 if(pendingChecks.has(index))return;const epoch=generation;
 pendingChecks.add(index);renderHealth();
 try {const result=await api('/health',{method:'POST',body:{auth_index:index}});if(epoch!==generation)return;await refresh();notify(result.message,!result.ok);}
 catch(error){if(epoch===generation)notify(error.message,true);}
 finally{pendingChecks.delete(index);if(epoch===generation)renderHealth();}
}
function initMonitoring() {
 for(const [id,event] of [['log-search','input'],['log-status','change'],['log-key','change'],['log-page-size','change']])$(id).addEventListener(event,()=>{logPage=1;renderLogs();});
 $('reset-log-filters').addEventListener('click',()=>{$('log-search').value='';$('log-status').value='all';$('log-key').value='all';logPage=1;renderLogs();});
 $('log-prev').addEventListener('click',()=>{logPage--;renderLogs();});$('log-next').addEventListener('click',()=>{logPage++;renderLogs();});
 $('log-rows').addEventListener('click',e=>{const b=e.target.closest('[data-request]');if(!b)return;detailRecord=logRecords(overview).find(r=>r.id===b.dataset.request);if(detailRecord){renderRequestDetail();$('request-detail').showModal();}});
 for(const id of ['close-request-detail','dismiss-request-detail'])$(id).addEventListener('click',()=>$('request-detail').close());
 $('request-detail').addEventListener('close',()=>{detailRecord=null;});
 $('health-cards').addEventListener('click',e=>{const b=e.target.closest('[data-health]');if(b)runHealthCheck(b.dataset.health);});
 $('policy-rows').addEventListener('click',e=>{const b=e.target.closest('[data-policy]');if(b){const c=credentials.find(c=>c.auth_index===b.dataset.policy);if(c){openEditor(c);switchEditorTab('advanced');}}});
 document.addEventListener('keydown',e=>{const b=e.target.closest('.status-block');if(!b||!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const all=[...b.parentElement.children],i=all.indexOf(b),next=e.key==='Home'?0:e.key==='End'?all.length-1:Math.max(0,Math.min(all.length-1,i+(e.key==='ArrowRight'?1:-1)));all.forEach((x,j)=>x.tabIndex=j===next?0:-1);all[next].focus();});
}
