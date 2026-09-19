"""Exercise interrupted-publication recovery without contacting a registry."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('recovery', Path(__file__).with_name('recover-release-metadata.py'))
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


class RecoveryTests(unittest.TestCase):
    def exercise(self, mismatch):
        tag = 'v2026.38'
        uploaded = []
        def api(endpoint):
            if '/git/ref/' in endpoint:
                return {'object': {'type':'tag', 'sha':'a'*40}}
            if '/git/tags/' in endpoint:
                return {'object': {'type':'commit', 'sha':'b'*40}}
            return {'draft': True}
        def output(args, **kwargs):
            if args[:2] == ['docker','inspect']:
                return json.dumps([{'Config':{'Labels':{'org.opencontainers.image.revision':'b'*40}}}]).encode()
            return 'container-id\n'
        def run(args, **kwargs):
            if args[:3] == ['gh','release','download']:
                root = Path(args[args.index('--dir')+1])
                sums = []
                for arch in ('amd64','arm64'):
                    name = f'report-portal_{tag}_linux_{arch}'
                    archive = root/(name+'.tar.gz')
                    with tarfile.open(archive, 'w:gz') as tar:
                        info = tarfile.TarInfo(name+'/report-portal')
                        info.size = len(b'binary')
                        tar.addfile(info, io.BytesIO(b'binary'))
                    sums.append(hashlib.sha256(archive.read_bytes()).hexdigest()+'  '+archive.name)
                (root/'SHA256SUMS.txt').write_text('\n'.join(sums))
            elif args[:2] == ['docker','cp']:
                Path(args[-1]).write_bytes(b'wrong' if mismatch else b'binary')
            elif args[:3] == ['gh','release','upload']:
                uploaded.append(json.loads(Path(args[4]).read_text()))
        with patch.object(recovery, 'gh_json', side_effect=api), \
             patch.object(recovery, 'image_digest', return_value='sha256:'+'c'*64), \
             patch.object(recovery.subprocess, 'run', side_effect=run), \
             patch.object(recovery.subprocess, 'check_output', side_effect=output):
            if mismatch:
                with self.assertRaisesRegex(RuntimeError, 'binaries differ'):
                    recovery.recover('owner/repo', tag)
                self.assertEqual(uploaded, [])
            else:
                recovery.recover('owner/repo', tag)
                self.assertEqual(len(uploaded), 1)
                self.assertEqual(uploaded[0]['commit'], 'b'*40)
    def test_recovers_only_after_binary_verification(self):
        self.exercise(False)
    def test_mismatch_never_uploads_metadata(self):
        self.exercise(True)

if __name__ == '__main__':
    unittest.main()
