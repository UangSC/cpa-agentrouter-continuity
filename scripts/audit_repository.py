#!/usr/bin/env python3
"""审计源码、可达 Git 历史和本地 Release 附件，不输出疑似密钥原文。"""
import argparse
import hashlib
import io
import json
import subprocess
import zipfile
from pathlib import Path, PurePosixPath

from check_release import scan_bytes, source_files

UNWANTED_PARTS = {'.git', 'node_modules', '__pycache__', '.venv', '.idea', '.vscode',
                  'auths', 'backups', '.runtime', 'build', 'dist', 'coverage'}
UNWANTED_SUFFIXES = {'.log', '.pyc', '.key', '.pem', '.pending', '.tmp'}


def unwanted(name, release=False):
    path = PurePosixPath(name)
    if any(p in UNWANTED_PARTS for p in path.parts):
        return True
    if path.suffix.lower() in UNWANTED_SUFFIXES or path.name.startswith('.env') and path.name != '.env.example':
        return True
    if path.name in {'config.yaml', 'config.yml', 'config.toml', 'credentials.json', 'Thumbs.db', '.DS_Store'}:
        return True
    if path.name.startswith('VALIDATION-') and path.name.endswith('-local.md'):
        return True
    if 'sessions.json' in path.name:
        return True
    return not release and path.suffix.lower() in {'.zip', '.exe', '.dll', '.so', '.dylib'}


def git(root, *args):
    return subprocess.check_output(['git', *args], cwd=root)


def audit(root, history=False, releases=None):
    findings = []
    counts = {'working_files': 0, 'history_blobs': 0, 'history_commits': 0, 'release_archives': 0, 'release_files': 0}

    def inspect(scope, name, raw, release=False):
        for rule in scan_bytes(name, raw):
            findings.append({'scope': scope, 'path': name, 'rule': rule})
        if unwanted(name, release):
            findings.append({'scope': scope, 'path': name, 'rule': 'unnecessary or runtime file'})

    for file in source_files(root):
        inspect('working-tree', file.relative_to(root).as_posix(), file.read_bytes())
        counts['working_files'] += 1
    refs = git(root, 'for-each-ref', '--format=%(refname) %(objectname)').decode().splitlines()
    if history:
        counts['history_commits'] = int(git(root, 'rev-list', '--count', '--all'))
        names = {}
        for commit in git(root, 'rev-list', '--all').decode().splitlines():
            for entry in git(root, 'ls-tree', '-r', '-z', '--full-tree', commit).split(b'\0'):
                if not entry:
                    continue
                metadata, name = entry.split(b'\t', 1)
                _, kind, oid = metadata.decode().split()
                if kind == 'blob':
                    names.setdefault(oid, set()).add(name.decode('utf-8'))
        for oid, paths in names.items():
            raw = git(root, 'cat-file', 'blob', oid)
            for name in sorted(paths):
                inspect('git:' + oid, name, raw)
            counts['history_blobs'] += 1
    archives = []
    if releases:
        for archive in sorted(releases.glob('*.zip')):
            raw = archive.read_bytes()
            checksum = hashlib.sha256(raw).hexdigest()
            checksum_file = archive.with_name(archive.name + '.sha256')
            checksum_ok = checksum_file.is_file() and checksum_file.read_text().split()[0].lower() == checksum
            if not checksum_ok:
                findings.append({'scope': 'release', 'path': archive.name, 'rule': 'missing or mismatched archive checksum'})
            with zipfile.ZipFile(io.BytesIO(raw)) as bundle:
                for item in bundle.infolist():
                    if item.is_dir():
                        continue
                    # Scan in memory; no untrusted archive path is ever extracted.
                    if item.file_size > 128 * 1024 * 1024 or item.file_size > max(1, item.compress_size) * 200:
                        raise ValueError('Release member exceeds audit size limit: ' + item.filename)
                    p = PurePosixPath(item.filename)
                    if p.is_absolute() or '..' in p.parts:
                        findings.append({'scope': archive.name, 'path': item.filename, 'rule': 'unsafe archive path'})
                    inspect(archive.name, item.filename, bundle.read(item), release=True)
                    counts['release_files'] += 1
            archives.append({'name': archive.name, 'sha256': checksum, 'checksum_verified': checksum_ok})
            counts['release_archives'] += 1
    return {'counts': counts, 'refs': refs, 'archives': archives, 'findings': findings,
            'limits': 'Heuristic scan of reachable local refs and supplied attachments; not proof against every secret format or unreachable server objects.'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1], help='待检查的 Git 仓库')
    parser.add_argument('--history', action='store_true', help='扫描已获取的所有分支与标签历史')
    parser.add_argument('--releases-dir', type=Path, help='包含 ZIP 与 .sha256 的本地附件目录')
    parser.add_argument('--report', type=Path, help='保存脱敏 JSON 审计报告')
    args = parser.parse_args()
    result = audit(args.root.resolve(), args.history, args.releases_dir)
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(result['counts'], ensure_ascii=False))
    for item in result['findings']:
        print('FAIL', item['scope'], item['path'], item['rule'])
    print('FAIL' if result['findings'] else 'PASS', '审计完成；报告不包含疑似密钥原文。')
    raise SystemExit(bool(result['findings']))


if __name__ == '__main__':
    main()
