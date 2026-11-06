/* 独立接口模拟：验证监控交互，不读取生产配置或访问真实上游。 */
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const path=require('node:path');
const {chromium}=require(process.env.PLAYWRIGHT_PATH||'playwright');
const root=path.resolve(__dirname,'../..'),output=path.join(root,'dist','preview');
async function main(){
 const now=Date.parse('2026-11-06T06:35:00Z');
 const credentials=[{id:'primary',auth_index:'primary',label:'开发主账号',masked_key:'••••4821',disabled:false,model_prefix:'ar',models:[{ID:'daily',Name:'gpt-6.1-sol'},{ID:'gpt-6-astra',Name:'gpt-6-astra'}],max_concurrent_sessions:4,active_sessions:2,max_retries:4,retry_budget_seconds:180,headers:{}},{id:'secondary',auth_index:'secondary',label:'备用账号',masked_key:'••••3065',disabled:false,models:[{ID:'gpt-5.6-sol',Name:'gpt-5.6-sol'}],max_concurrent_sessions:2,active_sessions:0,max_retries:2,retry_budget_seconds:60,headers:{}}];
 const recent=Array.from({length:55},(_,i)=>({id:String(100+i),credential_id:i%3?'primary':'secondary',model:i%3?'gpt-6.1-sol':'gpt-5.6-sol',started_at:new Date(now-(55-i)*40000).toISOString(),duration_ms:7800+i*100,first_output_ms:850+i*10,attempts:i%9===0?3:1,status:i%11===0?'failed':i%13===0?'incomplete':i%17===0?'canceled':'completed',error_code:i%11===0?'ar_upstream_pricing_error':'',retry_reasons:i%9===0?['transport','rate']:[]}));
 const active=[{...recent[0],id:'active',credential_id:'primary',model:'gpt-6-astra',started_at:new Date(now-30000).toISOString(),status:'retrying',error_code:'',attempts:2,retry_reasons:['rate']}];
 const buckets=Array.from({length:40},(_,i)=>credentials.map((c,j)=>({started_at:new Date(Math.floor(now/300000)*300000-(39-i)*300000).toISOString(),credential_id:c.id,model:j?'gpt-5.6-sol':'gpt-6.1-sol',requests:10,completed:j?(i%5===0?0:6):i%7===0?8:10,failed:j?(i%5===0?10:4):i%7===0?2:0,retries:2,recovered:1,usage_count:10,tokens:{input_tokens:10000,cached_tokens:8000,output_tokens:1000}}))).flat();
 const snapshot={version:'0.5.2',logs:{recent,active,analytics:{now:new Date(now).toISOString(),started_at:new Date(now-12*3600000).toISOString(),credential_scoped:true,buckets}},health:{primary:{ok:true,latency_ms:460,http_status:200,checked_at:new Date(now-600000).toISOString(),message:'连接检测通过，模型列表可访问。'}},defaults:{user_agent:'test-agent',originator:'test-agent'}};
 let priceMode='success',releasePrice=null,priceRequests=0,healthRequests=0,otherMutations=0;
 const browser=await chromium.launch({headless:true,...(process.env.BROWSER_PATH?{executablePath:process.env.BROWSER_PATH}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000},timezoneId:'America/Los_Angeles'}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const request=route.request(),url=new URL(request.url());
   if(url.pathname.includes('/resource/')){const name=path.basename(url.pathname),file=name==='ui'?'index.html':name;await route.fulfill({body:await fs.readFile(path.join(root,'web',file)),contentType:file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.png')?'image/png':'text/html'});return;}
   if(url.pathname.endsWith('/pricing')){priceRequests++;if(priceMode==='delay')await new Promise(resolve=>releasePrice=resolve);await route.fulfill(priceMode==='error'?{status:502,json:{error:'渠道暂不可用；当前单价未修改。'}}:{json:{prices:{'gpt-6.1-sol':{input:6,cached:6,output:30,cache_assumed:true},'gpt-6-astra':{input:12,cached:1.2,output:60,cache_assumed:false}},currency:'USD',unit:'million_tokens',group:'default',fetched_at:new Date(now).toISOString(),skipped:0}});return;}
   if(url.pathname.endsWith('/health')){healthRequests++;const {auth_index}=request.postDataJSON();snapshot.health[auth_index]={ok:false,latency_ms:320,http_status:503,checked_at:new Date(now).toISOString(),message:'模型列表服务暂不可用。'};await route.fulfill({json:snapshot.health[auth_index]});return;}
   if(request.method()!=='GET')otherMutations++;
   await route.fulfill({json:url.pathname.endsWith('/overview')?snapshot:{credentials}});
  });
  await page.addInitScript(()=>localStorage.setItem('cpa-agentrouter-continuity.connection.v1',JSON.stringify({key:'mock-runtime-only'})));
  await page.goto('http://monitor.test/v0/resource/plugins/cpa-agentrouter-continuity/ui');await page.waitForSelector('#monitor-keys .status-block');
  assert.equal(await page.locator('#monitor-keys .status-block').count(),40);
  assert.match(await page.locator('#monitor-keys .key-rate').first().textContent(),/9\d\.\d%/);
  assert.notEqual(await page.locator('#monitor-keys .key-rate').nth(1).textContent(),await page.locator('#monitor-keys .key-rate').first().textContent());
  const last=page.locator('#monitor-keys .status-block').nth(19);await last.focus();await page.keyboard.press('ArrowLeft');assert.equal(await page.locator('#monitor-keys .status-block').nth(18).evaluate(e=>e===document.activeElement),true);assert.match(await page.locator('#chart-tooltip').textContent(),/完成率/);
  await page.locator('#logs-tab').click();assert.equal(await page.locator('#log-rows tr').count(),20);assert.equal(await page.locator('#log-page-label').textContent(),'1 / 3');
  assert.match(await page.locator('#log-rows tr').first().textContent(),/14:34:30/,'日志时间固定北京时间');
  await page.locator('#log-next').click();assert.equal(await page.locator('#log-page-label').textContent(),'2 / 3');
  await page.locator('#log-status').selectOption('recovered');assert.equal(await page.locator('#log-page-label').textContent(),'1 / 1');assert.ok(await page.locator('#log-rows tr').count()>0);
  await page.locator('[data-request]').first().click();assert.match(await page.locator('#request-detail-body').textContent(),/内部重试后完成/);assert.equal(await page.locator('.retry-timeline li').count(),2);await page.keyboard.press('Escape');
  await page.locator('#log-status').selectOption('failed');await page.locator('#log-key').selectOption('secondary');const failedCount=await page.locator('#log-rows tr').count();assert.ok(failedCount>0);assert.match(await page.locator('#log-rows').textContent(),/备用账号/);
  await page.locator('[data-request]').first().click();assert.match(await page.locator('.error-detail').textContent(),/ar_upstream_pricing_error/);await page.screenshot({path:path.join(output,'request-detail-light.png')});await page.locator('#dismiss-request-detail').click();
  await page.locator('#log-search').fill('nothing-matches');assert.match(await page.locator('#log-empty').textContent(),/没有匹配/);await page.locator('#reset-log-filters').click();await page.locator('#log-page-size').selectOption('50');assert.equal(await page.locator('#log-rows tr').count(),50);
  await page.locator('#log-page-size').selectOption('20');await page.evaluate(()=>document.querySelector('#logs-panel .page-heading p').textContent='演示数据 · 浏览器验收');await page.screenshot({path:path.join(output,'request-logs-light.png'),fullPage:true});
  await page.locator('#health-tab').click();assert.equal(await page.locator('.connection-card').count(),2);assert.match(await page.locator('.connection-check').nth(1).textContent(),/尚未检测/);assert.equal(healthRequests,0);
  const rateBefore=await page.locator('.connection-performance strong').nth(1).textContent();await page.locator('[data-health="secondary"]').click();await page.waitForFunction(()=>document.querySelectorAll('.connection-check')[1].textContent.includes('检测失败'));assert.equal(await page.locator('.connection-performance strong').nth(1).textContent(),rateBefore);assert.equal(healthRequests,1);
  await page.locator('[data-policy="secondary"]').click();assert.equal(await page.locator('#field-retries').inputValue(),'2');assert.equal(await page.locator('#editor-advanced').isVisible(),true);await page.locator('#cancel-editor').click();
  await page.evaluate(()=>document.querySelector('#health-panel .page-heading p').textContent='演示数据 · 浏览器验收');await page.screenshot({path:path.join(output,'connection-policy-light.png'),fullPage:true});
  await page.locator('#dashboard-tab').click();await page.locator('#configure-prices').click();
  const input=i=>page.locator(`[data-price-model="${i}"][data-price-key="input"]`);
  assert.equal(await input(0).inputValue(),'4','别名使用上游默认价');assert.match(await page.locator('[data-price-source="0"]').textContent(),/缓存价未公布/);
  assert.equal(priceRequests,0,'无自动渠道请求');await page.locator('#fetch-prices').click();await page.waitForFunction(()=>document.getElementById('price-status').textContent.includes('已填入'));assert.equal(await input(0).inputValue(),'6');assert.match(await page.locator('#price-status').textContent(),/未匹配/);
  await page.locator('#cancel-prices').click();await page.locator('#configure-prices').click();assert.equal(await input(0).inputValue(),'4','取消不保存获取值');
  priceMode='error';await page.locator('#fetch-prices').click();await page.waitForSelector('#price-error:not(:empty)');assert.equal(await input(0).inputValue(),'4');
  priceMode='delay';await page.locator('#fetch-prices').click();await page.waitForTimeout(50);await input(0).fill('7');releasePrice();await page.waitForFunction(()=>document.getElementById('price-status').textContent.includes('保留获取期间的编辑'));assert.equal(await input(0).inputValue(),'7');
  await page.screenshot({path:path.join(output,'channel-prices-light.png')});await page.locator('#price-form button[type=submit]').click();await page.reload();await page.waitForSelector('#monitor-keys .status-block');await page.locator('#configure-prices').click();assert.equal(await input(0).inputValue(),'7','手工价持久保留');
  for(const f of await page.locator('[data-price-model="0"]').all())await f.fill('');await page.locator('#price-form button[type=submit]').click();await page.locator('#configure-prices').click();assert.equal(await input(0).inputValue(),'','清空后不恢复默认值');await page.locator('#cancel-prices').click();
  priceMode='delay';await page.locator('#configure-prices').click();await page.locator('#fetch-prices').click();await page.waitForTimeout(50);await page.locator('#cancel-prices').click();await page.locator('#configure-prices').click();releasePrice();await page.waitForTimeout(100);assert.equal(await input(0).inputValue(),'','已关闭弹窗的异步结果不写入新表单');await page.locator('#cancel-prices').click();
  for(const theme of ['white','dark']){
   await page.evaluate(theme=>{localStorage.setItem('cli-proxy-theme',JSON.stringify({state:{theme}}));applyTheme();},theme);
   for(const tab of ['logs','health']){
    await page.locator(`#${tab}-tab`).click();await page.setViewportSize({width:1440,height:1000});await page.screenshot({path:path.join(output,`${tab}-${theme}.png`),fullPage:true});
    await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`${tab} 页面不溢出`);await page.screenshot({path:path.join(output,`${tab}-${theme}-mobile.png`),fullPage:true});
   }
  }
  assert.deepEqual(errors,[]);assert.equal(otherMutations,0);
  console.log('PASS 多 Key 完成率、键盘、日志分页/组合筛选/详情、北京时间、检测独立展示、配置入口、默认定价/别名/获取/失败保留/异步保护/清空/持久化、双主题与小屏');
 }finally{await browser.close();}
}
main().catch(e=>{console.error(e);process.exitCode=1;});
