"""Run only against an isolated CPA process and a local, TLS mock of AR.
No production credentials or real provider traffic are used.
"""
import argparse, shutil, tempfile, http.server, json, os, pathlib, re, socket, socketserver, ssl, subprocess, threading, time, urllib.request, urllib.error
import yaml

ROOT = None
SCENARIO = 'success'
COUNTS = {}
CAPTURE = []
KEY_CAPTURE = []
STARTED = threading.Event()
RELEASE = threading.Event()
PRICING_DIAGNOSTIC = 'model ar-mock-model tiered expr run failed: expr compile error: unknown name cc12h (1:62)\n| tier("small", cc12h * 25)\n[trace_id=trace-pricing-integration]'

def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        if self.path=='/api/pricing':
            assert not self.headers.get('Authorization') and not self.headers.get('Cookie')
            payload={'success':True,'group_ratio':{'default':1},'data':[{'model_name':'ar-mock-model','quota_type':0,'model_ratio':2,'completion_ratio':5,'enable_groups':['default']}]}
            self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps(payload).encode());return
        assert self.path=='/v1/models'
        assert self.headers.get('Authorization') in ('Bearer mock-secret','Bearer mock-second')
        self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"data":[{"id":"ar-mock-model"}]}')
    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        assert self.headers.get('Authorization') in ('Bearer mock-secret','Bearer mock-second')
        assert 'codex' in self.headers.get('User-Agent', '').lower()
        if globals().get('DEFAULT_HEADERS_REQUIRED'):
            assert self.headers.get('User-Agent')=='codex_exec/0.159.3 (Windows 10.0.19045; x86_64) dumb (codex_exec; 0.159.3)'
            assert self.headers.get('Originator')=='codex_exec'
        assert data['model']=='ar-mock-model', data['model']
        scenario = data.get('metadata', {}).get('test_scenario', SCENARIO)
        COUNTS[scenario] = COUNTS.get(scenario, 0) + 1
        CAPTURE.append((scenario, data))
        KEY_CAPTURE.append((scenario, self.headers.get('Authorization')))
        attempt = COUNTS[scenario]
        if scenario == 'limit' and attempt == 1:
            self.send_response(429); self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(b'{"error":{"code":"rate_limit_exceeded"}}'); return
        if scenario in ('decrypt','history_http_recover','history_json_recover') and any(x.get('type') == 'reasoning' for x in data.get('input', [])) and (scenario=='decrypt' or attempt<=2):
            self.send_response(400);self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(b'{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"The encrypted content for item rs_old could not be verified. [trace_id=history-integration]"}}'); return
        if scenario == 'auth':
            self.send_response(401);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"error":{"code":"invalid_api_key"}}');return
        if scenario.startswith('pricing_http_'):
            status=int(scenario.split('_')[2])
            self.send_response(status);self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(json.dumps({'error':{'code':'server_error','message':PRICING_DIAGNOSTIC}}).encode());return
        terminal = None
        if scenario.startswith('anon-tool-first-'):
            suffix=scenario.rsplit('-',1)[1]
            terminal={'id':'resp_anon_'+suffix,'object':'response','status':'completed','model':data['model'],'output':[{'type':'function_call','id':'fc_anon_'+suffix,'call_id':'call_anon_'+suffix,'name':'clock','arguments':'{}'}]}
        elif scenario.startswith(('content_filter', 'max_output_tokens')):
            reason = 'content_filter' if scenario.startswith('content_filter') else 'max_output_tokens'
            terminal = {'id':'resp_mock','object':'response','status':'incomplete','model':data['model'],
                        'incomplete_details':{'reason':reason},'output':[]}
        elif scenario.startswith('desktop_tools'):
            definition = data['input'][0]
            assert definition['type'] == 'additional_tools'
            assert definition['tools'][0]['name'] == 'cpa_ar_collaboration'
            assert data['input'][1]['content'] == 'collaboration.wait_agent protocol test'
            terminal = {'id':'resp_mock','object':'response','status':'completed','model':data['model'],
                        'output':[{'type':'function_call','namespace':'cpa_ar_collaboration','name':'wait_agent',
                                   'call_id':'call_mock','arguments':'{"timeout_ms":10000}'}]}
        if terminal is not None and not data.get('stream'):
            self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(json.dumps(terminal).encode());return
        if scenario=='history_json_recover' and not data.get('stream'):
            self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(json.dumps({'id':'resp_mock','object':'response','status':'completed','model':data['model'],'output':[],'usage':{'input_tokens':25,'output_tokens':4,'input_tokens_details':{'cached_tokens':20}}}).encode());return
        if not data.get('stream'):
            self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
            if scenario=='json-error-200':self.wfile.write(b'{"error":{"code":"invalid_api_key","message":"mock-secret rejected"}}');return
            if scenario=='json-invalid-200':self.wfile.write(b'<html>temporary gateway error</html>');return
            self.wfile.write(json.dumps({'id':'resp_mock','object':'response','status':'completed','model':data['model'],'output':[{'type':'message','role':'assistant','content':[{'type':'output_text','text':'OK'}]}]}).encode());return
        self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
        def event(value):
            raw=('data: '+json.dumps(value)+'\n\n').encode()
            self.wfile.write(raw);self.wfile.flush()
        event({'type':'response.created','response':{'id':(terminal or {}).get('id','resp_mock'),'status':'in_progress'}})
        if scenario in ('capacity_recover','capacity_exhausted','capacity_after_output') and (scenario!='capacity_recover' or attempt<=2):
            if scenario=='capacity_after_output':event({'type':'response.output_text.delta','delta':'Keep capacity output'})
            event({'type':'response.failed','response':{'status':'failed','error':{'code':'no_capacity','message':'No capacity available'}}});return
        if scenario in ('history_sse_recover','history_after_output') and (scenario=='history_after_output' or attempt<=2):
            if scenario=='history_after_output':event({'type':'response.output_text.delta','delta':'Keep history output'})
            event({'type':'response.failed','response':{'status':'failed','error':{'code':'invalid_encrypted_content','message':'The encrypted content for item rs_old could not be verified. [trace_id=history-integration]'}}});return
        if scenario in ('pricing_sse_before','pricing_sse_after','pricing_sse_error'):
            if scenario=='pricing_sse_after':event({'type':'response.output_text.delta','delta':'Keep this output'})
            detail={'code':'server_error','message':PRICING_DIAGNOSTIC}
            event({'type':'error','error':detail} if scenario=='pricing_sse_error' else {'type':'response.failed','response':{'id':'resp_mock','status':'failed','error':detail}})
            return
        if terminal is not None:
            if scenario == 'content_filter_after':
                event({'type':'response.output_text.delta','delta':'Filtered response'})
            if scenario.startswith('desktop_tools'):
                event({'type':'response.output_item.added','item':terminal['output'][0]})
            event({'type':'response.'+terminal['status'],'response':terminal});return
        if scenario == 'disconnect' and attempt == 1: return
        if scenario == 'hold' or scenario.startswith('slot-'):
            STARTED.set();RELEASE.wait(20)
        if scenario == 'cancel':
            STARTED.set();time.sleep(12);return
        event({'type':'response.output_text.delta','delta':'OK'})
        if scenario == 'after_output': return
        event({'type':'response.completed','response':{'id':'resp_mock','object':'response','status':'completed','model':data['model'],'output':[{'type':'message','role':'assistant','content':[{'type':'output_text','text':'OK'}]}],'usage':{'input_tokens':10,'output_tokens':1,'total_tokens':11}}})

