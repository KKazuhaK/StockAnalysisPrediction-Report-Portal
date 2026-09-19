"""Failure-path checks for release publication guards."""
import unittest
from unittest.mock import patch
import subprocess
from release_guard import image_digest, validate_metadata, ci_ready, read_state, guard_draft, expected_assets, verify_target

class GuardTests(unittest.TestCase):
    @patch('release_guard.subprocess.run')
    def test_registry_failure_is_not_absence(self, run):
        run.return_value = subprocess.CompletedProcess([], 1, '', 'unauthorized')
        with self.assertRaises(RuntimeError):
            image_digest('registry/image:v2026.38')

    @patch('release_guard.subprocess.run')
    def test_missing_manifest_is_absent(self, run):
        run.return_value = subprocess.CompletedProcess([], 1, '', 'manifest unknown')
        self.assertIsNone(image_digest('registry/image:v2026.38'))

    @patch('release_guard.subprocess.check_output')
    def test_state_read_failure_aborts(self, read):
        read.side_effect = subprocess.CalledProcessError(1, 'gh')
        with self.assertRaises(subprocess.CalledProcessError):
            read_state('owner/repo')

    @patch('release_guard.subprocess.check_output')
    def test_published_release_cannot_be_replaced(self, read):
        read.return_value = '[[{"tag_name":"v2026.38","draft":false}]]'
        with self.assertRaises(RuntimeError):
            guard_draft('owner/repo', 'v2026.38')

    def test_expected_assets_identify_all_platforms(self):
        assets = expected_assets('v2026.38')
        self.assertEqual(len(assets), 8)
        self.assertIn('report-portal_v2026.38_windows_arm64.zip', assets)
        self.assertIn('release-metadata.json', assets)

    def test_metadata_rejects_wrong_commit(self):
        with self.assertRaises(ValueError):
            validate_metadata({'tag':'v2026.38','image':'registry/image','commit':'bad',
                               'digest':'sha256:'+'a'*64}, 'v2026.38', 'registry/image', 'b'*40)

    @patch('release_guard.image_digest', return_value='sha256:' + 'b'*64)
    @patch('release_guard.subprocess.check_output')
    @patch('release_guard.gh_json')
    def test_promotion_rejects_changed_registry_tag(self, gh, read, digest):
        import json
        gh.side_effect = [
            {'draft': False, 'assets': [{'name':name, 'id':1} for name in expected_assets('v2026.38')]},
            {'object': {'type':'tag','sha':'t'}},
            {'object': {'type':'commit','sha':'c'*40}},
        ]
        read.return_value = json.dumps({'tag':'v2026.38','image':'registry/image',
                                       'commit':'c'*40,'digest':'sha256:'+'a'*64})
        with self.assertRaisesRegex(RuntimeError, 'recorded digest'):
            verify_target('owner/repo', 'v2026.38', 'registry/image')

    def test_ci_requires_all_full_race_shards(self):
        jobs = [{'name':name, 'conclusion':'success'} for name in
                ['go-test','web typecheck + build','workflow lint','docker image (font gate + smoke)',
                 'go race full (other packages)']]
        self.assertFalse(ci_ready(jobs))
        jobs += [{'name':f'go race full ({i}/4)', 'conclusion':'success'} for i in range(1,5)]
        self.assertTrue(ci_ready(jobs))

if __name__ == '__main__':
    unittest.main()
