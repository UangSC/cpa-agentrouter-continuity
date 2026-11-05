#!/usr/bin/env python3
"""Create a disabled runtime credential; never accept API keys in arguments."""
import argparse
import getpass
import json
import os
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--prefix', default='')
    args = parser.parse_args()
    package = Path(__file__).resolve().parents[1]
    output = args.output.expanduser().resolve()
    if output.is_relative_to(package):
        parser.error('Runtime credentials must be stored outside the source/release directory.')
    if output.exists():
        parser.error('Output already exists; update it through CPA credential management.')
    if not output.parent.is_dir():
        parser.error('Output parent must already exist.')
    key = getpass.getpass('AgentRouter API key (hidden): ').strip()
    if not key:
        parser.error('API key cannot be empty.')
    data = json.loads((package / 'examples/cpa-agentrouter-continuity.example.json').read_text())
    data['api_key'] = key
    data['model_prefix'] = args.prefix.strip(' /\t')
    descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, 'w') as handle:
        json.dump(data, handle, ensure_ascii=False, indent=2)
        handle.write('\n')
    print(f'Created disabled credential: {output}')
    print('Set the owner to the CPA service account before importing or enabling it.')


if __name__ == '__main__':
    main()
