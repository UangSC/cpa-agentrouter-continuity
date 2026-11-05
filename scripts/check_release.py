#!/usr/bin/env python3
"""Check distributable files without printing possible secrets."""
import argparse
import json
import re
from pathlib import Path

PATTERNS = (
    re.compile(rb'sk-[A-Za-z0-9_-]{20,}'),
    re.compile(rb'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'),
)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('root', type=Path)
    args = parser.parse_args()
    failed = []
    count = 0
    for file in sorted(args.root.rglob('*')):
        if not file.is_file() or '.git' in file.parts or '__pycache__' in file.parts:
            continue
        raw = file.read_bytes()
        count += 1
        if any(pattern.search(raw) for pattern in PATTERNS):
            failed.append((str(file.relative_to(args.root)), 'credential-like bytes'))
        if file.suffix == '.json':
            try:
                data = json.loads(raw)
            except (ValueError, UnicodeDecodeError):
                failed.append((str(file.relative_to(args.root)), 'invalid JSON'))
                continue
            def inspect(value):
                if isinstance(value, dict):
                    for key, item in value.items():
                        if key.lower().replace('-', '_') in ('api_key', 'access_token', 'refresh_token') and item:
                            failed.append((str(file.relative_to(args.root)), 'nonempty credential field'))
                        inspect(item)
                elif isinstance(value, list):
                    for item in value:
                        inspect(item)
            inspect(data)
    if failed:
        for name, reason in failed:
            print(f'FAIL {name}: {reason}')
        raise SystemExit(1)
    print(f'PASS: {count} files scanned; no key-like strings, private keys, or populated credential fields.')
    print('This heuristic complements runtime-key exact matching; it is not a proof for arbitrary secret formats.')


if __name__ == '__main__':
    main()
