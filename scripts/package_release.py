#!/usr/bin/env python3
"""从干净的 Git 提交和已构建插件生成安装包，不包含本地运行数据。"""
import argparse
import hashlib
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path

from audit_repository import unwanted


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path, help='与当前提交对应的插件二进制')
    parser.add_argument('--output-dir', required=True, type=Path, help='ZIP 与校验文件输出目录')
    parser.add_argument('--platform', choices=('linux-amd64', 'windows-amd64'), default='linux-amd64', help='目标平台')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    version = re.search(r'const version = "([^"]+)"', (root / 'cmd/ar-continuity/plugin.go').read_text(encoding='utf-8'))[1]
    name = 'cpa-agentrouter-continuity'
    output = args.output_dir.resolve()
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=root):
        raise SystemExit('请先提交源码并保持工作区干净，再打包 Release。')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root).decode().strip()
    files = subprocess.check_output(['git', 'ls-tree', '-r', '--name-only', '-z', commit], cwd=root).decode('utf-8').split('\0')
    bad = [name for name in files if name and unwanted(name)]
    if bad:
        raise SystemExit('拒绝打包运行数据或构建产物：' + ', '.join(bad))
    binary = args.binary.resolve()
    extension = 'dll' if args.platform == 'windows-amd64' else 'so'
    if binary.name != f'{name}-v{version}.{extension}':
        raise SystemExit('二进制文件名与版本或平台不匹配。')
    raw = binary.read_bytes()
    if not raw.startswith(b'MZ' if extension == 'dll' else b'\x7fELF'):
        raise SystemExit('二进制格式与目标平台不匹配。')
    for asset in (root / 'web').iterdir():
        if asset.suffix in {'.html', '.js', '.css', '.png'} and asset.read_bytes() not in raw:
            raise SystemExit('二进制内嵌网页与当前提交不一致：' + asset.name)
    output.mkdir(parents=True, exist_ok=True)
    archive = output / f'{name}-v{version}-{args.platform}.zip'
    if archive.exists() or archive.with_name(archive.name + '.sha256').exists():
        raise SystemExit('输出安装包已存在，请使用新的输出目录。')
    with tempfile.TemporaryDirectory(prefix='release-', dir=output) as temp:
        package = Path(temp) / f'{name}-v{version}'
        package.mkdir()
        for relative in filter(None, files):
            source = root / relative
            if source.is_symlink() or not source.is_file():
                raise SystemExit(f'Unsupported source entry: {relative}')
            target = package / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(subprocess.check_output(['git', 'show', commit + ':' + relative], cwd=root))
        platform_dir, extension = args.platform.split('-', 1), 'so'
        if args.platform == 'windows-amd64':
            platform_dir, extension = ['windows', 'amd64'], 'dll'
        target = package / 'plugins' / platform_dir[0] / platform_dir[1] / f'{name}-v{version}.{extension}'
        target.parent.mkdir(parents=True)
        shutil.copy2(binary, target)
        (package / 'SOURCE_COMMIT.txt').write_text(commit + '\n', encoding='utf-8')
        subprocess.run([sys.executable, str(package / 'scripts/check_release.py'), str(package)], check=True)
        manifest = ''.join(
            hashlib.sha256(file.read_bytes()).hexdigest() + '  ' + file.relative_to(package).as_posix() + '\n'
            for file in sorted(package.rglob('*')) if file.is_file()
        )
        (package / 'SHA256SUMS.txt').write_text(manifest, encoding='utf-8')
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
            for file in sorted(package.rglob('*')):
                if file.is_file():
                    bundle.write(file, str(file.relative_to(package.parent)))
    checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
    (output / (archive.name + '.sha256')).write_text(checksum + '  ' + archive.name + '\n', encoding='utf-8')
    print(archive)
    print('SHA256', checksum)


if __name__ == '__main__':
    main()
