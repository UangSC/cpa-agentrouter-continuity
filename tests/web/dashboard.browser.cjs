/* 浏览器验收使用独立的模拟接口，不访问生产凭证或调用上游。 */
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const {chromium} = require(process.env.PLAYWRIGHT_PATH || 'playwright');
const root = path.resolve(__dirname, '../..');
const output = path.join(root, 'dist', 'preview');

async function main() {
 const now=Date.parse('2026-11-06T07:42:00Z'),start=Date.parse('2026-11-05T16:00:00Z');
 const models=['gpt-6-astra','gpt-6.1-sol','gpt-5.6-sol','gpt-6-luna'];
 const buckets=Array.from({length:189},(_,i)=>models.map((model,j)=>{
  const requests=Math.max(0,Math.round((Math.sin(i/8+j)*3+4)*(1+Math.sin(i/42))));
  const failed=requests>0&&i%31===0?1:0,retries=requests>1&&i%9===0?2:0;
  return {started_at:new Date(start+i*300000).toISOString(),model,requests,completed:requests-failed,failed,incomplete:0,canceled:0,recovered:retries?1:0,retries,usage_count:requests,first_output_count:requests,first_output_total_ms:requests*(1500+j*400),tokens:{input_tokens:requests*22000,output_tokens:requests*2000,cached_tokens:requests*16000}};
 })).flat();
 let credentials=[{id:'demo',auth_index:'demo',label:'日常开发',masked_key:'sk-••••demo',disabled:false,models:models.map(ID=>({ID,Name:ID})),active_sessions:2,max_concurrent_sessions:4,max_retries:2,retry_budget_seconds:60,headers:{}}];
 let recent=Array.from({length:39},(_,i)=>({id:String(i),credential_id:'demo',model:models[i%4],started_at:new Date(now-(40-i)*120000).toISOString(),duration_ms:15000+i*200,first_output_ms:1800,status:i%21===0?'failed':i%13===0?'incomplete':'completed',attempts:i%9===0?3:1,retry_reasons:[]}));
 let snapshot={version:'0.5.2',logs:{active:[{...recent[0],id:'active',status:'retrying',attempts:2,started_at:new Date(now-30000).toISOString()}],recent,analytics:{started_at:new Date(start).toISOString(),now:new Date(now).toISOString(),buckets}},health:{},defaults:{user_agent:'test-agent',originator:'test-agent'}};
 let unavailable=false,mutations=0;
 const browser=await chromium.launch({headless:true,...(process.env.BROWSER_PATH?{executablePath:process.env.BROWSER_PATH}:{})});
 try {
  await fs.mkdir(output,{recursive:true});
  const page=await browser.newPage({viewport:{width:1440,height:1000},reducedMotion:'reduce'});
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const request=route.request(),url=new URL(request.url());
   if(url.pathname.includes('/resource/')) {
    const name=path.basename(url.pathname),file=name==='ui'?'index.html':name;
    await route.fulfill({body:await fs.readFile(path.join(root,'web',file)),contentType:file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.png')?'image/png':'text/html'});return;
   }
   if(request.method()!=='GET') {mutations++;await route.fulfill({json:{}});return;}
   if(unavailable){await route.fulfill({status:503,json:{error:'测试：连接中断'}});return;}
   await route.fulfill({json:url.pathname.endsWith('/overview')?snapshot:{credentials}});
  });
  await page.addInitScript(()=>localStorage.setItem('cpa-agentrouter-continuity.connection.v1',JSON.stringify({key:'mock-runtime-only'})));
  await page.goto('http://dashboard.test/v0/resource/plugins/cpa-agentrouter-continuity/ui');
  await page.waitForSelector('#request-heatmap .heat-cell');
  assert.equal(await page.locator('.heat-cell').count(),144);
  assert.equal(await page.locator('#monitor-keys .status-block').count(),20);
  assert.equal(await page.locator('#dashboard-panel').isVisible(),true);
  await page.locator('[data-range="1h"]').click();assert.equal(await page.locator('.chart-hit').count(),12);
  await page.locator('[data-range="6h"]').click();assert.equal(await page.locator('.chart-hit').count(),36);
  await page.locator('#traffic-mode').selectOption('tokens');
  await page.locator('.chart-hit').nth(5).hover();assert.match(await page.locator('#chart-tooltip').innerText(),/输入 Token/);
  await page.keyboard.press('Escape');assert.equal(await page.locator('#chart-tooltip').isVisible(),false);
  await page.locator('#configure-prices').click();
  await page.locator('[data-price-model="0"][data-price-key="input"]').fill('10');
  await page.locator('[data-price-model="0"][data-price-key="cached"]').fill('');
  await page.locator('#price-form button[type=submit]').click();assert.match(await page.locator('#price-error').innerText(),/完整填写/);
  await page.locator('[data-price-model="0"][data-price-key="cached"]').fill('1');
  await page.locator('[data-price-model="0"][data-price-key="output"]').fill('20');
  await page.locator('#price-form button[type=submit]').click();
  await page.locator('[data-rank="cost"]').click();assert.match(await page.locator('#rank-total').innerText(),/已计价 \$/);
  assert.match(await page.locator('#model-ranking').innerText(),/未配置单价/);
  assert.equal(mutations,0,'单价设置不应写入宿主');
  await page.reload();await page.waitForSelector('.heat-cell');
  await page.locator('[data-rank="cost"]').click();assert.match(await page.locator('#rank-total').innerText(),/已计价 \$/);
  await page.locator('[data-tab="credentials"]').click();await page.locator('#add-key').click();
  assert.equal(await page.locator('#field-concurrency').inputValue(),'2');
  await page.locator('#cancel-editor').click();
  await page.locator('[data-tab="logs"]').click();assert.equal(await page.locator('#log-rows tr').count(),20);
  await page.locator('#log-search').fill('failed-model-no-match');assert.equal(await page.locator('#log-empty').isVisible(),true);
  await page.locator('[data-tab="dashboard"]').click();
  await page.locator('[data-rank="requests"]').click();
  await page.evaluate(()=>{document.querySelector('.dashboard-heading p').textContent='演示数据 · 浏览器验收';});
  await page.screenshot({path:path.join(output,'dashboard-light.png'),fullPage:true});
  await page.evaluate(()=>{localStorage.setItem('cli-proxy-theme',JSON.stringify({state:{theme:'dark'}}));applyTheme();});
  await page.screenshot({path:path.join(output,'dashboard-dark.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,'小屏页面不应横向溢出');
  await page.screenshot({path:path.join(output,'dashboard-mobile.png'),fullPage:true});
  await page.emulateMedia({reducedMotion:'reduce'});
  assert.equal(await page.locator('.status-block').first().evaluate(e=>getComputedStyle(e).animationName),'none');
  credentials=[{...credentials[0],label:'<img src=x onerror=alert(1)>'}];
  await page.locator('#refresh').click();await page.waitForFunction(()=>document.querySelector('#monitor-keys').textContent.includes('<img'));
  assert.equal(await page.locator('#monitor-keys img').count(),0,'凭证名称必须转义');
  unavailable=true;await page.locator('#refresh').click();await page.waitForSelector('#live-label.stale');
  unavailable=false;credentials=[];snapshot={...snapshot,logs:{active:[],recent:[],analytics:{started_at:new Date(now).toISOString(),now:new Date(now).toISOString(),buckets:[]}}};
  await page.locator('#refresh').click();await page.waitForFunction(()=>document.querySelector('#metric-requests').textContent==='0');
  assert.equal(await page.locator('#metric-tokens').innerText(),'—');assert.equal(await page.locator('#metric-rate').innerText(),'—');
  assert.match(await page.locator('#model-ranking').innerText(),/暂无模型数据/);
  assert.doesNotMatch(await page.locator('#dashboard-panel').innerHTML(),/NaN|Infinity/);
  await page.setViewportSize({width:1440,height:1000});
  await page.screenshot({path:path.join(output,'dashboard-empty.png'),fullPage:true});
  assert.deepEqual(errors,[]);
  console.log('PASS 仪表盘、价格、缓存、页签、筛选、键盘提示、深浅主题、小屏、减少动效、转义、断线与空数据；无宿主写入');
 } finally {await browser.close();}
}
main().catch(e=>{console.error(e);process.exitCode=1;});
