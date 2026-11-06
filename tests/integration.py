"""Run only against an isolated CPA process and a local, TLS mock of AR.
No production credentials or real provider traffic are used.
"""
import argparse, shutil, tempfile, http.server, json, os, pathlib, re, socket, socketserver, ssl, subprocess, threading, time, urllib.request, urllib.error
import yaml

ROOT = None
SCENARIO = 'success'
COUNTS = {}
CAPTURE = []
STARTED = threading.Event()
RELEASE = threading.Event()

def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        assert self.path=='/v1/models'
        assert self.headers.get('Authorization')=='Bearer mock-secret'
        self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"data":[{"id":"ar-mock-model"}]}')
    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        assert self.headers.get('Authorization') == 'Bearer mock-secret'
        assert 'codex' in self.headers.get('User-Agent', '').lower()
        if globals().get('DEFAULT_HEADERS_REQUIRED'):
            assert self.headers.get('User-Agent')=='codex_exec/0.159.3 (Windows 10.0.19045; x86_64) dumb (codex_exec; 0.159.3)'
            assert self.headers.get('Originator')=='codex_exec'
        assert data['model']=='ar-mock-model', data['model']
        scenario = data.get('metadata', {}).get('test_scenario', SCENARIO)
        COUNTS[scenario] = COUNTS.get(scenario, 0) + 1
        CAPTURE.append((scenario, data))
        attempt = COUNTS[scenario]
        if scenario == 'limit' and attempt == 1:
            self.send_response(429); self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(b'{"error":{"code":"rate_limit_exceeded"}}'); return
        if scenario == 'decrypt' and any(x.get('type') == 'reasoning' for x in data.get('input', [])):
            self.send_response(400);self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(b'{"error":{"type":"invalid_request_error","message":"The encrypted content for item rs_old could not be verified. Reason: Encrypted content could not be decrypted or parsed."}}'); return
        if scenario == 'auth':
            self.send_response(401);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"error":{"code":"invalid_api_key"}}');return
        terminal = None
        if scenario.startswith(('content_filter', 'max_output_tokens')):
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
        self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
        def event(value):
            raw=('data: '+json.dumps(value)+'\n\n').encode()
            self.wfile.write(raw);self.wfile.flush()
        event({'type':'response.created','response':{'id':'resp_mock','status':'in_progress'}})
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
    expected_version=args.expected_version or re.search(r'const version = "([^"]+)"', (pathlib.Path(__file__).resolve().parents[1]/'plugin.go').read_text(encoding='utf-8'))[1]
    ROOT=pathlib.Path(tempfile.mkdtemp(prefix='cpa-agentrouter-continuity-test-',dir=args.work_parent))
    print('TEST_WORKSPACE',str(ROOT),flush=True)
    plugins=ROOT/'plugins/linux/amd64';plugins.mkdir(parents=True)
    first=args.previous_plugin or args.plugin
    current_plugin=plugins/f'cpa-agentrouter-continuity-v{expected_version}.so'
    shutil.copy2(first,plugins/'cpa-agentrouter-continuity-v0.1.0.so' if args.previous_plugin else current_plugin)
    cert=ROOT/'mock-cert.pem'; key=ROOT/'mock-key.pem'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-days','1','-subj','/CN=agentrouter.org','-addext','subjectAltName=DNS:agentrouter.org'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    key.chmod(0o600)
    tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);tls.load_cert_chain(cert,key)
    mock=Server(('127.0.0.1',0),Proxy);threading.Thread(target=mock.serve_forever,daemon=True).start()
    other=http.server.ThreadingHTTPServer(('127.0.0.1',0),OtherUpstream);threading.Thread(target=other.serve_forever,daemon=True).start()
    listen=port(); auth=ROOT/'auths';auth.mkdir(exist_ok=True)
    credential={'type':'cpa-agentrouter-continuity','id':'ar-mock-only','api_key':'mock-secret','base_url':'https://agentrouter.org/v1','headers':{'User-Agent':'codex_exec/test'},'models':[{'ID':'ar-mock-model','Name':'ar-mock-model','Object':'model','OwnedBy':'cpa-agentrouter-continuity','Type':'codex','UserDefined':True}], 'max_retries':2,'model_prefix':'ar','priority':10}
    (auth/'ar-mock.json').write_text(json.dumps(credential));(auth/'ar-mock.json').chmod(0o600)
    cfg={'config-version':8,'server':{'host':'127.0.0.1','port':listen},'access':{'api-keys':['mock-client']},'oauth':{'auth-dir':str(auth)},'requests':{'proxy-url':'http://127.0.0.1:'+str(mock.server_address[1])},'routing':{'retry':{'request-retry':0}},'plugins':{'enabled':True,'dir':str(ROOT/'plugins'),'configs':{'cpa-agentrouter-continuity':{'enabled':True}}},'management':{'allow-remote':False,'secret-key':'mock-management','disable-control-panel':True}}
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
        if scenario=='decrypt':body['input'].insert(0,{'type':'reasoning','id':'rs_old','encrypted_content':'bad'})
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
            RELEASE.set();holding.join(10)
            assert response and 'response.completed' in response[0] and process.poll() is None
            print('PASS binary-hot-upgrade-with-active-stream',flush=True)
            STARTED.clear();RELEASE.clear()
        assert manage('/overview')['version']==expected_version
        print('PASS loaded-version',expected_version,flush=True)
        # New installs need no explicit User-Agent or Originator fields.
        credential['headers']={}
        (auth/'ar-mock.json').write_text(json.dumps(credential))
        time.sleep(1)
        DEFAULT_HEADERS_REQUIRED=True
        for scenario,attempts,success in [('success',1,True),('limit',2,True),('decrypt',1,False),('disconnect',2,True),('after_output',1,False),('auth',1,False)]:
            if scenario=='decrypt':
                body={'model':'ar/ar-mock-model','input':'start this conversation','stream':True,'metadata':{'test_scenario':'decrypt-start'}}
                req=urllib.request.Request('http://127.0.0.1:'+str(listen)+'/v1/responses',data=json.dumps(body).encode(),headers={'Authorization':'Bearer mock-client','Content-Type':'application/json','Session_id':'test-session-decrypt'})
                assert b'response.completed' in op.open(req,timeout=20).read()
            response=request(scenario)
            assert COUNTS.get(scenario)==attempts,(scenario,COUNTS,response)
            assert ('response.completed' in response)==success,(scenario,response)
            assert response.count('"delta": "OK"')<=1,(scenario,response)
            print('PASS',scenario,'attempts',attempts,'default_headers=True',flush=True)
        history=[d for s,d in CAPTURE if s=='decrypt'];assert any(x.get('type')=='reasoning' for x in history[0]['input']);assert len(history)==1
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
        for key in ['', 'mock-client', 'invalid-management']:
            try:manage('/overview',key=key)
            except urllib.error.HTTPError as error:assert error.code in (401,403)
            else:raise AssertionError('management API accepted wrong key')
        print('PASS management-auth-required',flush=True)
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
        RELEASE.clear();outputs=[]
        threads=[threading.Thread(target=lambda n=n:outputs.append(mixed('busy-'+str(n),'slot-'+str(n)))) for n in range(2)]
        for worker in threads:worker.start()
        for _ in range(50):
            if COUNTS.get('slot-0')==1 and COUNTS.get('slot-1')==1:break
            time.sleep(.1)
        else:raise AssertionError('two active AR sessions did not start')
        assert 'OTHER' in mixed('third-session','overflow')
        assert not COUNTS.get('overflow'),'third session exceeded key cap'
        RELEASE.set()
        for worker in threads:worker.join(10)
        assert len(outputs)==2 and all('response.completed' in x for x in outputs)
        assert 'response.completed' in mixed('new-after-release','released')
        assert COUNTS.get('released')==1
        assert 'OTHER' in mixed('third-session','overflow-followup',history=True)
        print('PASS two-session cap, non-AR fallback, release, origin remains external',flush=True)
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
