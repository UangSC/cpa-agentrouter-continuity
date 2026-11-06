#!/usr/bin/env python3
"""检查分发文件中的密钥特征；只报告文件与规则，不回显疑似密钥。"""
import argparse
import json
import re
import subprocess
from pathlib import Path

PATTERNS = {
    'API key': re.compile(rb'sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}'),
    'GitHub token': re.compile(rb'(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})'),
    'Google API key': re.compile(rb'AIza[0-9A-Za-z_-]{35}'),
    'AWS access key': re.compile(rb'(?:AKIA|ASIA)[0-9A-Z]{16}'),
    'private key': re.compile(rb'-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----'),
    'JWT': re.compile(rb'\beyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\b'),
}
SECRET_FIELDS = {'api_key', 'api_keys', 'access_token', 'refresh_token', 'client_secret',
                 'management_secret_key', 'secret_key', 'password', 'passwd', 'authorization', 'private_key'}
ASSIGNMENT = re.compile(
    rb'''(?im)["']?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|management[_-]secret[_-]key|secret[_-]key|password|passwd|authorization)["']?\s*[:=]\s*["']([^"'\r\n]{8,})["']''')


def is_placeholder(value):
    if not isinstance(value, str):
        return False
    text = value.strip()
    if text.lower().startswith('bearer '):
        text = text[7:]
    return not text or text.startswith(('mock-', 'test-', 'example-', '${', '<')) or text in {'REDACTED', 'YOUR_API_KEY'}


def scan_bytes(name, raw):
    """返回规则名称；内容和疑似密钥不得进入结果。"""
    reasons = {label for label, pattern in PATTERNS.items() if pattern.search(raw)}
    for match in ASSIGNMENT.finditer(raw):
        value = match[1].decode('utf-8', 'replace')
        if not is_placeholder(value) and not any(x in value for x in ('{', '}', '\\', '\n')):
            reasons.add('literal credential assignment')
    if str(name).lower().endswith('.json'):
        try:
            data = json.loads(raw)
        except (ValueError, UnicodeDecodeError):
            reasons.add('invalid JSON')
        else:
            def inspect(value):
                if isinstance(value, dict):
                    for key, item in value.items():
                        if key.lower().replace('-', '_') in SECRET_FIELDS and item:
                            values = item if isinstance(item, list) else [item]
                            if not all(is_placeholder(v) for v in values):
                                reasons.add('nonempty credential field')
                        inspect(item)
                elif isinstance(value, list):
                    for item in value:
                        inspect(item)
            inspect(data)
    return sorted(reasons)


def source_files(root):
    """跟踪文件与未忽略的新源码，不递归扫描构建缓存或运行数据。"""
    raw = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=root)
    return sorted({root / name for name in raw.decode('utf-8').split('\0') if name and (root / name).is_file()})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('root', type=Path, help='源码仓库或待分发目录')
    parser.add_argument('--git', action='store_true', help='只检查 Git 跟踪与未忽略的新文件')
    args = parser.parse_args()
    failed = []
    count = 0
    files = source_files(args.root) if args.git else sorted(args.root.rglob('*'))
    for file in files:
        if not file.is_file() or '.git' in file.parts or '__pycache__' in file.parts:
            continue
        raw = file.read_bytes()
        count += 1
        for reason in scan_bytes(file.name, raw):
            failed.append((str(file.relative_to(args.root)), reason))
    if failed:
        for name, reason in failed:
            print(f'FAIL {name}: {reason}')
        raise SystemExit(1)
    print(f'PASS: {count} files scanned; no key-like strings, private keys, or populated credential fields.')
    print('This heuristic complements runtime-key exact matching; it is not a proof for arbitrary secret formats.')


if __name__ == '__main__':
    main()
