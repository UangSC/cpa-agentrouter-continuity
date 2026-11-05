#!/usr/bin/env python3
"""Package tracked source and a separately built Linux amd64 plugin; no runtime data."""
import argparse
import hashlib
import re
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--output-dir', required=True, type=Path)
    parser.add_argument('--platform', choices=('linux-amd64', 'windows-amd64'), default='linux-amd64')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    version = re.search(r'const version = "([^"]+)"', (root / 'plugin.go').read_text())[1]
    name = 'cpa-agentrouter-continuity'
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    files = subprocess.check_output(['git', 'ls-files', '-z'], cwd=root).decode().split('\0')
    if not any(files):
        raise SystemExit('No tracked source files; stage or commit source before packaging.')
    with tempfile.TemporaryDirectory(prefix='release-', dir=output) as temp:
        package = Path(temp) / f'{name}-v{version}'
        package.mkdir()
        for relative in filter(None, files):
            source = root / relative
            if source.is_symlink() or not source.is_file():
                raise SystemExit(f'Unsupported source entry: {relative}')
            target = package / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
        platform_dir, extension = args.platform.split('-', 1), 'so'
        if args.platform == 'windows-amd64':
            platform_dir, extension = ['windows', 'amd64'], 'dll'
        target = package / 'plugins' / platform_dir[0] / platform_dir[1] / f'{name}-v{version}.{extension}'
        target.parent.mkdir(parents=True)
        shutil.copy2(args.binary.resolve(), target)
        subprocess.run(['python3', str(package / 'scripts/check_release.py'), str(package)], check=True)
        manifest = ''.join(
            hashlib.sha256(file.read_bytes()).hexdigest() + '  ' + str(file.relative_to(package)) + '\n'
            for file in sorted(package.rglob('*')) if file.is_file()
        )
        (package / 'SHA256SUMS.txt').write_text(manifest)
        archive = output / f'{name}-v{version}-{args.platform}.zip'
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
            for file in sorted(package.rglob('*')):
                if file.is_file():
                    bundle.write(file, str(file.relative_to(package.parent)))
    checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
    (output / (archive.name + '.sha256')).write_text(checksum + '  ' + archive.name + '\n')
    print(archive)
    print('SHA256', checksum)


if __name__ == '__main__':
    main()
