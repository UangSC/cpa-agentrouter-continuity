"use strict";
const BASE="/v0/management/plugins/cpa-agentrouter-continuity";
const CONNECTION_STORE="cpa-agentrouter-continuity.connection.v1";
const $=id=>document.getElementById(id);
const esc=value=>String(value??"").replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const stateNames={waiting:"等待上游",retrying:"退避重试",streaming:"输出中",completed:"已完成",incomplete:"输出未完整",failed:"失败",canceled:"已取消"};
const reasonNames={rate:"临时限流",transport:"上游断流 / 网关",history:"历史验证失败"};
let editorEpoch=0,modelChoices=[];
let managementKey="",credentials=[],overview=null,timer=0,refreshing=false,editing=null,mutationPending=false,generation=0;

// Read the host panel's persisted connection/theme format without changing it.
function decodePanelValue(raw,host,userAgent){
 if(!raw)return null;
 try{
  const match=/^enc::v([12])::/.exec(raw);
  if(!match)return JSON.parse(raw);
  const packed=Uint8Array.from(atob(raw.substring(match[0].length)),x=>x.charCodeAt(0));
  const scopes=match[1]==="1"?[host+"|"+userAgent]:[host,"v2|"+host];
  for(const scope of scopes){
   const mask=new TextEncoder().encode("cli-proxy-api-webui::secure-storage|"+scope);
   const unpacked=new Uint8Array(packed.length);
   for(let i=0;i<packed.length;i++)unpacked[i]=packed[i]^mask[i%mask.length];
   try{return JSON.parse(new TextDecoder("utf-8",{fatal:true}).decode(unpacked))}catch{}
  }
 }catch{}
 return null;
}
function panelSetting(name){try{return decodePanelValue(localStorage.getItem(name),location.host,navigator.userAgent)}catch{return null}}
function savedKey(){try{const saved=JSON.parse(localStorage.getItem(CONNECTION_STORE)||"null");if(typeof saved?.key==="string")return saved.key}catch{};const key=panelSetting("cli-proxy-auth")?.state?.managementKey;return typeof key==="string"?key.trim():""}
function applyTheme(){
 let theme=panelSetting("cli-proxy-theme")?.state?.theme;
 try{if(window.parent!==window){const parent=window.parent.document.documentElement;theme=parent.dataset.theme||theme;if(parent.hasAttribute("data-cpamp-plugin-host")||window.parent.document.title.includes("CPA Manager Plus"))document.documentElement.setAttribute("data-cpamp-plugin-host","true")}}catch{}
 if(!["light","white","dark"].includes(theme))theme=matchMedia("(prefers-color-scheme: dark)").matches?"dark":"white";
 if(document.documentElement.dataset.theme!==theme)document.documentElement.dataset.theme=theme;
}
async function api(endpoint,{method="GET",body,key=managementKey}={}){
 const headers={Authorization:"Bearer "+key};const options={method,headers,cache:"no-store",signal:AbortSignal.timeout(45000)};
 if(body!==undefined){headers["Content-Type"]="application/json";options.body=JSON.stringify(body)}
 let response;try{response=await fetch(BASE+endpoint,options)}catch{throw new Error("无法连接 CPA，请检查网络后重试。")}
 let data;try{data=await response.json()}catch{throw new Error("CPA 返回了无效数据。")}
 if(!response.ok){const message=response.status===401||response.status===403?"管理密钥无效或无权限，请重新连接。":data.error||"请求失败";const error=new Error(typeof message==="string"?message:"操作失败");error.status=response.status;throw error}
 return data;
}
function notify(message,error=false){$("notice").textContent=message;$("notice").className="notice"+(error?" error":"");$("notice").hidden=false}
function disconnect(){generation++;managementKey="";credentials=[];overview=null;clearTimeout(timer);timer=0;$("app").hidden=true;$("login").hidden=false;$("refresh").hidden=true;$("disconnect").hidden=true;$("connection").textContent="未连接";$("connection").className="badge";$("management-key").value="";try{localStorage.removeItem(CONNECTION_STORE)}catch{};$("management-key").focus()}
async function connect(event){
 event?.preventDefault();const key=$("management-key").value.trim();if(!key)return;
 $("login-error").textContent="";$("connect").disabled=true;$("connect").textContent="连接中…";
 try{
  const snapshot=await api("/overview",{key});managementKey=key;generation++;overview=snapshot;
  try{if($("remember").checked)localStorage.setItem(CONNECTION_STORE,JSON.stringify({key}));else localStorage.removeItem(CONNECTION_STORE)}catch{$("login-error").textContent="浏览器无法保存密钥，本次连接仍可使用。"}
  $("management-key").value="";$("login").hidden=true;$("app").hidden=false;$("refresh").hidden=false;$("disconnect").hidden=false;$("connection").textContent="已连接";$("connection").className="badge good";
  await refresh(true);
 }catch(error){$("login-error").textContent=error.message;try{localStorage.removeItem(CONNECTION_STORE)}catch{}}
 finally{$("connect").disabled=false;$("connect").textContent="连接 →"}
}
async function refresh(manual=false){
 if(!managementKey||refreshing)return;refreshing=true;const epoch=generation;
 if(manual)$("refresh").disabled=true;
 try{const [stats,keys]=await Promise.all([api("/overview"),api("/credentials")]);if(epoch!==generation)return;overview=stats;credentials=keys.credentials||[];render();if(manual)$("notice").hidden=true}
 catch(error){if(epoch!==generation)return;if(error.status===401||error.status===403){disconnect();$("login-error").textContent=error.message}else notify(error.message,true)}
 finally{refreshing=false;$("refresh").disabled=false;clearTimeout(timer);if(managementKey)timer=setTimeout(()=>{if(document.hidden){timer=setTimeout(()=>refresh(),5000)}else refresh()},5000)}
}
function duration(ms){if(ms<0||ms==null)return "—";return ms<1000?Math.round(ms)+" ms":(ms/1000).toFixed(1)+" s"}
function timestamp(value){const d=new Date(value);return Number.isFinite(d.valueOf())?d.toLocaleString('zh-CN',{hour12:false}):"—"}
function statusClass(status){if(status==="completed")return "good";if(status==="failed")return "bad";if(status==="retrying"||status==="incomplete")return "warning";return ""}
function labelFor(id){return credentials.find(c=>c.id===id)?.label||"AR API Key"}
function render(){
 $("version").textContent="v"+overview.version;$("metric-requests").textContent=overview.requests;$("metric-active").textContent=overview.logs.active.length;$("metric-recovered").textContent=overview.recovered;$("metric-retries").textContent=overview.retries+" 次额外重试";
 const recent=overview.logs.recent.filter(r=>r.status!=="canceled"),done=recent.filter(r=>r.status==="completed").length;
 $("metric-rate").textContent=recent.length?(done/recent.length*100).toFixed(1)+"%":"—";$("metric-failures").textContent=recent.length?"最近 "+recent.length+" 次 · "+overview.failures+" 次失败":"等待请求数据";
 $("key-count").textContent=credentials.length;$("updated").textContent="更新于 "+new Date().toLocaleTimeString('zh-CN',{hour12:false});renderCredentials();renderLogs();renderHealth();
}
function setMarkup(id,value){const element=$(id);if(element.innerHTML!==value)element.innerHTML=value}
function renderCredentials(){
 $("credential-empty").hidden=credentials.length>0;
 setMarkup("credential-rows",credentials.map(c=>`<tr><td><strong>${esc(c.label)}</strong><small class="mono">${esc(c.masked_key)}</small></td><td><span class="pill ${c.disabled?"":"good"}">${c.disabled?"已停用":"已启用"}</span></td><td><div class="model-chips">${c.models.map(m=>`<span>${esc(m.ID)}</span>`).join("")}</div></td><td>${c.model_prefix?`<code>${esc(c.model_prefix)}/</code>`:'<span class="muted">无前缀</span>'}</td><td>${c.max_retries} 次 <small>${c.retry_budget_seconds} 秒等待预算</small></td><td><div class="row-actions"><button data-action="edit" data-index="${esc(c.auth_index)}">编辑</button><button data-action="toggle" data-index="${esc(c.auth_index)}">${c.disabled?"启用":"停用"}</button></div></td></tr>`).join(""));
}
function renderLogs(){
 if(!overview)return;const query=$("log-search").value.toLowerCase();const rows=[...overview.logs.active,...overview.logs.recent].sort((a,b)=>new Date(b.started_at)-new Date(a.started_at)).filter(r=>[r.model,labelFor(r.credential_id),stateNames[r.status],r.error_code].join(" ").toLowerCase().includes(query));
 $("log-empty").hidden=rows.length>0;setMarkup("log-rows",rows.map(r=>`<tr><td>${esc(timestamp(r.started_at))}<small>#${esc(r.id)}</small></td><td class="mono">${esc(r.model||"—")}</td><td>${esc(labelFor(r.credential_id))}</td><td><span class="pill ${statusClass(r.status)}">${esc(stateNames[r.status]||r.status)}</span>${r.error_code?`<small>${esc(r.error_code)}</small>`:""}</td><td>${r.attempts}</td><td>${duration(r.first_output_ms)}<small>${duration(r.duration_ms)}</small></td><td>${r.retry_reasons?.length?esc(r.retry_reasons.map(x=>reasonNames[x]||x).join(" → ")):'<span class="muted">—</span>'}</td></tr>`).join(""));
}
function renderHealth(){
 $("health-cards").innerHTML=credentials.length?credentials.map(c=>{
  const records=overview.logs.recent.filter(r=>r.credential_id===c.id),last=records.at(-1),check=overview.health[c.id];const label=c.disabled?"已停用":last?(last.status==="completed"?"最近请求正常":last.status==="failed"?"最近请求失败":"查看最近请求"):check?(check.ok?"连接检测通过":"连接检测失败"):"等待数据";const tone=c.disabled?"":last?statusClass(last.status):check?(check.ok?"good":"bad"):"";
  return `<article class="card health-card"><div class="health-title"><h2>${esc(c.label)}</h2><span class="pill ${tone}">${label}</span></div><dl><dt>并发会话</dt><dd>${overview.routing?.active_sessions?.[c.id]||0} / ${overview.routing?.limit||2}</dd><dt>近期请求</dt><dd>${records.length}</dd><dt>最后请求</dt><dd>${last?esc(timestamp(last.started_at)):"暂无"}</dd><dt>模型列表检测</dt><dd>${check?"HTTP "+check.http_status+" · "+duration(check.latency_ms):"未检测"}</dd><dt>检测时间</dt><dd>${check?esc(timestamp(check.checked_at)):"—"}</dd></dl><button data-health="${esc(c.auth_index)}">检测连接</button><p>${check?esc(check.message):"仅检查模型列表接口，不发送生成请求。"}</p></article>`
 }).join(""):'<div class="card empty"><strong>暂无可监控的 API Key</strong><p>先在 API Key 管理中添加凭证。</p></div>';
}
function editPayload(c){return {auth_index:c?.auth_index||"",label:c?.label||"",api_key:"",disabled:c?.disabled??true,model_prefix:c?.model_prefix||"",models:c?.models||[],user_agent:c?.headers?.['User-Agent']||overview.defaults.user_agent,originator:c?.headers?.Originator||overview.defaults.originator,max_retries:c?.max_retries??4,retry_budget_seconds:c?.retry_budget_seconds??180}}
function switchEditorTab(name){
 document.querySelectorAll('[data-editor-tab]').forEach(button=>{const selected=button.dataset.editorTab===name;button.setAttribute('aria-selected',String(selected));button.tabIndex=selected?0:-1;$("editor-"+button.dataset.editorTab).hidden=!selected});
}
function openEditor(c=null){
 editorEpoch++;switchEditorTab('basic');
 editing=c;$("editor-title").textContent=c?"编辑 API Key":"新增 API Key";$("editor-error").textContent="";$("field-label").value=c?.label||"";$("field-prefix").value=c?.model_prefix||"";$("field-key").value="";$("field-key").required=!c;$("field-key").placeholder=c?"留空保留当前 Key":"请输入 AR API Key";$("key-hint").textContent=c?"当前 "+c.masked_key+"；留空不会更换 Key。":"只保存到服务器，提交后不回显。";
 $("field-models").value=c?c.models.map(m=>m.Name&&m.Name!==m.ID?m.ID+" = "+m.Name:m.ID).join("\n"):"gpt-6.1-sol\ngpt-6-astra\ngpt-5.6-sol";
 $("field-ua").value=c?.headers?.['User-Agent']||overview.defaults.user_agent;$("field-originator").value=c?.headers?.Originator||overview.defaults.originator;$("field-retries").value=c?.max_retries??4;$("field-budget").value=c?.retry_budget_seconds??180;$("field-enabled").checked=c?!c.disabled:false;$("editor").showModal();$("field-label").focus();
}
function closeEditor(){editorEpoch++;$("model-picker").close();$("field-key").value="";$("editor").close();editing=null}
function parseModels(text,old=[]){return text.split(/\r?\n/).map(v=>v.trim()).filter(Boolean).map(line=>{const pair=line.split("=").map(v=>v.trim());if(pair.length>2||!pair[0])throw new Error("模型格式应为名称，或「显示名 = 上游模型名」。");const previous=old.find(m=>m.ID===pair[0]);return {...(previous||{Object:"model",Type:"codex",UserDefined:true,SupportedInputModalities:["text","image"],SupportedOutputModalities:["text"],Thinking:{Levels:["none","minimal","low","medium","high","xhigh","max"]}}),ID:pair[0],Name:pair[1]||pair[0]}})}
function buildModelChoices(ids,text){
 const existing=parseModels(text),fetched=new Set(ids),covered=new Set();const rows=[];
 for(const m of existing){const upstream=m.Name||m.ID;covered.add(upstream);rows.push({id:m.ID,upstream,line:upstream===m.ID?m.ID:m.ID+" = "+upstream,selected:true,available:fetched.has(upstream),existing:true})}
 for(const id of [...fetched].sort()){if(!covered.has(id)&&!rows.some(row=>row.id===id))rows.push({id,upstream:id,line:id,selected:false,available:true,existing:false})}
 return rows;
}
function visibleModelChoices(){const query=$("model-search").value.trim().toLowerCase();return modelChoices.filter(row=>(row.id+" "+row.upstream).toLowerCase().includes(query))}
function renderModelChoices(){
 const visible=visibleModelChoices();$("model-selection-count").textContent="已选 "+modelChoices.filter(x=>x.selected).length+" / "+modelChoices.length;
 $("model-options").innerHTML=visible.length?visible.map(row=>`<label class="model-option"><input type="checkbox" data-model-id="${esc(row.id)}" ${row.selected?'checked':''}><span><strong>${esc(row.id)}</strong>${row.upstream!==row.id?`<small>上游：${esc(row.upstream)}</small>`:''}</span>${row.existing?`<em>${row.available?'已配置':'已有 · 本次未返回'}</em>`:''}</label>`).join(''):'<div class="empty">没有匹配的模型</div>';
 $("apply-model-picker").disabled=!modelChoices.some(row=>row.selected);
}
async function fetchModelChoices(){
 if($("fetch-models").disabled)return;const epoch=editorEpoch,key=$("field-key").value.trim(),text=$("field-models").value;
 $("editor-error").textContent="";$("fetch-models").disabled=true;$("fetch-models").textContent="获取中…";
 try{
  parseModels(text);
  const result=await api('/models',{method:'POST',body:{auth_index:editing?.auth_index||'',api_key:key,user_agent:$("field-ua").value,originator:$("field-originator").value}});
  if(epoch!==editorEpoch||!$("editor").open)return;
  if(key!==$("field-key").value.trim())throw new Error('API Key 已变化，请重新获取模型。');
  modelChoices=buildModelChoices(result.models,$("field-models").value);$("model-search").value="";renderModelChoices();$("model-picker").showModal();$("model-search").focus();
 }catch(error){if(epoch===editorEpoch)$("editor-error").textContent=error.message}
 finally{$("fetch-models").disabled=false;$("fetch-models").textContent="获取模型"}
}
function closeModelPicker(){$("model-picker").close();modelChoices=[];$("model-options").replaceChildren()}
function applyModelChoices(){const selected=modelChoices.filter(row=>row.selected);if(!selected.length)return;$("field-models").value=selected.map(row=>row.line).join('\n');closeModelPicker();$("field-models").focus()}
async function saveEditor(event){event.preventDefault();if(mutationPending)return;mutationPending=true;$("save-key").disabled=true;$("editor-error").textContent="";
 try{const body=editPayload(editing);Object.assign(body,{label:$("field-label").value.trim(),api_key:$("field-key").value.trim(),disabled:!$("field-enabled").checked,model_prefix:$("field-prefix").value.trim(),models:parseModels($("field-models").value,editing?.models),user_agent:$("field-ua").value,originator:$("field-originator").value,max_retries:Number($("field-retries").value),retry_budget_seconds:Number($("field-budget").value)});await api("/credentials",{method:editing?"PUT":"POST",body});closeEditor();await refresh();notify("API Key 配置已保存，无需重启 CPA。")}catch(error){$("editor-error").textContent=error.message}finally{mutationPending=false;$("save-key").disabled=false}}
