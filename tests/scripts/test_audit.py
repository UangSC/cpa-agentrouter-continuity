"""发布扫描须识别真实形态的凭证，保留明确的模拟数据，覆盖历史及附件。"""
import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'scripts'))
from audit_repository import audit, unwanted
from check_release import scan_bytes


class AuditTests(unittest.TestCase):
    def test_secrets_are_detected_without_echo(self):
        secrets = [b'sk-' + b'A' * 35, b'ghp_' + b'B' * 36,
                   b'github_pat_' + b'C' * 60, b'AKIA' + b'D' * 16,
                   b'-----BEGIN ' + b'PRIVATE KEY-----']
        for secret in secrets:
            reasons = scan_bytes('artifact.bin', b'prefix ' + secret + b' suffix')
            self.assertTrue(reasons)
            self.assertNotIn(secret.decode(), str(reasons))

    def test_nested_credentials_and_mock_values(self):
        for value in ['', 'mock-management', 'test-runtime-only']:
            self.assertEqual(scan_bytes('example.json', json.dumps({'api_key': value}).encode()), [])
        data = json.dumps({'nested': [{'api_key': 'live-' + 'z' * 24}]}).encode()
        self.assertIn('nonempty credential field', scan_bytes('runtime.json', data))
        self.assertIn('literal credential assignment', scan_bytes('runtime.yml', b'password: "' + b'X' * 24 + b'"'))

    def test_source_and_release_allow_different_artifacts(self):
        for name in ['auths/live.json', 'build/output.log', '.env', 'config.yaml', 'node_modules/a.js']:
            self.assertTrue(unwanted(name))
        self.assertFalse(unwanted('examples/plugins.example.yaml'))
        self.assertFalse(unwanted('web/logo.png'))
        self.assertTrue(unwanted('plugins/windows/amd64/plugin.dll'))
        self.assertFalse(unwanted('package/plugins/windows/amd64/plugin.dll', release=True))

    def test_history_and_same_blob_at_runtime_path_are_not_skipped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                subprocess.run(['git', *args], cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
            git('init')
            git('config', 'user.name', 'Audit Test')
            git('config', 'user.email', 'audit@example.invalid')
            (root / 'config.yaml').write_text('mode: sample\n')
            (root / 'historical.txt').write_bytes(b'sk-' + b'H' * 35)
            git('add', '.')
            git('commit', '-m', 'test fixture')
            git('mv', 'config.yaml', 'example.yaml')
            (root / 'historical.txt').write_text('removed\n')
            git('add', '.')
            git('commit', '-m', 'remove fixture secret')
            report = audit(root, history=True)
            self.assertTrue(any(f['path'] == 'config.yaml' and f['scope'].startswith('git:') for f in report['findings']))
            self.assertTrue(any(f['rule'] == 'API key' and f['scope'].startswith('git:') for f in report['findings']))
            self.assertFalse(any(f['scope'] == 'working-tree' for f in report['findings']))

    def test_release_is_scanned_in_memory_and_checksum_verified(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(['git', 'init'], cwd=root, capture_output=True, check=True)
            (root / '.gitignore').write_text('releases/\n')
            releases = root / 'releases'
            releases.mkdir()
            archive = releases / 'fixture.zip'
            with zipfile.ZipFile(archive, 'w') as bundle:
                bundle.writestr('../config.yaml', 'mode: example')
                bundle.writestr('package/plugin.bin', b'ghp_' + b'J' * 36)
            archive.with_name(archive.name + '.sha256').write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + '  fixture.zip\n')
            result = audit(root, releases=releases)
            self.assertTrue(result['archives'][0]['checksum_verified'])
            self.assertTrue(any(f['rule'] == 'unsafe archive path' for f in result['findings']))
            self.assertTrue(any(f['rule'] == 'GitHub token' for f in result['findings']))
            self.assertFalse((root / 'config.yaml').exists())


if __name__ == '__main__':
    unittest.main()
