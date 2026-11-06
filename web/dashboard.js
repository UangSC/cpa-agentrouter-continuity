"use strict";
const PRICE_STORE = "cpa-agentrouter-continuity.prices.v1";
const HOUR = 3600000, DAY = 24 * HOUR;
const palette = ["#4388f5", "#8b74df", "#2fbba1", "#edac4a", "#e47e94", "#52b2d3"];
let chartRange = "today", rankMode = "requests", modelPrices = Object.create(null), priceModels = [];
const iconPaths = {
 activity:'M3 12h4l3-8 4 16 3-8h4', requests:'M4 5h16v14H4z M4 13h5l2 3h2l2-3h5',
 check:'M20 11v1a8 8 0 1 1-5-7 M9 11l3 3 9-9', tokens:'m12 3 9 5-9 5-9-5z M3 12l9 5 9-5 M3 16l9 5 9-5',
 recovery:'M3 11a9 9 0 1 1 2 7 M3 4v7h7 M12 7v5l3 2', speed:'M4 17a9 9 0 1 1 16 0 M12 13l5-5 M7 17h10',
 clock:'M12 8v5l3 2 M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0', trend:'M3 4v16h18 M6 15l4-5 4 3 6-7',
 pie:'M12 3v9h9 M9 3.5A9 9 0 1 0 20.5 15 M15 3.5v5.5h5.5', grid:'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',
 rank:'M4 14h4v7H4z M10 8h4v13h-4z M16 3h4v18h-4z'
};
function icon(name) { return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${iconPaths[name] || iconPaths.activity}"/></svg>`; }
function compactNumber(n) { return new Intl.NumberFormat("en", {notation:"compact", maximumFractionDigits:1}).format(n || 0); }
function count(n) { return Math.round(n || 0).toLocaleString("zh-CN"); }
function beijingStart(now) { return Math.floor((now + 8 * HOUR) / DAY) * DAY - 8 * HOUR; }
function clockLabel(ms) { return new Date(ms).toLocaleTimeString("zh-CN", {timeZone:"Asia/Shanghai",hour:"2-digit",minute:"2-digit",hour12:false}); }
function aggregate(rows) {
 const total = {requests:0,completed:0,failed:0,canceled:0,incomplete:0,recovered:0,retries:0,usage_count:0,first_output_count:0,first_output_total_ms:0,tokens:{input_tokens:0,output_tokens:0,cached_tokens:0}};
 for (const r of rows) { for (const key of Object.keys(total)) { if (key !== "tokens") total[key] += Number(r[key]) || 0; } for (const key of Object.keys(total.tokens)) total.tokens[key] += Number(r.tokens?.[key]) || 0; }
 return total;
}
function legacyBuckets(logs) {
 return [...logs.recent,...logs.active].map(r=>({started_at:r.started_at,credential_id:r.credential_id,model:r.model,requests:1,completed:+(r.status==="completed"),failed:+(r.status==="failed"),incomplete:+(r.status==="incomplete"),canceled:+(r.status==="canceled"),recovered:+(r.status==="completed" && r.attempts>1),retries:Math.max(0,r.attempts-1),first_output_count:+(r.first_output_ms>=0),first_output_total_ms:Math.max(0,r.first_output_ms)}));
}
function dashboardData(snapshot, now = Date.now()) {
 const source = snapshot.logs.analytics;
 const serverNow = Date.parse(source?.now);
 if (Number.isFinite(serverNow)) now = serverNow;
 const start = beijingStart(now), rows = source?.buckets || legacyBuckets(snapshot.logs);
 const today = rows.filter(r=>Date.parse(r.started_at)>=start && Date.parse(r.started_at)<=now);
 const models = [...new Set(today.map(r=>r.model))].map(model=>({model,...aggregate(today.filter(r=>r.model===model))}));
 return {now,start,rows,today,models,total:aggregate(today),partial:!source,started:Date.parse(source?.started_at)};
}
function series(rows,start,end,size) {
 const bins = Array.from({length:Math.max(1,Math.ceil((end-start)/size))},(_,i)=>({time:start+i*size,rows:[]}));
 for (const row of rows) { const i=Math.floor((Date.parse(row.started_at)-start)/size); if(i>=0&&i<bins.length) bins[i].rows.push(row); }
 return bins.map(b=>({time:b.time,...aggregate(b.rows)}));
}
function tokenTotal(row) { return row.tokens.input_tokens + row.tokens.output_tokens; }
function modelCost(row,prices) {
 const p=prices[row.model];
 if (!p || !row.usage_count || ![p.input,p.cached,p.output].every(x=>typeof x==="number"&&Number.isFinite(x)&&x>=0)) return null;
 return ((row.tokens.input_tokens-row.tokens.cached_tokens)*p.input+row.tokens.cached_tokens*p.cached+row.tokens.output_tokens*p.output)/1000000;
}
function money(value) { return value === null ? "未配置" : "$" + value.toLocaleString("en-US",{minimumFractionDigits:2,maximumFractionDigits:4}); }
function spark(values,color) {
 const max=Math.max(1,...values), points=values.map((v,i)=>`${(i/Math.max(1,values.length-1)*160).toFixed(1)},${(36-v/max*30).toFixed(1)}`).join(" ");
 return `<svg viewBox="0 0 160 40" preserveAspectRatio="none" aria-hidden="true"><polygon points="0,40 ${points} 160,40" fill="${color}" opacity=".06"/><polyline points="${points}" fill="none" stroke="${color}" stroke-width="1.8" opacity=".5"/></svg>`;
}
function renderDashboard() {
 if(!overview)return;
 const d=dashboardData(overview),t=d.total,ended=t.completed+t.failed+t.incomplete;
 $("version").textContent="v"+overview.version;$("footer-version").textContent="v"+overview.version;
 $("key-count").textContent=credentials.length;
 $("updated").textContent="更新于 "+clockLabel(d.now);
 $("dashboard-date").textContent=new Date(d.now).toLocaleDateString("zh-CN",{timeZone:"Asia/Shanghai",year:"numeric",month:"long",day:"numeric",weekday:"long"});
 $("data-scope").textContent=d.partial?"最近 200 条记录中的今日数据":"北京时间 00:00 至今 · 本次运行";
 $("retention-note").textContent=d.partial?"当前服务未提供全天聚合，统计仅覆盖保留记录":"聚合保留 24 小时 · 本次运行始于 "+(Number.isFinite(d.started)?clockLabel(d.started):"—")+" · 重载后重新统计";
 $("metric-requests").textContent=count(t.requests);$("metric-failures").textContent=count(t.failed)+" 次失败 · "+count(t.canceled)+" 次取消";
 $("metric-rate").textContent=ended?(t.completed/ended*100).toFixed(1)+"%":"—";
 $("metric-tokens").textContent=t.usage_count?compactNumber(tokenTotal(t)):"—";
 $("metric-token-note").textContent=t.usage_count?count(t.usage_count)+" 次请求返回用量":"等待上游返回用量";
 $("metric-recovered").textContent=count(t.recovered);$("metric-retries").textContent=count(t.retries)+" 次额外尝试";
 const rolling=aggregate(d.rows.filter(r=>Date.parse(r.started_at)>=Math.floor(d.now/300000)*300000-1500000));
 $("metric-rpm").innerHTML=(rolling.requests/30).toFixed(1)+"<em>RPM</em>";
 $("metric-latency").textContent=t.first_output_count?duration(t.first_output_total_ms/t.first_output_count):"—";
 $("metric-active").textContent=overview.logs.active.length;
 const hourly=series(d.today,d.start,Math.max(d.start+HOUR,Math.ceil(d.now/HOUR)*HOUR),HOUR);
 for(const [id,key,color] of [["requests","requests",palette[0]],["rate","completed",palette[2]],["tokens","tokens",palette[1]],["recovered","recovered",palette[3]],["rpm","requests",palette[5]],["latency","latency",palette[4]]])setMarkup("spark-"+id,spark(hourly.map(x=>key==="tokens"?tokenTotal(x):key==="latency"?(x.first_output_count?x.first_output_total_ms/x.first_output_count:0):x[key]),color));
 renderMonitor(d);renderTraffic(d);renderDistribution(t);renderTimeline(d);renderRanking(d);
}
function renderTraffic(d) {
 const mode=$("traffic-mode").value,tokenMode=mode==="tokens";
 const size=chartRange==="1h"?300000:chartRange==="6h"?600000:HOUR;
 const end=Math.floor(d.now/size)*size+size,start=chartRange==="today"?d.start:end-(chartRange==="1h"?HOUR:6*HOUR);
 const bins=series(d.rows,start,end,size),a=bins.map(b=>tokenMode?b.tokens.input_tokens:b.requests),b=bins.map(b=>tokenMode?b.tokens.output_tokens:b.retries);
 const labelA=tokenMode?"输入 Token（含缓存）":"请求数",labelB=tokenMode?"输出 Token":"额外尝试";
 setMarkup("traffic-legend",`<span><i class="legend-dot blue"></i>${labelA}</span><span><i class="legend-dot ${tokenMode?"violet":"amber"}"></i>${labelB}</span>`);
 const colorB=tokenMode?palette[1]:palette[3],max=Math.max(4,...a,...b),top=Math.ceil(max/4)*4;
 const W=Math.max(320,$("traffic-chart").clientWidth||880),H=W<600?190:224,left=36,right=16,padTop=20,bottom=30,plotW=W-left-right,plotH=H-padTop-bottom;
 const x=i=>left+(i+.5)/bins.length*plotW,y=v=>padTop+(1-v/top)*plotH;
 const points=a.map((v,i)=>`${x(i).toFixed(2)},${y(v).toFixed(2)}`).join(" ");
 const grid=Array.from({length:5},(_,i)=>{const v=top*(4-i)/4;return `<line x1="${left}" y1="${y(v)}" x2="${W-right}" y2="${y(v)}" class="chart-gridline"/><text x="${left-10}" y="${y(v)+4}" text-anchor="end">${compactNumber(v)}</text>`;}).join("");
 const labels=bins.map((v,i)=>i%Math.max(1,Math.ceil(bins.length/8))===0||i===bins.length-1?`<text x="${x(i)}" y="${H-5}" text-anchor="middle">${clockLabel(v.time)}</text>`:"").join("");
 const bars=b.map((v,i)=>`<rect x="${x(i)-Math.min(9,plotW/bins.length*.2)}" y="${y(v)}" width="${Math.min(18,plotW/bins.length*.4)}" height="${plotH+padTop-y(v)}" fill="${colorB}" opacity=".65" rx="3"/>`).join("");
 const overlay=bins.map((v,i)=>`<rect class="chart-hit" tabindex="0" x="${left+i/bins.length*plotW}" y="${padTop}" width="${plotW/bins.length}" height="${plotH}" data-tip="${esc(clockLabel(v.time)+"–"+clockLabel(v.time+size)+"\n"+labelA+"  "+count(a[i])+"\n"+labelB+"  "+count(b[i]))}"/>`).join("");
 const total=aggregate(bins),hasData=tokenMode?total.usage_count>0:total.requests>0;
 setMarkup("traffic-chart",`<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc((tokenMode?"Token":"请求")+"趋势，"+bins.length+" 个时间段，"+(hasData?"有数据":"暂无数据"))}"><defs><linearGradient id="traffic-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${palette[0]}" stop-opacity=".22"/><stop offset="1" stop-color="${palette[0]}" stop-opacity=".01"/></linearGradient></defs>${grid}${bars}<polygon points="${x(0)},${y(0)} ${points} ${x(bins.length-1)},${y(0)}" fill="url(#traffic-fill)"/><polyline points="${points}" fill="none" stroke="${palette[0]}" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>${a.map((v,i)=>v?`<circle cx="${x(i)}" cy="${y(v)}" r="3" fill="${palette[0]}" stroke="var(--surface)" stroke-width="1.5"/>`:"").join("")}${labels}${overlay}</svg>${hasData?"":'<div class="chart-empty">'+(tokenMode?"此时段暂无上游用量数据":"此时段暂无请求")+'</div>'}`);
 $("traffic-summary").textContent=(chartRange==="today"?"今日":chartRange==="1h"?"近 1 小时":"近 6 小时")+" · "+(tokenMode?compactNumber(tokenTotal(total))+" Token":count(total.requests)+" 次请求 · "+count(total.retries)+" 次额外尝试");
}
function renderDistribution(t) {
 const values=[t.completed,t.failed,t.incomplete,t.canceled],colors=[palette[2],"#e57682",palette[3],"#a1afc1"],labels=["已完成","失败","未完整","已取消"],total=values.reduce((a,b)=>a+b,0);
 let offset=0;
 const arcs=values.map((v,i)=>{const length=total?v/total*100:0;const svg=`<circle cx="90" cy="90" r="67" pathLength="100" fill="none" stroke="${colors[i]}" stroke-width="15" stroke-dasharray="${length} ${100-length}" stroke-dashoffset="${-offset}" transform="rotate(-90 90 90)"/>`;offset+=length;return svg;}).join("");
 setMarkup("result-donut",`<svg viewBox="0 0 180 180" aria-hidden="true"><circle cx="90" cy="90" r="67" fill="none" stroke="var(--line)" stroke-width="15"/>${arcs}</svg><div><strong>${count(total)}</strong><span>已结束请求</span></div>`);
 setMarkup("result-legend",values.map((v,i)=>`<div><span><i style="background:${colors[i]}"></i>${labels[i]}</span><strong>${count(v)}</strong><small>${total?(v/total*100).toFixed(1):"0"}%</small></div>`).join(""));
}
function renderTimeline(d) {
 const bins=series(d.today,d.start,d.start+DAY,600000);
 let rows="";
 for(let row=0;row<6;row++) { rows+=`<span class="heat-hour">${String(row*4).padStart(2,"0")}:00</span>`;for(let col=0;col<24;col++) {const b=bins[row*24+col],future=b.time>d.now,unknown=Number.isFinite(d.started)&&b.time+600000<=d.started;
  const tone=future?"future":!b.requests?"neutral":b.failed?"red":b.retries||b.incomplete?"amber":"green";
  const text=clockLabel(b.time)+"–"+clockLabel(b.time+600000)+"\n"+(future?"尚未到达":unknown&&!b.requests?"本次运行尚未开始，暂无记录":count(b.requests)+" 次请求 · "+count(b.completed)+" 完成\n"+count(b.failed)+" 失败 · "+count(b.retries)+" 次额外尝试");
  rows+=`<span tabindex="0" class="heat-cell ${tone}" data-tip="${esc(text)}" aria-label="${esc(text)}"></span>`;
 }}
 setMarkup("request-heatmap",rows);
 const ended=d.total.completed+d.total.failed+d.total.incomplete;
 $("timeline-rate").textContent=ended?(d.total.completed/ended*100).toFixed(1)+"% 完成":"等待数据";
 const peak=[...bins].sort((a,b)=>b.requests-a.requests)[0];
 setMarkup("timeline-insight",`<span class="insight-icon">${icon("activity")}</span><div><strong>${peak.requests?"最活跃时段 "+clockLabel(peak.time)+"–"+clockLabel(peak.time+600000):"等待第一条请求"}</strong><p>${peak.requests?count(peak.requests)+" 次请求 · 今日 "+count(d.total.recovered)+" 次请求在内部重试后完成":"实际请求到来后，对应时间格会自动点亮。"}</p></div>`);
}
function renderRanking(d) {
 const prices=Object.fromEntries(d.models.map(m=>[m.model,resolvedPrice(m.model)]));
 const models=d.models.map(m=>({...m,cost:modelCost(m,prices)}));
 const value=m=>rankMode==="cost"?m.cost:rankMode==="tokens"?(m.usage_count?tokenTotal(m):null):m.requests;
 models.sort((a,b)=>(value(b)??-1)-(value(a)??-1)||a.model.localeCompare(b.model));
 const known=models.filter(m=>value(m)!==null),total=known.reduce((sum,m)=>sum+value(m),0),max=Math.max(1,...known.map(value));
 $("rank-caption").textContent=rankMode==="cost"?"按已配置单价估算 · USD":rankMode==="tokens"?"按已返回的 Token 用量排序":"按今日请求量排序";
 $("rank-total").textContent=rankMode==="cost"?(known.length?"已计价 "+money(total):"待配置单价"):rankMode==="tokens"?compactNumber(total)+" Token":count(total)+" 次";
 setMarkup("model-ranking",models.length?models.slice(0,8).map((m,i)=>{const v=value(m),color=palette[i%palette.length],unknown=rankMode==="cost"?(m.usage_count?"未配置单价":"未返回用量"):"未返回用量";return `<div class="rank-row"><span class="rank-index">${String(i+1).padStart(2,"0")}</span><div class="rank-model"><div><strong title="${esc(m.model)}">${esc(m.model)}</strong><span>${v===null?unknown:rankMode==="cost"?money(v):compactNumber(v)}</span></div><div class="rank-track"><span style="width:${v===null?0:v/max*100}%;background:${color}"></span></div><small>${count(m.requests)} 次请求${m.usage_count?" · "+compactNumber(tokenTotal(m))+" Token":" · 等待用量"}${rankMode==="cost"&&m.usage_count<m.completed+m.incomplete?" · 部分用量":""}</small></div></div>`;}).join(""):'<div class="visual-empty">'+icon("rank")+'<strong>暂无模型数据</strong><p>请求到来后，自动汇总模型用量。</p></div>');
 $("rank-note").textContent=rankMode==="cost"?"按默认分组或手动单价估算；缓存默认不打折，阶梯与失败尝试费用未计入，以账单为准。":"费用按渠道默认值或手动单价估算；缓存默认不打折，未返回的用量不计入 Token。";
}
function switchMainTab(name) {
 document.querySelectorAll('[data-tab]').forEach(b=>{const selected=b.dataset.tab===name;b.setAttribute("aria-selected",String(selected));b.tabIndex=selected?0:-1;$(b.dataset.tab+"-panel").hidden=!selected;});
 $("chart-tooltip").hidden=true;if(name==="dashboard"&&overview)renderDashboard();
}
function initDashboard() {
 initPricing();
 document.querySelectorAll('[data-icon]').forEach(e=>e.innerHTML=icon(e.dataset.icon));
 document.querySelectorAll('[data-range]').forEach(b=>b.addEventListener("click",()=>{chartRange=b.dataset.range;document.querySelectorAll('[data-range]').forEach(x=>x.setAttribute("aria-pressed",String(x===b)));renderDashboard();}));
 document.querySelectorAll('[data-rank]').forEach(b=>b.addEventListener("click",()=>{rankMode=b.dataset.rank;document.querySelectorAll('[data-rank]').forEach(x=>x.setAttribute("aria-pressed",String(x===b)));renderDashboard();}));
 $("traffic-mode").addEventListener("change",()=>renderDashboard());
 document.addEventListener("click",e=>{const b=e.target.closest('[data-open-tab]');if(b)switchMainTab(b.dataset.openTab);});
 document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener("keydown",e=>{if(!["ArrowLeft","ArrowRight","Home","End"].includes(e.key))return;e.preventDefault();const tabs=[...document.querySelectorAll('[data-tab]')],i=tabs.indexOf(b),next=tabs[e.key==="Home"?0:e.key==="End"?tabs.length-1:(i+(e.key==="ArrowRight"?1:-1)+tabs.length)%tabs.length];switchMainTab(next.dataset.tab);next.focus();}));
 let resizeTimer;window.addEventListener("resize",()=>{clearTimeout(resizeTimer);resizeTimer=setTimeout(()=>{if(overview&&!$("dashboard-panel").hidden)renderDashboard();},80);});
 const showTip=e=>{const el=e.target.closest?.('[data-tip]');if(!el)return;const tip=$("chart-tooltip"),rect=el.getBoundingClientRect();tip.textContent=el.dataset.tip;tip.hidden=false;const x=e.clientX||rect.left+rect.width/2,y=e.clientY||rect.bottom;tip.style.left=Math.max(8,Math.min(x+12,window.innerWidth-tip.offsetWidth-12))+"px";tip.style.top=Math.max(8,Math.min(y+14,window.innerHeight-tip.offsetHeight-12))+"px";};
 document.addEventListener("pointerover",showTip);document.addEventListener("focusin",showTip);document.addEventListener("pointerout",e=>{if(e.target.closest?.('[data-tip]'))$("chart-tooltip").hidden=true;});document.addEventListener("focusout",()=>$("chart-tooltip").hidden=true);document.addEventListener("keydown",e=>{if(e.key==="Escape")$("chart-tooltip").hidden=true;});window.addEventListener("scroll",()=>$("chart-tooltip").hidden=true,true);
}
