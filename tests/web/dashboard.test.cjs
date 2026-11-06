const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
function runtime(){const ctx=vm.createContext({TextEncoder,TextDecoder,Uint8Array,atob,console});for(const file of ['app.js','dashboard.js','monitoring.js','pricing.js'])vm.runInContext(fs.readFileSync(path.join(__dirname,'../../web',file),'utf8'),ctx);return code=>vm.runInContext(code,ctx);}

test('status window isolates keys and retains aggregate totals beyond recent logs',()=>{
 const run=runtime();run(`globalThis.s={logs:{recent:[],active:[],analytics:{now:'2026-11-06T04:04:00Z',credential_scoped:true,buckets:[
 {started_at:'2026-11-06T04:00:00Z',credential_id:'a',requests:300,completed:270,failed:30},
 {started_at:'2026-11-06T04:00:00Z',credential_id:'b',requests:4,failed:4},
 {started_at:'2026-11-06T00:40:00Z',credential_id:'a',requests:99,failed:99},
 {started_at:'2026-11-06T04:10:00Z',credential_id:'a',requests:99,failed:99}]}}}`);
 assert.equal(run('statusWindow(s,"a").rate'),.9);
 assert.equal(run('statusWindow(s,"b").rate'),0);
 assert.equal(run('statusWindow(s,"a").total.requests'),300);
 assert.equal(run('statusWindow(s,"a").bins.length'),20);
 assert.equal(run('statusWindow(s,"a").partial'),false);
 assert.equal(run('statusWindow(s,"unknown").rate'),null);
});
test('status rate ignores cancellation and running requests; recovered remains completed',()=>{
 const run=runtime();assert.equal(run('completionRate(aggregate([{completed:2,failed:1,incomplete:1,recovered:1,canceled:5,requests:20}]))'),.5);
 assert.equal(run('completionRate(aggregate([{canceled:3,requests:6}]))'),null);
 assert.equal(run('rateColor(0)'), 'rgb(239, 68, 68)');assert.equal(run('rateColor(.5)'), 'rgb(250, 204, 21)');assert.equal(run('rateColor(1)'), 'rgb(34, 197, 94)');
 assert.notEqual(run('rateColor(.75)'),run('rateColor(.5)'));assert.equal(run('rateTone(.9)'),'good');assert.equal(run('rateTone(.5)'),'warning');assert.equal(run('rateTone(.49)'),'bad');
});
test('legacy key status uses only matching retained records and signals partial scope',()=>{
 const run=runtime();run(`globalThis.s={logs:{active:[],recent:[{credential_id:'a',model:'x',started_at:'2026-11-06T04:00:00Z',status:'completed',attempts:3}],analytics:{now:'2026-11-06T04:04:00Z',buckets:[{started_at:'2026-11-06T04:00:00Z',requests:100,failed:100}]}}}`);
 assert.equal(run('statusWindow(s,"a").rate'),1);assert.equal(run('statusWindow(s,"a").partial'),true);
});
test('request filters combine state, key, request id and recovery',()=>{
 const run=runtime();run(`globalThis.rows=[{id:'1',model:'m',credential_id:'a',status:'completed',attempts:2},{id:'2',model:'m',credential_id:'b',status:'failed',attempts:2,error_code:'upstream_failure'},{id:'3',credential_id:'a',status:'retrying',attempts:2}]`);
 assert.equal(run('filterLogRecords(rows,{status:"recovered",key:"a"})[0].id'),'1');
 assert.equal(run('filterLogRecords(rows,{status:"active"})[0].id'),'3');
 assert.equal(run('filterLogRecords(rows,{query:" upstream_failure ",key:"b"})[0].id'),'2');
 assert.equal(run('filterLogRecords(rows,{query:"2",key:"a"}).length'),0);
});
test('pricing defaults and aliases respect saved overrides, explicit clearing and ambiguity',()=>{
 const run=runtime();run(`credentials=[{model_prefix:'ar',models:[{ID:'daily',Name:'gpt-6.1-sol'}]}]`);
 assert.equal(run('resolvedPrice("daily").input'),4);assert.equal(run('resolvedPrice("ar/daily").output'),20);
 assert.equal(run('resolvedPrice("gpt-6-astra").cache_assumed'),true);
 run('modelPrices["daily"]={input:0,cached:0,output:0}');assert.equal(run('resolvedPrice("daily").input'),0);
 run('modelPrices["daily"]=null');assert.equal(run('resolvedPrice("daily")'),null);
 run(`credentials.push({models:[{ID:'daily',Name:'gpt-6-astra'}]});delete modelPrices.daily`);assert.equal(run('resolvedPrice("daily")'),undefined);
 assert.equal(run('resolvedPrice("unknown")'),undefined);assert.equal(run('validPrice({input:4,cached:null,output:20})'),false);
});
test('today uses Beijing midnight independent of browser timezone',()=>{const run=runtime();assert.equal(run('new Date(beijingStart(Date.parse("2026-11-06T01:00:00Z"))).toISOString()'),'2026-11-05T16:00:00.000Z');run('globalThis.snapshot={logs:{recent:[],active:[],analytics:{now:"2026-11-06T01:00:00Z",buckets:[{started_at:"2026-11-05T15:55:00Z",model:"a",requests:99},{started_at:"2026-11-05T16:00:00Z",model:"a",requests:3}]}}}');assert.equal(run('dashboardData(snapshot).total.requests'),3)});
test('cache is subtracted from regular input, and zero price is known',()=>{const run=runtime();run('globalThis.row={model:"a",usage_count:1,tokens:{input_tokens:1000000,cached_tokens:800000,output_tokens:100000}}');assert.equal(run('modelCost(row,{a:{input:10,cached:1,output:20}})'),4.8);assert.equal(run('modelCost(row,{a:{input:0,cached:0,output:0}})'),0);assert.equal(run('modelCost(row,{})'),null);assert.equal(run('modelCost({...row,usage_count:0},{a:{input:0,cached:0,output:0}})'),null)});
test('partial prices never produce a misleading total',()=>{const run=runtime();assert.equal(run('modelCost({model:"a",usage_count:1,tokens:{}},{a:{input:2,output:8}})'),null)});
test('legacy overview is explicitly partial and does not borrow process totals',()=>{const run=runtime();run('globalThis.snapshot={requests:999,logs:{active:[],recent:[{model:"a",started_at:"2026-11-06T01:00:00Z",attempts:3,status:"completed",first_output_ms:200}]}}');assert.equal(run('dashboardData(snapshot,Date.parse("2026-11-06T02:00:00Z")).partial'),true);assert.equal(run('dashboardData(snapshot,Date.parse("2026-11-06T02:00:00Z")).total.requests'),1);assert.equal(run('dashboardData(snapshot,Date.parse("2026-11-06T02:00:00Z")).total.retries'),2)});
test('series includes empty buckets without counting out-of-window requests',()=>{const run=runtime();assert.equal(run('JSON.stringify(series([{started_at:new Date(300000).toISOString(),requests:7},{started_at:new Date(900000).toISOString(),requests:99}],0,900000,300000).map(b=>b.requests))'),'[0,7,0]')});