OTHER_COUNTS={}
class OtherUpstream(http.server.BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_POST(self):
        assert self.headers.get('Authorization')=='Bearer mock-other'
        assert not self.headers.get('X-Cpa-Agentrouter-Continuity-Admission'), 'internal marker leaked'
        data=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        scenario=data.get('metadata',{}).get('test_scenario','other')
        OTHER_COUNTS[scenario]=OTHER_COUNTS.get(scenario,0)+1
        self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
        for value in [{'id':'other-response','object':'chat.completion.chunk','model':data['model'],'choices':[{'index':0,'delta':{'role':'assistant','content':'OTHER'},'finish_reason':None}]},{'id':'other-response','object':'chat.completion.chunk','model':data['model'],'choices':[{'index':0,'delta':{},'finish_reason':'stop'}]}]:
            self.wfile.write(('data: '+json.dumps(value)+'\n\n').encode())
        self.wfile.write(b'data: [DONE]\n\n');self.wfile.flush()

class Proxy(socketserver.BaseRequestHandler):
    def handle(self):
        buf=b''
        while b'\r\n\r\n' not in buf:
            part=self.request.recv(1024)
            if not part:return
            buf+=part
        if not buf.startswith(b'CONNECT agentrouter.org:443 '):
            self.request.sendall(b'HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n');return
        self.request.sendall(b'HTTP/1.1 200 Connection established\r\n\r\n')
        try:
            with tls.wrap_socket(self.request,server_side=True) as conn:
                Upstream(conn,self.client_address,self.server)
        except (ssl.SSLError,BrokenPipeError,ConnectionResetError):pass

class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address=True
    daemon_threads=True

if __name__ == '__main__':
    parser=argparse.ArgumentParser(description='使用隔离的 Linux CPA 与本地 TLS 模拟上游验证插件')
    parser.add_argument('--cpa-binary',required=True,help='Linux CPA 可执行文件的绝对路径')
    parser.add_argument('--plugin',required=True,help='待验证的 Linux amd64 .so 路径')
    parser.add_argument('--previous-plugin',help='可选的旧版 .so，用于热升级验证')
    parser.add_argument('--work-parent',help='测试临时目录的父目录')
    parser.add_argument('--hold-ui',action='store_true',help='完成测试后保留界面供人工检查')
    parser.add_argument('--expected-version',help='预期插件版本，默认读取当前源码')
    args=parser.parse_args()
    expected_version=args.expected_version or re.search(r'const version = "([^"]+)"', (pathlib.Path(__file__).resolve().parents[2]/'cmd/ar-continuity/plugin.go').read_text(encoding='utf-8'))[1]
    ROOT=pathlib.Path(tempfile.mkdtemp(prefix='cpa-agentrouter-continuity-test-',dir=args.work_parent))
    print('TEST_WORKSPACE',str(ROOT),flush=True)
    plugins=ROOT/'plugins/linux/amd64';plugins.mkdir(parents=True)
    first=args.previous_plugin or args.plugin
    current_plugin=plugins/f'cpa-agentrouter-continuity-v{expected_version}.so'
    shutil.copy2(first,plugins/pathlib.Path(first).name if args.previous_plugin else current_plugin)
    cert=ROOT/'mock-cert.pem'; key=ROOT/'mock-key.pem'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-days','1','-subj','/CN=agentrouter.org','-addext','subjectAltName=DNS:agentrouter.org'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    key.chmod(0o600)
    tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);tls.load_cert_chain(cert,key)
    mock=Server(('127.0.0.1',0),Proxy);threading.Thread(target=mock.serve_forever,daemon=True).start()
    other=http.server.ThreadingHTTPServer(('127.0.0.1',0),OtherUpstream);threading.Thread(target=other.serve_forever,daemon=True).start()
    listen=port(); auth=ROOT/'auths';auth.mkdir(exist_ok=True)
    credential={'type':'cpa-agentrouter-continuity','id':'ar-mock-only','api_key':'mock-secret','base_url':'https://agentrouter.org/v1','headers':{'User-Agent':'codex_exec/test'},'models':[{'ID':'ar-mock-model','Name':'ar-mock-model','Object':'model','OwnedBy':'cpa-agentrouter-continuity','Type':'codex','UserDefined':True}], 'max_retries':2,'model_prefix':'ar','priority':10}
    (auth/'ar-mock.json').write_text(json.dumps(credential));(auth/'ar-mock.json').chmod(0o600)
    cfg={'config-version':8,'server':{'host':'127.0.0.1','port':listen},'access':{'api-keys':['mock-client','mock-isolated-client']},'oauth':{'auth-dir':str(auth)},'requests':{'proxy-url':'http://127.0.0.1:'+str(mock.server_address[1])},'routing':{'cooldown':{'disable-cooling':True},'retry':{'request-retry':0}},'plugins':{'enabled':True,'dir':str(ROOT/'plugins'),'configs':{'cpa-agentrouter-continuity':{'enabled':True}}},'management':{'allow-remote':False,'secret-key':'mock-management','disable-control-panel':True}}
    cfg['api-keys']={'openai-compatibility':[{'name':'mock-other','base-url':'http://127.0.0.1:'+str(other.server_port)+'/v1','keys':[{'api-key':'mock-other','proxy-url':'direct'}],'models':[{'name':'ar-mock-model'},{'name':'other-only'}]}]}
    config=ROOT/'config.yaml';config.write_text(yaml.safe_dump(cfg));config.chmod(0o600)
    log=open(ROOT/'isolated.log','w');env=dict(os.environ,SSL_CERT_FILE=str(cert),NO_PROXY='127.0.0.1,localhost',no_proxy='127.0.0.1,localhost')
    process=subprocess.Popen([args.cpa_binary,'-config',str(config),'-local-model'],cwd=ROOT,env=env,stdout=log,stderr=log)
    op=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    def get(path):
        req=urllib.request.Request('http://127.0.0.1:'+str(listen)+path,headers={'Authorization':'Bearer mock-client'})
        return op.open(req,timeout=3).read()
    def manage(path,body=None,method='GET',key='mock-management'):
        req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v0/management/plugins/cpa-agentrouter-continuity'+path,data=None if body is None else json.dumps(body).encode(),method=method,headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'})
        with op.open(req,timeout=10) as response:return json.loads(response.read())
    def request(scenario,stream=True):
        body={'model':'ar/ar-mock-model','input':[{'type':'message','role':'user','content':'Reply OK'}],'stream':stream,'metadata':{'test_scenario':scenario}}
        if scenario.startswith('desktop_tools'):
            body['input']=[{'type':'additional_tools','id':'at_mock','role':'developer','tools':[
                {'type':'namespace','name':'collaboration','tools':[{'type':'function','name':'wait_agent',
                 'parameters':{'type':'object','properties':{'timeout_ms':{'type':'integer'}}}}]}]},
                {'role':'user','content':'collaboration.wait_agent protocol test'}]
        if scenario=='decrypt' or scenario.startswith('history_'):body['input'].insert(0,{'type':'reasoning','id':'rs_old','encrypted_content':'bad'})
        req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','Session_id':'test-session-'+scenario})
        return op.open(req,timeout=45).read().decode()
    try:
        for _ in range(80):
            try:
                models=json.loads(get('/v1/models'));ids=[m['id'] for m in models['data']]
                if 'ar/ar-mock-model' in ids:break
            except Exception:pass
            if process.poll() is not None:raise RuntimeError('isolated CPA exited')
            time.sleep(.25)
        else:raise RuntimeError('plugin model not published: '+str(ids))
        print('ISOLATED_READY',process.pid,'models',ids,flush=True)
        if args.previous_plugin:
            credential['max_concurrent_requests']=1
            (auth/'ar-mock.json').write_text(json.dumps(credential));time.sleep(1)
            response=[]
            holding=threading.Thread(target=lambda:response.append(request('hold')));holding.start();assert STARTED.wait(5)
            shutil.copy2(args.plugin,current_plugin)
            cfg['plugins']['configs']['cpa-agentrouter-continuity']['priority']=1
            config.write_text(yaml.safe_dump(cfg))
            for _ in range(60):
                try:
                    status=manage('/overview')
                    if status['version']==expected_version:break
                except Exception:pass
                time.sleep(.2)
            else:raise AssertionError('new plugin version not active')
            queued=[]
            waiter=threading.Thread(target=lambda:queued.append(request('hold')));waiter.start()
            for _ in range(50):
                snapshot=manage('/overview')
                if any(r['status']=='queued' for r in snapshot['logs']['active']):break
                time.sleep(.1)
            else:raise AssertionError('new generation did not count the old in-flight request')
            assert COUNTS.get('hold')==1,'hot upgrade bypassed per-session cap'
            RELEASE.set();holding.join(10);waiter.join(10)
            assert queued and 'response.completed' in queued[0]
            assert response and 'response.completed' in response[0] and process.poll() is None
            print('PASS binary-hot-upgrade-with-active-stream',flush=True)
            STARTED.clear();RELEASE.clear()
            credential.pop('max_concurrent_requests',None)
            (auth/'ar-mock.json').write_text(json.dumps(credential));time.sleep(1)
        assert manage('/overview')['version']==expected_version
        print('PASS loaded-version',expected_version,flush=True)
        # New installs need no explicit User-Agent or Originator fields.
        credential['headers']={}
        (auth/'ar-mock.json').write_text(json.dumps(credential))
        time.sleep(1)
        DEFAULT_HEADERS_REQUIRED=True
        # DSH's generic Responses transport sends no Codex session header.
        for carrier in ('both', 'header', 'body'):
            session='harness-session-'+carrier
            for turn in range(2):
                scenario='harness-'+carrier+'-'+str(turn)
                items=[{'role':'user','content':'Reply OK'}]
                if turn:items += [{'role':'assistant','content':'OK'},{'role':'user','content':'Continue'}]
                body={'model':'ar/ar-mock-model','input':items,'stream':True,'store':False,'metadata':{'test_scenario':scenario}}
                headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','User-Agent':'deepseek-harness/0.2.2-rc.1'}
                if carrier != 'body':headers['X-Client-Request-Id']=session
                if carrier != 'header':body['prompt_cache_key']=session
                req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers=headers)
                response=op.open(req,timeout=20).read()
                assert b'response.completed' in response and COUNTS.get(scenario)==1,(carrier,turn,response)
            print('PASS harness-session-'+carrier+' first turn and continuation',flush=True)
        for scenario,agent,header,cache,items in [
            ('harness-conflict','deepseek-harness/0.2.2-rc.1','harness-conflict-one','harness-conflict-two',[{'role':'user','content':'Hello'}]),
            ('harness-unknown-history','deepseek-harness/0.2.2-rc.1','harness-unknown-history','harness-unknown-history',[{'role':'assistant','content':'Previous'},{'role':'user','content':'Continue'}]),
            ('anonymous-history','Trae/test','','',[{'role':'assistant','content':'Previous'},{'role':'user','content':'Continue'}]),
        ]:
            body={'model':'ar/ar-mock-model','input':items,'stream':True,'metadata':{'test_scenario':scenario}}
            if cache:body['prompt_cache_key']=cache
            headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','User-Agent':agent}
            if header:headers['X-Client-Request-Id']=header
            req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers=headers)
            try:response=op.open(req,timeout=20).read()
            except urllib.error.HTTPError as error:assert b'ar_session_ineligible' in error.read(),scenario
            else:assert b'ar_session_ineligible' in response,scenario
            assert not COUNTS.get(scenario),'ineligible request reached upstream: '+scenario
            print('PASS '+scenario+' rejected before upstream',flush=True)
        # Model setup probes have no conversation yet. Keep them stateless.
        for agent in ['Trae/test','deepseek-harness/0.2.2-rc.1','ordinary-client']:
            for streaming in [True,False]:
                scenario='probe-'+agent.split('/')[0]+'-'+str(streaming)
                before_origins=json.loads((ROOT/'plugins/cpa-agentrouter-continuity-sessions.json').read_text())['origins']
                body={'model':'ar/ar-mock-model','input':'Reply OK','stream':streaming,'metadata':{'test_scenario':scenario}}
                headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','User-Agent':agent}
                if agent=='ordinary-client':headers['X-Client-Request-Id']='request-only'
                req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers=headers)
                response=op.open(req,timeout=20).read().decode()
                assert ('response.completed' in response) if streaming else json.loads(response)['status']=='completed'
                assert COUNTS[scenario]==1
                after_origins=json.loads((ROOT/'plugins/cpa-agentrouter-continuity-sessions.json').read_text())['origins']
                assert after_origins==before_origins,'anonymous probe retained origin state'
                print('PASS',scenario,'no stable ID, no persisted origin',flush=True)
        temporary_second=auth/'ar-anonymous-second.json'
        temporary_second.write_text(json.dumps(dict(credential,id='ar-anonymous-second',api_key='mock-second')));time.sleep(1)
        for mode in ['sse','json']:
            def anonymous(body,scenario,key='mock-client'):
                payload={'model':'ar/ar-mock-model','input':body,'stream':mode=='sse','metadata':{'test_scenario':scenario}}
                req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(payload).encode(),headers={'Authorization':'Bearer '+key,'Content-Type':'application/json','User-Agent':'Trae/test'})
                return op.open(req,timeout=20).read().decode()
            first='anon-tool-first-'+mode
            response=anonymous('What time is it?',first)
            assert 'call_anon_'+mode in response,response
            original_key=next(key for name,key in KEY_CAPTURE if name==first)
            anonymous('independent probe','anon-noise-'+mode)
            call={'type':'function_call','id':'fc_anon_'+mode,'call_id':'call_anon_'+mode,'name':'clock','arguments':'{}'}
            history=[{'role':'user','content':'What time is it?'},call,{'type':'function_call_output','call_id':'call_anon_'+mode,'output':'16:30:31'}]
            result='anon-tool-result-'+mode
            response=anonymous(history,result)
            assert 'completed' in response,response
            assert next(key for name,key in KEY_CAPTURE if name==result)==original_key,'tool result changed Key'
            history += [{'role':'assistant','content':'It is 16:30:31'},{'role':'user','content':'Continue'}]
            follow='anon-follow-'+mode
            assert 'completed' in anonymous(history,follow)
            assert next(key for name,key in KEY_CAPTURE if name==follow)==original_key
            try:anonymous(history,'anon-other-caller-'+mode,'mock-isolated-client')
            except urllib.error.HTTPError as error:assert b'ar_session_ineligible' in error.read()
            else:raise AssertionError('anonymous continuation crossed caller scope')
            assert not COUNTS.get('anon-other-caller-'+mode)
            stored=(ROOT/'plugins/cpa-agentrouter-continuity-sessions.json.continuity').read_text()
            assert 'call_anon_' not in stored and '16:30:31' not in stored and 'mock-secret' not in stored
            print('PASS anonymous Trae',mode,'tool/result/follow-up pinned across two Keys; caller isolated; hashed storage',flush=True)
        diagnostics=manage('/admission-diagnostics')
        assert diagnostics['version']==expected_version
        assert 0<len(diagnostics['entries'])<=256
        assert any(x['stage']=='output' and any(r['kind']=='call' for r in x['references']) for x in diagnostics['entries'])
        assert any(x['stage']=='input' and x.get('recovered') for x in diagnostics['entries'])
        serialized=json.dumps(diagnostics)
        assert all(secret not in serialized for secret in ['call_anon_','mock-secret','mock-client','What time is it?'])
        try:manage('/admission-diagnostics',key='mock-client')
        except urllib.error.HTTPError as error:assert error.code in (401,403)
        else:raise AssertionError('admission diagnostics exposed without management authentication')
        try:get('/v0/resource/plugins/cpa-agentrouter-continuity/admission-diagnostics')
        except urllib.error.HTTPError as error:assert error.code==404
        else:raise AssertionError('admission diagnostics exposed as public resource')
        print('PASS bounded hashed admission diagnostics; management authentication enforced',flush=True)
        temporary_second.unlink();time.sleep(1)
        for scenario in ['json-error-200','json-invalid-200']:
            try:request(scenario,False)
            except urllib.error.HTTPError as error:
                response=error.read().decode();assert error.code>=400 and 'mock-secret' not in response
            else:raise AssertionError('HTTP 200 error envelope accepted as success')
            assert COUNTS.get(scenario)==(1 if scenario=='json-error-200' else 3),(scenario,COUNTS.get(scenario),response)
            print('PASS',scenario,'reported failure, no credential echo',flush=True)
        for scenario,attempts,success in [('success',1,True),('limit',2,True),('decrypt',3,False),('disconnect',2,True),('after_output',1,False),('auth',1,False)]:
            if scenario=='decrypt':
                body={'model':'ar/ar-mock-model','input':'start this conversation','stream':True,'metadata':{'test_scenario':'decrypt-start'}}
                req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','Session_id':'test-session-decrypt'})
                assert b'response.completed' in op.open(req,timeout=20).read()
            response=request(scenario)
            assert COUNTS.get(scenario)==attempts,(scenario,COUNTS,response)
            assert ('response.completed' in response)==success,(scenario,response)
            assert response.count('"delta": "OK"')<=1,(scenario,response)
            print('PASS',scenario,'attempts',attempts,'default_headers=True',flush=True)
        history=[d for s,d in CAPTURE if s=='decrypt'];assert any(x.get('type')=='reasoning' for x in history[0]['input']);assert len(history)==3 and all(d==history[0] for d in history)
        for scenario,stream in [('history_http_recover',True),('history_sse_recover',True),('history_json_recover',False),('history_after_output',True)]:
            start={'model':'ar/ar-mock-model','input':'start this conversation','stream':True,'metadata':{'test_scenario':'start-'+scenario}}
            req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(start).encode(),headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','Session_id':'test-session-'+scenario})
            assert b'response.completed' in op.open(req,timeout=20).read()
            response=request(scenario,stream)
            attempts=1 if scenario=='history_after_output' else 3
            assert COUNTS.get(scenario)==attempts,(scenario,COUNTS,response)
            sent=[d for name,d in CAPTURE if name==scenario]
            assert len(sent)==attempts and all(d==sent[0] for d in sent)
            if scenario=='history_after_output':
                assert 'ar_history_verification_failed' in response and response.count('Keep history output')==1
            else:
                assert all(text not in response for text in ['response.failed','ar_history_verification_failed','history-integration']),response
                assert 'response.completed' in response if stream else json.loads(response)['status']=='completed'
                if stream:
                    events=[json.loads(line[5:]) for line in response.splitlines() if line.startswith('data:') and line[5:].strip()!='[DONE]']
                    assert not any(e.get('type')=='error' for e in events)
            print('PASS',scenario,'one client request, internal attempts',attempts,'history preserved',flush=True)
        for scenario,wanted in [('capacity_recover',3),('capacity_exhausted',3),('capacity_after_output',1)]:
            response=request(scenario)
            assert COUNTS.get(scenario)==wanted,(scenario,COUNTS)
            if scenario=='capacity_recover':assert 'response.completed' in response and 'no_capacity' not in response
            else:assert 'no_capacity' in response
            if scenario=='capacity_after_output':assert 'Keep capacity output' in response
            captures=[data for name,data in CAPTURE if name==scenario]
            assert all(data==captures[0] for data in captures)
            record=next(r for r in reversed(manage('/overview')['logs']['recent']) if r['model']=='ar/ar-mock-model')
            if scenario=='capacity_recover':assert record['retry_reasons']==['capacity','capacity'],record
            print('PASS',scenario,'attempts',wanted,'same history and capacity backoff',flush=True)
        analytics=manage('/overview')['logs']['analytics']
        assert analytics['bucket_minutes']==5
        buckets=analytics['buckets']
        assert sum(b['usage_count'] for b in buckets)>=5
        assert sum(b['tokens']['cached_tokens'] for b in buckets)==20
        assert sum(b['tokens']['input_tokens'] for b in buckets)>=65
        assert sum(b['recovered'] for b in buckets)>=3
        print('PASS dashboard numeric usage from SSE and JSON, cached input, and internal recovery aggregates',flush=True)
        for scenario,stream in [('content_filter_before',True),('content_filter_after',True),('content_filter_json',False)]:
            try:response=request(scenario,stream)
            except urllib.error.HTTPError as error:
                assert not stream and error.code==400,(scenario,error.code)
                response=error.read().decode()
            else:assert stream,'JSON content_filter must return an HTTP error'
            assert COUNTS.get(scenario)==1,(scenario,COUNTS)
            assert 'content_filter' in response and 'response.incomplete' not in response,(scenario,response)
            if stream:
                events=[json.loads(line[5:]) for line in response.splitlines() if line.startswith('data:') and line[5:].strip()!='[DONE]']
                # CPA may normalize the plugin's response.failed into an error event.
                errors=[v for v in events if v.get('type') in ('error','response.failed')]
                assert len(errors)==1,response
                error=errors[0].get('error') or errors[0].get('response',{}).get('error',{})
                assert error.get('code')=='ar_content_filter',response
            else:
                # CPA 8.0.34 keeps HTTP 400 and the reason but omits the JSON code field.
                error=json.loads(response)['error']
                assert error['type']=='invalid_request_error' and 'content_filter' in error['message']
            if scenario=='content_filter_after':assert 'Filtered response' in response
            print('PASS',scenario,'attempts=1 explicit terminal error',flush=True)
        for stream in [True,False]:
            scenario='max_output_tokens' if stream else 'max_output_tokens_json'
            response=request(scenario,stream)
            assert COUNTS.get(scenario)==1 and 'max_output_tokens' in response and 'ar_content_filter' not in response
            assert ('response.incomplete' in response) if stream else json.loads(response)['status']=='incomplete'
            print('PASS',scenario,'normal incomplete response preserved',flush=True)
            scenario='desktop_tools_stream' if stream else 'desktop_tools_json'
            response=request(scenario,stream)
            assert COUNTS.get(scenario)==1 and 'cpa_ar_collaboration' not in response
            values=[json.loads(line[5:]) for line in response.splitlines() if line.startswith('data:') and line[5:].strip()!='[DONE]'] if stream else [json.loads(response)]
            final=next(v['response'] for v in values if v.get('type')=='response.completed') if stream else values[0]
            call=final['output'][0]
            assert call['namespace']=='collaboration' and call['name']=='wait_agent' and call['call_id']=='call_mock'
            assert json.loads(call['arguments'])=={'timeout_ms':10000}
            if stream:
                added=next(v['item'] for v in values if v.get('type')=='response.output_item.added')
                assert added==call
            print('PASS',scenario,'desktop admission and tool alias round trip',flush=True)
        recent=manage('/overview')['logs']['recent']
        filtered=[r for r in recent if r.get('error_code')=='ar_content_filter']
        assert len(filtered)==3 and all(r['status']=='failed' and r['attempts']==1 for r in filtered)
        print('PASS content_filter request logs',flush=True)
        pricing_cases=[(f'pricing_http_{status}_{mode}',mode=='stream') for status in (400,500) for mode in ('stream','json')]
        pricing_cases += [(name,True) for name in ('pricing_sse_before','pricing_sse_after','pricing_sse_error')]
        for scenario,stream in pricing_cases:
            try:response=request(scenario,stream)
            except urllib.error.HTTPError as error:
                assert not stream and error.code==400,(scenario,error.code)
                response=error.read().decode()
            else:assert stream,'JSON pricing errors must be terminal HTTP 400'
            assert COUNTS.get(scenario)==1,(scenario,COUNTS)
            assert all(value in response for value in ['上游模型计费规则异常','cc12h','trace-pricing-integration']),(scenario,response)
            if stream:
                events=[json.loads(line[5:]) for line in response.splitlines() if line.startswith('data:') and line[5:].strip()!='[DONE]']
                failures=[v for v in events if v.get('type') in ('error','response.failed')]
                assert len(failures)==1,(scenario,response)
                detail=failures[0].get('error') or failures[0].get('response',{}).get('error',{})
                assert detail['code']=='ar_upstream_pricing_error' and detail['http_status']==400
            else:assert json.loads(response)['error']['type']=='invalid_request_error'
            if scenario=='pricing_sse_after':assert response.count('Keep this output')==1
            print('PASS',scenario,'attempts=1 pricing diagnostic preserved',flush=True)
        rows=manage('/overview')['logs']['recent']
        pricing=[r for r in rows if r.get('error_code')=='ar_upstream_pricing_error']
        assert len(pricing)==len(pricing_cases) and all(r['status']=='failed' and r['attempts']==1 and not r['retry_reasons'] for r in pricing)
        for _ in range(20):
            if not any(manage('/overview')['routing']['active_sessions'].values()):break
            time.sleep(.1)
        else:raise AssertionError('pricing failures leaked session slots')
        assert 'response.completed' in request('pricing_recovered') and COUNTS.get('pricing_recovered')==1
        print('PASS pricing errors logged, session slots released, next normal request succeeds',flush=True)
        for key in ['', 'mock-client', 'invalid-management']:
            try:manage('/overview',key=key)
            except urllib.error.HTTPError as error:assert error.code in (401,403)
            else:raise AssertionError('management API accepted wrong key')
        print('PASS management-auth-required',flush=True)
        # Keep combined failures below the host's per-IP management-key ban threshold.
        for key in ['invalid-management']:
            try:manage('/pricing',key=key)
            except urllib.error.HTTPError as error:assert error.code in (401,403)
            else:raise AssertionError('pricing API accepted wrong key')
        channel=manage('/pricing')
        assert channel['currency']=='USD' and channel['unit']=='million_tokens' and channel['group']=='default'
        assert channel['prices']['ar-mock-model']=={'input':4,'cached':4,'output':20,'cache_assumed':True}
        assert manage('/overview')['logs']['analytics']['credential_scoped'] is True
        for asset in ['monitoring.js','monitoring.css','pricing.js']:
            assert get('/v0/resource/plugins/cpa-agentrouter-continuity/'+asset)==(pathlib.Path(__file__).resolve().parents[2]/'web'/asset).read_bytes()
        print('PASS channel-pricing auth, public-fetch without credentials, conversion, scoped aggregates and embedded assets',flush=True)
        catalog=manage('/credentials');assert len(catalog['credentials'])==1
        item=catalog['credentials'][0];assert 'mock-secret' not in json.dumps(catalog)
        catalog=manage('/models',{'auth_index':item['auth_index']},'POST')
        assert catalog['models']==['ar-mock-model'] and 'mock-secret' not in json.dumps(catalog)
        catalog=manage('/models',{'api_key':'mock-secret'},'POST');assert catalog['count']==1
        print('PASS catalog-fetch stored-key and draft-key',flush=True)
        edit={'auth_index':item['auth_index'],'label':'UI validation <test>','api_key':'','disabled':False,'model_prefix':'ar','models':item['models'],'user_agent':'','originator':'','max_retries':2,'retry_budget_seconds':180}
        manage('/credentials',edit,'PUT')
        stored=json.loads((auth/'ar-mock.json').read_text());assert stored['api_key']=='mock-secret'
        assert manage('/health',{'auth_index':item['auth_index']},'POST')['ok'] is True
        snapshot=manage('/overview');assert snapshot['logs']['recent'] and 'mock-secret' not in json.dumps(snapshot)
        assert '连接 CPA'.encode() in get('/v0/resource/plugins/cpa-agentrouter-continuity/ui')
        print('PASS key-edit-preservation health-check request-logs public-login-page',flush=True)
        new=dict(edit);new.pop('auth_index');new.update(api_key='mock-disabled-key',disabled=True,label='Created via API')
        saved=manage('/credentials',new,'POST');assert saved['name'].startswith('cpa-agentrouter-continuity-')
        assert json.loads((auth/saved['name']).read_text())['disabled'] is True
        assert len(manage('/credentials')['credentials'])==2
        print('PASS create-disabled-api-key',flush=True)
        # Prefix is an optional per-credential setting, never a hardcoded model name.
        for prefix,wanted,absent in [('', 'ar-mock-model', 'ar/ar-mock-model'),('ar','ar/ar-mock-model','ar-mock-model')]:
            credential['model_prefix']=prefix
            (auth/'ar-mock.json').write_text(json.dumps(credential))
            for _ in range(60):
                ids=[m['id'] for m in json.loads(get('/v1/models'))['data']]
                if wanted in ids and (prefix=='ar' or absent not in ids):break
                time.sleep(.2)
            else:raise AssertionError(('prefix publication',prefix,ids))
            print('PASS optional-prefix',repr(prefix),'models',ids,flush=True)
        # Mixed-provider regression: old sessions and full AR keys choose other providers.
        credential['model_prefix']='';(auth/'ar-mock.json').write_text(json.dumps(credential));time.sleep(1)
        def mixed(session,scenario,model='ar-mock-model',history=False):
            body={'model':model,'input':[{'role':'user','content':'hello'}],'stream':True,'metadata':{'test_scenario':scenario}}
            if history:body['input'] += [{'role':'assistant','content':'existing answer'},{'role':'user','content':'continue'}]
            req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','Session_id':session})
            return op.open(req,timeout=45).read().decode()
        def ar_attempts():return sum(COUNTS.values())
        def wait_idle():
            for _ in range(60):
                if not manage('/overview')['routing']['active_sessions']:return
                time.sleep(.1)
            raise AssertionError('session reservations were not released')
        baseline=ar_attempts();assert 'OTHER' in mixed('foreign-session','foreign-start','other-only')
        assert 'OTHER' in mixed('foreign-session','foreign-switch')
        assert ar_attempts()==baseline,'non-AR origin entered AR'
        assert 'OTHER' in mixed('unknown-old','unknown-history',history=True)
        assert ar_attempts()==baseline,'unknown history entered AR'
        result=mixed('new-ar-session','new-ar')
        assert 'response.completed' in result and COUNTS.get('new-ar')==1
        result=mixed('new-ar-session','ar-continued',history=True)
        assert COUNTS.get('ar-continued')==1
        print('PASS non-AR-origin excluded, unknown-history excluded, AR-continuation admitted',flush=True)
        queue_edit=dict(edit,model_prefix='',max_concurrent_requests=2)
        manage('/credentials',queue_edit,'PUT');time.sleep(1)
        RELEASE.clear();outputs=[]
        first=threading.Thread(target=lambda:outputs.append(mixed('queue-same','slot-queue-first')));first.start()
        for _ in range(50):
            if COUNTS.get('slot-queue-first'):break
            time.sleep(.1)
        else:raise AssertionError('queue first did not start')
        second=threading.Thread(target=lambda:outputs.append(mixed('queue-same','slot-queue-second')));second.start()
        for _ in range(50):
            if COUNTS.get('slot-queue-second'):break
            time.sleep(.1)
        else:raise AssertionError('same session was incorrectly serialized')
        third=threading.Thread(target=lambda:outputs.append(mixed('queue-other','slot-queue-other')));third.start()
        for _ in range(50):
            active=manage('/overview')['logs']['active']
            current=next(c for c in manage('/credentials')['credentials'] if c['auth_index']==item['auth_index'])
            if any(r['status']=='queued' and r['attempts']==0 for r in active) and current['queued_requests']==1:break
            time.sleep(.1)
        else:raise AssertionError(('request did not queue',active))
        assert current['active_requests']==2 and current['active_sessions']==2,current
        assert not COUNTS.get('slot-queue-other'),'Key cap bypassed'
        time.sleep(.3)
        RELEASE.set()
        for worker in [first,second,third]:worker.join(15)
        assert len(outputs)==3 and all('response.completed' in result for result in outputs)
        assert COUNTS.get('slot-queue-other')==1
        wait_idle()
        assert 'response.completed' in mixed('queue-other','queue-sticky-continued',history=True)
        queue_record=next(r for r in reversed(manage('/overview')['logs']['recent']) if r.get('queue_wait_ms',0)>100)
        assert queue_record['attempts']==1 and queue_record['queue_wait_ms']>=100,queue_record
        # Queue cancellation is observed by stream heartbeats and frees its reservation.
        queue_edit['max_concurrent_requests']=1
        manage('/credentials',queue_edit,'PUT');time.sleep(1)
        RELEASE.clear();outputs=[]
        first=threading.Thread(target=lambda:outputs.append(mixed('queue-cancel','slot-queue-holder')));first.start()
        for _ in range(50):
            if COUNTS.get('slot-queue-holder'):break
            time.sleep(.1)
        else:raise AssertionError('queue cancel holder did not start')
        payload=json.dumps({'model':'ar-mock-model','input':'queue cancel','stream':True,'metadata':{'test_scenario':'slot-queue-canceled'}})
        client=subprocess.Popen(['curl','--noproxy','*','-sS','-H','Authorization: Bearer mock-client','-H','Content-Type: application/json','-H','Session_id: queue-cancel','-d',payload,'http://127.0.0.1:'+str(listen)+'/v1/responses'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for _ in range(50):
            if any(r['status']=='queued' for r in manage('/overview')['logs']['active']):break
            time.sleep(.1)
        else:raise AssertionError('cancel request did not queue')
        client.terminate();client.wait();time.sleep(6)
        assert not COUNTS.get('slot-queue-canceled')
        assert not any(r['status']=='queued' for r in manage('/overview')['logs']['active'])
        payload=json.dumps({'model':'ar-mock-model','input':'cancel JSON','stream':False,'metadata':{'test_scenario':'json-queue-canceled'}})
        client=subprocess.Popen(['curl','--noproxy','*','-sS','-H','Authorization: Bearer mock-client','-H','Content-Type: application/json','-H','Session_id: queue-cancel-json','-d',payload,'http://127.0.0.1:'+str(listen)+'/v1/responses'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for _ in range(50):
            if any(r['status']=='queued' for r in manage('/overview')['logs']['active']):break
            time.sleep(.1)
        else:raise AssertionError('non-stream request did not queue')
        client.terminate();client.wait()
        for _ in range(60):
            if not any(r['status']=='queued' for r in manage('/overview')['logs']['active']):break
            time.sleep(.1)
        else:raise AssertionError('non-stream cancellation retained queued request')
        assert not COUNTS.get('json-queue-canceled')
        print('PASS non-stream queued cancellation releases without upstream call',flush=True)
        RELEASE.set();first.join(15)
        assert len(outputs)==1 and 'response.completed' in outputs[0]
        for field in ['max_concurrent_requests']:
            for invalid in [0,-1,101,1.5]:
                try:manage('/credentials',dict(queue_edit,**{field:invalid}),'PUT')
                except urllib.error.HTTPError as error:assert error.code==400
                else:raise AssertionError('invalid request cap accepted')
        # Legacy session fields do not independently gate admission after migration.
        config_edit=dict(edit,model_prefix='',max_concurrent_requests=3)
        manage('/credentials',config_edit,'PUT');time.sleep(1)
        RELEASE.clear();outputs=[]
        threads=[threading.Thread(target=lambda n=n:outputs.append(mixed('custom-'+str(n),'slot-custom-'+str(n)))) for n in range(3)]
        for worker in threads:worker.start()
        for _ in range(50):
            if all(COUNTS.get('slot-custom-'+str(n))==1 for n in range(3)):break
            time.sleep(.1)
        else:raise AssertionError('three request slots did not start')
        config_edit['max_concurrent_requests']=1
        manage('/credentials',config_edit,'PUT');time.sleep(1)
        assert all(worker.is_alive() for worker in threads),'lowering request cap canceled work'
        RELEASE.set()
        for worker in threads:worker.join(10)
        assert len(outputs)==3 and all('response.completed' in x for x in outputs)
        wait_idle()
        config_edit.pop('max_concurrent_requests')
        manage('/credentials',config_edit,'PUT')
        stored=json.loads((auth/'ar-mock.json').read_text())
        assert stored['max_concurrent_requests']==1 and 'max_concurrent_sessions' not in stored and 'max_concurrent_requests_per_session' not in stored
        credential=stored;credential['max_concurrent_requests']=2
        (auth/'ar-mock.json').write_text(json.dumps(credential));time.sleep(1)
        print('PASS request caps 1/2/3, same-session parallel requests, queue cancellation, reduction and migration',flush=True)
        second=dict(credential,id='ar-mock-second',api_key='mock-second')
        second_path=auth/'ar-mock-second.json'
        second_path.write_text(json.dumps(second));second_path.chmod(0o600);time.sleep(1)
        RELEASE.clear();outputs=[]
        workers=[]
        for n in range(4):
            scenario='slot-balanced-'+str(n)
            worker=threading.Thread(target=lambda n=n:outputs.append(mixed('balanced-'+str(n),'slot-balanced-'+str(n))))
            workers.append(worker);worker.start()
            for _ in range(50):
                if COUNTS.get(scenario)==1:break
                time.sleep(.1)
            else:raise AssertionError('balanced session did not start')
            sequence=[key for name,key in KEY_CAPTURE if name.startswith('slot-balanced-')]
            if len(sequence)>1:assert sequence[-1]!=sequence[-2],sequence
        sequence=[key for name,key in KEY_CAPTURE if name.startswith('slot-balanced-')]
        assert sequence.count('Bearer mock-secret')==2 and sequence.count('Bearer mock-second')==2,sequence
        overflow=threading.Thread(target=lambda:outputs.append(mixed('balanced-overflow','slot-balanced-overflow')));overflow.start()
        time.sleep(.4);assert not COUNTS.get('slot-balanced-overflow')
        workers.append(overflow)
        RELEASE.set()
        for worker in workers:worker.join(10)
        assert len(outputs)==5 and all('response.completed' in result for result in outputs)
        print('PASS two Keys distribute active sessions alternately 2/2',flush=True)
        pinned_path=auth/'ar-mock.json' if sequence[0]=='Bearer mock-secret' else second_path
        pinned=json.loads(pinned_path.read_text())
        pinned_path.write_text(json.dumps(dict(pinned,disabled=True)));time.sleep(1)
        baseline=ar_attempts();other_baseline=sum(OTHER_COUNTS.values())
        for model in ['ar-mock-model','other-only']:
            try:mixed('balanced-0','pinned-key-unavailable-'+model,model,history=True)
            except urllib.error.HTTPError as error:assert b'ar_session_ineligible' in error.read()
            else:raise AssertionError('bound conversation switched Key or provider')
        assert ar_attempts()==baseline and sum(OTHER_COUNTS.values())==other_baseline
        pinned_path.write_text(json.dumps(pinned));time.sleep(1)
        assert 'response.completed' in mixed('balanced-0','pinned-key-restored',history=True)
        assert next(key for scenario,key in KEY_CAPTURE if scenario=='pinned-key-restored')==sequence[0]
        def child_request(session,parent,scenario,model='ar-mock-model'):
            body={'model':model,'input':[{'role':'user','content':'original'},{'role':'assistant','content':'inherited history'},{'role':'user','content':'continue'}],'stream':True,'metadata':{'test_scenario':scenario}}
            req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','Session_id':session,'X-Parent-Session-Id':parent})
            return op.open(req,timeout=45).read().decode()
        for scenario,parent,child in [('child-inherits','balanced-0','child-0'),('grandchild-inherits','child-0','grandchild-0')]:
            assert 'response.completed' in child_request(child,parent,scenario)
            assert next(key for name,key in KEY_CAPTURE if name==scenario)==sequence[0]
        assert 'response.completed' in child_request('balanced-1','balanced-0','child-existing-wins')
        assert next(key for name,key in KEY_CAPTURE if name=='child-existing-wins')==sequence[1]
        pinned_path.write_text(json.dumps(dict(pinned,disabled=True)));time.sleep(1)
        try:child_request('child-0','balanced-0','child-pinned-unavailable')
        except urllib.error.HTTPError as error:assert b'ar_session_ineligible' in error.read()
        else:raise AssertionError('child switched Key')
        assert not COUNTS.get('child-pinned-unavailable')
        pinned_path.write_text(json.dumps(pinned));time.sleep(1)
        assert 'response.completed' in mixed('child-0','child-restored',history=True)
        assert next(key for name,key in KEY_CAPTURE if name=='child-restored')==sequence[0]
        print('PASS child/grandchild inherit Key, existing child wins, unavailable Key preserves binding',flush=True)
        second_path.unlink();time.sleep(1)
        print('PASS original Key unavailable rejects, restored Key resumes same conversation',flush=True)
        # State survives a configuration reload, without altering the other provider's routing.
        cfg['plugins']['configs']['cpa-agentrouter-continuity']['priority']=2;config.write_text(yaml.safe_dump(cfg));time.sleep(1)
        assert 'OTHER' in mixed('foreign-session','foreign-after-reload')
        assert not COUNTS.get('foreign-after-reload')
        print('PASS persisted session origin after reload',flush=True)
        credential['model_prefix']='ar';(auth/'ar-mock.json').write_text(json.dumps(credential));time.sleep(1)
        STARTED.clear();RELEASE.clear()
        # Hold a stream while hot-reloading config; verify PID and in-flight work.
        result=[];thread=threading.Thread(target=lambda:result.append(request('hold')));thread.start();assert STARTED.wait(5)
        cfg['plugins']['configs']['cpa-agentrouter-continuity']['priority']=1;config.write_text(yaml.safe_dump(cfg));time.sleep(2);RELEASE.set();thread.join(10)
        assert process.poll() is None and result and 'response.completed' in result[0]
        print('PASS hot-reload-with-active-stream PID',process.pid,flush=True)
        # Cancellation must close the callback scope, with no new upstream attempt.
        STARTED.clear()
        payload=json.dumps({'model':'ar/ar-mock-model','stream':True,'input':'cancel','metadata':{'test_scenario':'cancel'}})
        client=subprocess.Popen(['curl','--noproxy','*','-sS','-H','Authorization: Bearer mock-client','-H','Content-Type: application/json','-H','Session_id: test-session-cancel','-d',payload,'http://127.0.0.1:'+str(listen)+'/v1/responses'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        assert STARTED.wait(5);client.terminate();client.wait();time.sleep(6);assert COUNTS['cancel']==1
        print('PASS downstream-cancellation no-replay',flush=True)
        if args.hold_ui:
            (ROOT.parent/'ui-state.json').write_text(json.dumps({'port':listen,'pid':process.pid,'root':str(ROOT)}))
            print('UI_READY',listen,flush=True)
            while not (ROOT/'stop-ui').exists():time.sleep(1)
    finally:
        RELEASE.set();process.terminate()
        try:process.wait(timeout=8)
        except subprocess.TimeoutExpired:process.kill();process.wait()
        log.close();mock.shutdown();mock.server_close();other.shutdown();other.server_close()
