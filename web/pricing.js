"use strict";
// Verified against AgentRouter /api/pricing and its pricing-page formula on 2026-11-06.
// Cache rates are unpublished: use ordinary input for a clearly labelled estimate.
const DEFAULT_CHANNEL_PRICES = {
 'gpt-5.6-sol':{input:4,cached:4,output:20,cache_assumed:true,source:'default',checked_at:'2026-11-06'},
 'gpt-6.1-sol':{input:4,cached:4,output:20,cache_assumed:true,source:'default',checked_at:'2026-11-06'},
 'gpt-6-astra':{input:10,cached:10,output:50,cache_assumed:true,source:'default',checked_at:'2026-11-06'}
};
let priceDialogEpoch=0,priceDraftMetadata=Object.create(null);
function validPrice(p) {return p&&[p.input,p.cached,p.output].every(x=>typeof x==='number'&&Number.isFinite(x)&&x>=0&&x<=1000000);}
function upstreamPriceModel(model) {
 const candidates=new Set();
 for(const c of credentials)for(const m of c.models||[]){
  const upstream=m.Name||m.ID;
  if(model===m.ID||model===upstream||(c.model_prefix&&model===c.model_prefix+'/'+m.ID))candidates.add(upstream);
 }
 return candidates.size>1?null:candidates.size===1?[...candidates][0]:model;
}
function resolvedPrice(model) {
 if(Object.hasOwn(modelPrices,model))return modelPrices[model];
 const upstream=upstreamPriceModel(model);
 if(upstream&&Object.hasOwn(modelPrices,upstream))return modelPrices[upstream];
 return upstream?DEFAULT_CHANNEL_PRICES[upstream]:undefined;
}
function priceNote(p) {
 if(!p)return '未配置单价';
 if(!p.source)return '手动单价 · 仅当前浏览器';
 return (p.source==='default'?'渠道默认值 · '+p.checked_at:'渠道定价 · '+timestamp(p.checked_at))+' · 默认分组'+(p.cache_assumed?'；缓存价未公布，暂按普通输入估算':'');
}
function openPrices() {
 priceDialogEpoch++;
 const d=dashboardData(overview);
 priceModels=[...new Set([...d.models.map(m=>m.model),...credentials.flatMap(c=>c.models.map(m=>m.ID))])].sort();
 priceDraftMetadata=Object.create(null);
 $('price-error').textContent='';$('price-status').textContent='已预设 2026-11-06 渠道默认分组单价；未知模型保留空白。可手动调整，或获取渠道最新公开价格。';
 $('fetch-prices').disabled=!priceModels.length;$('fetch-prices').textContent='获取渠道定价';
 setMarkup('price-fields',priceModels.length?priceModels.map((model,i)=>{
  const p=resolvedPrice(model);priceDraftMetadata[model]=p?{...p}:null;
  return `<fieldset><legend>${esc(model)}</legend><div class="price-grid">${[['input','普通输入'],['cached','缓存命中'],['output','输出']].map(([key,label])=>`<label>${label}<input type="number" min="0" max="1000000" step="any" placeholder="未配置" aria-label="${esc(model+' '+label)}" data-price-model="${i}" data-price-key="${key}" value="${p?.[key]??''}"></label>`).join('')}</div><p class="price-source" data-price-source="${i}">${esc(priceNote(p))}</p></fieldset>`;
 }).join(''):'<p class="empty">添加模型或产生请求后，即可设置单价。</p>');
 $('price-editor').showModal();
}
async function fetchPrices() {
 if($('fetch-prices').disabled)return;
 const epoch=priceDialogEpoch,connection=generation;
 const initial=[...document.querySelectorAll('[data-price-model]')].map(f=>f.value);
 $('fetch-prices').disabled=true;$('fetch-prices').textContent='获取中…';$('price-error').textContent='';
 try {
  const result=await api('/pricing');
  if(epoch!==priceDialogEpoch||connection!==generation||!$('price-editor').open)return;
  if(result.currency!=='USD'||result.unit!=='million_tokens'||result.group!=='default'||!result.prices)throw new Error('渠道返回的币种或单位无法识别，当前表单未修改。');
  let applied=0,skipped=0,edited=0;
  for(let i=0;i<priceModels.length;i++){
   const model=priceModels[i],upstream=upstreamPriceModel(model),p=upstream?result.prices[upstream]:null,fields=[...document.querySelectorAll(`[data-price-model="${i}"]`)];
   if(!validPrice(p)){skipped++;continue;}
   if(fields.some((f,j)=>f.value!==initial[i*3+j])){edited++;continue;}
   for(const f of fields)f.value=p[f.dataset.priceKey];
   priceDraftMetadata[model]={...p,source:'channel',checked_at:result.fetched_at};
   document.querySelector(`[data-price-source="${i}"]`).textContent=priceNote(priceDraftMetadata[model]);applied++;
  }
  $('price-status').textContent=`已填入 ${applied} 个模型 · ${skipped} 个未匹配${edited?' · '+edited+' 个保留获取期间的编辑':''}。默认分组；缓存价未公布时按普通输入估算。点击「保存单价」后生效。`;
 }catch(error){if(epoch===priceDialogEpoch&&connection===generation&&$('price-editor').open)$('price-error').textContent=error.message;}
 finally{if(epoch===priceDialogEpoch){$('fetch-prices').disabled=!priceModels.length;$('fetch-prices').textContent='获取渠道定价';}}
}
function initPricing() {
 try{const stored=JSON.parse(localStorage.getItem(PRICE_STORE)||'{}');for(const [model,p] of Object.entries(stored||{}))if(p===null||validPrice(p))modelPrices[model]=p;}catch{}
 $('configure-prices').addEventListener('click',openPrices);
 for(const id of ['close-prices','cancel-prices'])$(id).addEventListener('click',()=>$('price-editor').close());
 $('price-editor').addEventListener('close',()=>{priceDialogEpoch++;});
 $('fetch-prices').addEventListener('click',fetchPrices);
 $('price-fields').addEventListener('input',e=>{const index=e.target.dataset.priceModel;if(index===undefined)return;const model=priceModels[Number(index)];priceDraftMetadata[model]=null;document.querySelector(`[data-price-source="${index}"]`).textContent='手动单价 · 保存后生效';});
 $('price-form').addEventListener('submit',e=>{
  e.preventDefault();const next=Object.assign(Object.create(null),modelPrices);
  for(let i=0;i<priceModels.length;i++){
   const fields=[...document.querySelectorAll(`[data-price-model="${i}"]`)];
   if(fields.every(f=>f.value==='')){next[priceModels[i]]=null;continue;}
   if(fields.some(f=>f.value===''||!Number.isFinite(Number(f.value))||Number(f.value)<0||Number(f.value)>1000000)){$('price-error').textContent='请完整填写该模型的三项单价，或全部留空。';return;}
   next[priceModels[i]]={...(priceDraftMetadata[priceModels[i]]||{}),...Object.fromEntries(fields.map(f=>[f.dataset.priceKey,Number(f.value)]))};
  }
  try{localStorage.setItem(PRICE_STORE,JSON.stringify(next));modelPrices=next;$('price-editor').close();renderDashboard();}catch{$('price-error').textContent='浏览器无法保存单价，请检查本地存储权限。';}
 });
}