async function toggleCredential(c,button){if(mutationPending)return;mutationPending=true;button.disabled=true;try{const body=editPayload(c);body.disabled=!c.disabled;await api("/credentials",{method:"PUT",body});await refresh();notify(body.disabled?"该 API Key 已停用。":"该 API Key 已启用。")}catch(error){notify(error.message,true)}finally{mutationPending=false;button.disabled=false}}
function init(){
 $("fetch-models").addEventListener('click',fetchModelChoices);
 $("close-model-picker").addEventListener('click',closeModelPicker);$("cancel-model-picker").addEventListener('click',closeModelPicker);$("apply-model-picker").addEventListener('click',applyModelChoices);
 $("model-search").addEventListener('input',renderModelChoices);
 $("model-options").addEventListener('change',event=>{const id=event.target.dataset.modelId;const row=modelChoices.find(x=>x.id===id);if(row){row.selected=event.target.checked;$("model-selection-count").textContent="已选 "+modelChoices.filter(x=>x.selected).length+" / "+modelChoices.length;$("apply-model-picker").disabled=!modelChoices.some(x=>x.selected)}});
 for(const [id,selected] of [['select-visible-models',true],['clear-visible-models',false]])$(id).addEventListener('click',()=>{for(const row of visibleModelChoices())row.selected=selected;renderModelChoices()});
 $("model-picker").addEventListener('cancel',()=>{modelChoices=[]});

 applyTheme();setInterval(applyTheme,1500);window.addEventListener("storage",applyTheme);matchMedia("(prefers-color-scheme: dark)").addEventListener("change",applyTheme);
 $("connect-form").addEventListener("submit",connect);$("disconnect").addEventListener("click",disconnect);$("refresh").addEventListener("click",()=>refresh(true));$("add-key").addEventListener("click",()=>openEditor());$("close-editor").addEventListener("click",closeEditor);$("cancel-editor").addEventListener("click",closeEditor);$("editor").addEventListener("cancel",()=>{editorEpoch++;$("field-key").value="";editing=null});$("credential-form").addEventListener("submit",saveEditor);
 $("credential-form").addEventListener("invalid",event=>{const section=event.target.closest('[role="tabpanel"]');if(section)switchEditorTab(section.id.replace('editor-',''))},true);
 document.querySelectorAll('[data-editor-tab]').forEach(button=>{button.addEventListener('click',()=>switchEditorTab(button.dataset.editorTab));button.addEventListener('keydown',event=>{if(['ArrowLeft','ArrowRight','Home','End'].includes(event.key)){event.preventDefault();const name=event.key==='Home'?'basic':event.key==='End'?'advanced':button.dataset.editorTab==='basic'?'advanced':'basic';switchEditorTab(name);$("editor-"+name+"-tab").focus()}})});$("log-search").addEventListener("input",renderLogs);
 document.querySelectorAll('[data-tab]').forEach(button=>button.addEventListener('click',()=>{document.querySelectorAll('[data-tab]').forEach(b=>{const selected=b===button;b.setAttribute('aria-selected',String(selected));$(b.dataset.tab+'-panel').hidden=!selected})}));
 $("credential-rows").addEventListener("click",event=>{const b=event.target.closest('button[data-action]');if(!b)return;const c=credentials.find(x=>x.auth_index===b.dataset.index);if(!c)return;b.dataset.action==="edit"?openEditor(c):toggleCredential(c,b)});
 $("health-cards").addEventListener("click",async event=>{const b=event.target.closest('button[data-health]');if(!b)return;b.disabled=true;b.textContent="检测中…";try{const result=await api("/health",{method:"POST",body:{auth_index:b.dataset.health}});await refresh();notify(result.message,!result.ok)}catch(error){notify(error.message,true)}finally{b.disabled=false;b.textContent="检测连接"}});
 const existing=savedKey();if(existing){$("management-key").value=existing;$("remember").checked=true;connect()}
}
if(typeof document!=="undefined")document.addEventListener("DOMContentLoaded",init);
