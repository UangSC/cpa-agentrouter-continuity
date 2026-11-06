"""通过本机 CPA 验证完整桌面工具定义与模拟工具结果往返；不执行模型工具调用。"""
import argparse
import json
import pathlib
import re
import time
import tomllib
import urllib.request
import uuid

import yaml

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--core-root', type=pathlib.Path, required=True, help='CPA 内核目录')
parser.add_argument('--error-log', required=True, help='含原始桌面工具定义的 CPA 错误日志名')
parser.add_argument('--expected-version', help='预期插件版本，默认读取当前源码')
args = parser.parse_args()
expected_version = args.expected_version or re.search(
    r'const version = "([^"]+)"',
    (pathlib.Path(__file__).resolve().parents[1] / 'plugin.go').read_text(encoding='utf-8'))[1]
cfg = yaml.safe_load((args.core_root / 'config.yaml').read_text(encoding='utf-8'))
app = tomllib.loads((args.core_root.parent / 'config.toml').read_text(encoding='utf-8'))
base = 'http://127.0.0.1:' + str(cfg['server']['port'])
op = urllib.request.build_opener(urllib.request.ProxyHandler({}))

def manage(path):
    req = urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + app['management-secret-key']})
    with op.open(req, timeout=15) as res:
        return res.read()

overview = json.loads(manage('/v0/management/plugins/cpa-agentrouter-continuity/overview'))
assert overview['version'] == expected_version, '目标插件版本尚未加载'
log = manage('/v0/management/request-error-logs/' + args.error_log).decode('utf-8')
original = json.loads(log.split('=== REQUEST BODY ===', 1)[1].split('\n===', 1)[0].strip())
definitions = [x for x in original['input'] if x.get('type') == 'additional_tools']
assert definitions, '日志中没有桌面工具定义'
for item in definitions:
    item['id'] = 'at_' + uuid.uuid4().hex
sid = 'ar-tools-' + uuid.uuid4().hex[:16]

def request(items, choice):
    payload = {'model': original['model'], 'input': items, 'stream': True,
               'tool_choice': choice, 'parallel_tool_calls': False,
               'max_output_tokens': 512, 'reasoning': {'effort': 'low'},
               'include': ['reasoning.encrypted_content'], 'store': False,
               'prompt_cache_key': sid}
    req = urllib.request.Request(base + '/v1/responses', data=json.dumps(payload).encode(),
        headers={'Authorization': 'Bearer ' + cfg['access']['api-keys'][0],
                 'Content-Type': 'application/json', 'Session-Id': sid, 'Thread-Id': sid})
    output = None
    text = ''
    tool_items = []
    with op.open(req, timeout=120) as res:
        for line in res:
            if not line.startswith(b'data:'):
                continue
            try:
                event = json.loads(line[5:])
            except ValueError:
                continue
            kind = event.get('type')
            if kind == 'response.output_text.delta':
                text += event.get('delta', '')
            if kind in ('response.output_item.added', 'response.output_item.done'):
                item = event.get('item', {})
                if item.get('type') == 'function_call':
                    tool_items.append(item)
            if kind == 'response.failed':
                raise RuntimeError(json.dumps(event.get('response', {}).get('error'), ensure_ascii=False))
            if kind == 'response.completed':
                output = event['response']['output']
    assert output is not None, '缺少 response.completed'
    return output, text, tool_items

items = definitions + [{'role': 'user', 'content': 'For a protocol test, call the available wait_agent mailbox tool exactly once with timeout_ms=10000. Use the namespace shown in its tool definition. Do not call other tools or reply with text before the call. After the tool result, reply exactly AR_TOOL_ROUNDTRIP_OK.'}]
start = time.monotonic()
output, _, events = request(items, 'auto')
calls = [x for x in output if x.get('type') == 'function_call']
assert len(calls) == 1, '未返回唯一工具调用'
for call in calls + events:
    assert (call.get('namespace') == 'collaboration' and call.get('name') == 'wait_agent') or call.get('name') == 'collaboration.wait_agent', '工具名称没有还原'
assert json.loads(calls[0]['arguments']).get('timeout_ms') == 10000, '工具参数发生变化'
print('PASS 完整桌面工具定义、自动工具选择、SSE 与完成事件的工具名称还原', flush=True)
print('工具仅检查返回值，未实际执行。', flush=True)
items += output + [{'type': 'function_call_output', 'call_id': calls[0]['call_id'], 'output': 'Simulated mailbox result: no updates. Protocol test complete.'}]
_, answer, _ = request(items, 'none')
assert 'AR_TOOL_ROUNDTRIP_OK' in answer, '工具结果往返后未收到预期文本'
report = {'version': overview['version'], 'model': original['model'], 'tool': 'collaboration.wait_agent',
          'argument_preserved': True, 'simulated_tool_result': True, 'response_completed': True,
          'text': answer, 'seconds': round(time.monotonic() - start, 2)}
print(json.dumps(report, ensure_ascii=False), flush=True)
