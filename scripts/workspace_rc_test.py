#!/usr/bin/env python3
"""Provenance and immutable-publication regressions; never contacts GitHub."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

import workspace_rc as rc

spec = importlib.util.spec_from_file_location('publish_workspace_rc', Path(__file__).with_name('publish-workspace-rc.py'))
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)

VERSION = '0.2.0-rc.3'
SOURCE = 'a' * 40
RUN = '123456'


class ProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.reports = {}
        for kind, assets in rc.package_names(VERSION).items():
            records = []
            for flavor, name in assets.items():
                path = self.root / name
                path.write_bytes(('actual fixture ' + flavor).encode())
                records.append({'kind': flavor, 'name': name, 'source_name': name,
                                'size': path.stat().st_size, 'sha256': rc.digest(path)})
            report = {'schema_version': 2, 'platform': kind, 'version': VERSION, 'source_commit': SOURCE,
                      'workflow_run_id': RUN, 'build_succeeded': True, 'files': records,
                      'native_installation_outcome': 'success', 'native_gui_acceptance_passed': kind in rc.NATIVE_REPORTS,
                      'apple_notarized': False}
            if kind in rc.NATIVE_REPORTS:
                native = self.root / rc.NATIVE_REPORTS[kind]
                rc.write_json(native, {'success': True, 'scope': kind})
                report['native_report_sha256'] = rc.digest(native)
            if kind == 'Ubuntu24':
                report['tested_files'] = copy.deepcopy(self.reports['Linux']['files'])
            self.reports[kind] = report
            self.save(kind)
        self.browser = {'success': True, 'source_commit': SOURCE, 'workflow_run_id': RUN,
                        'scope': 'two real folder-backed processes and Chromium',
                        'checks': ['bind', 'merged notes', 'edit doc', 'conflict',
                                   'public delivery stays disabled until this device configures a server',
                                   'note and document references share the picker; a real file move removes the source and preserves the note reference']}
        rc.write_json(self.root / 'workspace-browser-results.json', self.browser)

    def save(self, kind):
        rc.write_json(self.root / ('build-evidence-' + kind + '.json'), self.reports[kind])

    def verify(self):
        return rc.verify_build_inputs(self.root, VERSION, SOURCE, RUN)

    def test_complete_same_source_candidate(self):
        reports, files, browser = self.verify()
        self.assertEqual(len(files), 9)
        self.assertEqual(set(reports), set(rc.KINDS))
        self.assertEqual(len(browser['checks']), 6)

    def test_stale_source_or_run_rejected(self):
        for key, value in [('source_commit', 'b' * 40), ('workflow_run_id', '987')]:
            with self.subTest(key=key):
                original = self.reports['Windows'][key]
                self.reports['Windows'][key] = value
                self.save('Windows')
                with self.assertRaisesRegex(ValueError, 'Mixed build'):
                    self.verify()
                self.reports['Windows'][key] = original
                self.save('Windows')

    def test_native_failure_cannot_be_hidden_by_green_build(self):
        self.reports['Windows']['native_installation_outcome'] = 'failure'
        self.save('Windows')
        with self.assertRaisesRegex(ValueError, 'Native acceptance failed'):
            self.verify()

    def test_changed_native_report_rejected(self):
        rc.write_json(self.root / rc.NATIVE_REPORTS['Linux'], {'success': True, 'modified': True})
        with self.assertRaisesRegex(ValueError, 'changed after staging'):
            self.verify()

    def test_ubuntu24_must_test_identical_packages(self):
        self.reports['Ubuntu24']['tested_files'][0]['sha256'] = '0' * 64
        self.save('Ubuntu24')
        with self.assertRaisesRegex(ValueError, 'same Linux release packages'):
            self.verify()

    def test_corrupt_bytes_same_size_rejected(self):
        file = self.root / self.reports['Windows']['files'][0]['name']
        file.write_bytes(b'x' * file.stat().st_size)
        with self.assertRaisesRegex(ValueError, 'SHA-256 mismatch'):
            self.verify()

    def test_case_colliding_or_unexpected_filename_rejected(self):
        self.reports['Windows']['files'][0]['name'] = '../outside.exe'
        self.save('Windows')
        with self.assertRaisesRegex(ValueError, 'Unexpected package filename'):
            self.verify()

    def test_missing_package_cannot_publish(self):
        self.reports['server']['files'].pop()
        self.save('server')
        with self.assertRaisesRegex(ValueError, 'Incomplete package set'):
            self.verify()

    def test_stale_browser_evidence_rejected(self):
        self.browser['source_commit'] = 'b' * 40
        rc.write_json(self.root / 'workspace-browser-results.json', self.browser)
        with self.assertRaisesRegex(ValueError, 'Browser evidence came from'):
            self.verify()

    def test_duplicate_browser_checks_do_not_count_as_full_verification(self):
        self.browser['checks'] = ['bind'] * 6
        rc.write_json(self.root / 'workspace-browser-results.json', self.browser)
        with self.assertRaisesRegex(ValueError, 'did not complete'):
            self.verify()

    def test_normalization_cannot_rebind_another_installer(self):
        records = self.reports['Windows']['files']
        item = records[0]
        report = {'schema_version': 1, 'version': VERSION, 'success': True, 'platform': 'windows',
                  'installers': [{'kind': item['kind'], 'asset': {'name': item['source_name'], 'sha256': '0' * 64}}]}
        with self.assertRaisesRegex(ValueError, 'not bound to the staged package'):
            rc.normalize_native_report(report, records, 'Windows', VERSION)

    def test_normalization_renames_only_matching_original_bytes(self):
        records = copy.deepcopy(self.reports['Windows']['files'])
        for record in records:
            record['source_name'] = 'original-' + record['kind'] + '.bin'
        report = {'schema_version': 1, 'version': VERSION, 'success': True, 'platform': 'windows',
                  'installers': [{'kind': item['kind'], 'asset': {'name': item['source_name'], 'sha256': item['sha256'], 'size': item['size']}}
                                 for item in records]}
        normalized = rc.normalize_native_report(report, records, 'Windows', VERSION)
        self.assertEqual([case['asset']['name'] for case in normalized['installers']], [item['name'] for item in records])


class FakeGitHub:
    def __init__(self, expected):
        self.expected = expected
        self.tag_source = None
        self.release = None
        self.assets = []
        self.writes = []
        self.corrupt_upload = False

    def get(self, path):
        if path.startswith('releases?'):
            return [copy.deepcopy(self.release)] if self.release else []
        if path.startswith('git/matching-refs/'):
            return [{'ref': 'refs/tags/v' + VERSION, 'object': {'type': 'commit', 'sha': self.tag_source}}] if self.tag_source else []
        if path.endswith('/assets?per_page=100'):
            return copy.deepcopy(self.assets)
        if path == 'releases/1':
            return copy.deepcopy(self.release)
        raise AssertionError('Unexpected read: ' + path)

    def mutate(self, method, path, payload):
        self.writes.append((method, path, copy.deepcopy(payload)))
        if path == 'git/refs':
            self.tag_source = payload['sha']
            return {}
        if path == 'releases':
            self.release = dict(payload, id=1, html_url='https://github.com/Azurefly/omnishare/releases/tag/v' + VERSION)
            return copy.deepcopy(self.release)
        if path == 'releases/1':
            self.release.update(payload)
            return copy.deepcopy(self.release)
        raise AssertionError('Unexpected write: ' + path)

    def upload(self, tag, path):
        self.writes.append(('upload', path.name))
        self.assets.append({'name': path.name, 'state': 'uploaded', 'size': path.stat().st_size,
                            'digest': 'sha256:' + ('0' * 64 if self.corrupt_upload else rc.digest(path))})

    def existing(self, draft):
        self.tag_source = SOURCE
        self.release = {'id': 1, 'tag_name': 'v' + VERSION, 'draft': draft, 'prerelease': True,
                        'html_url': 'https://github.com/Azurefly/omnishare/releases/tag/v' + VERSION}
        self.assets = [{'name': name, 'state': 'uploaded', 'size': size, 'digest': digest}
                       for name, (size, digest) in self.expected.items()]


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        package = self.root / 'candidate.zip'
        package.write_bytes(b'exact validated package')
        self.expected = {package.name: (package.stat().st_size, 'sha256:' + rc.digest(package))}
        self.github = FakeGitHub(self.expected)

    def publish(self):
        return publisher.publish_assets(self.root, VERSION, SOURCE, '# Exact release notes', self.expected, self.github)

    def test_new_release_uploads_then_publishes_verified_assets(self):
        self.assertTrue(self.publish()['verified'])
        kinds = [item[0] for item in self.github.writes]
        self.assertEqual(kinds, ['POST', 'POST', 'upload', 'PATCH'])
        self.assertFalse(self.github.release['draft'])
        self.assertTrue(self.github.release['prerelease'])
        self.assertEqual(self.github.release['make_latest'], 'false')

    def test_wrong_tag_is_never_moved(self):
        self.github.tag_source = 'b' * 40
        with self.assertRaisesRegex(ValueError, 'refusing to move'):
            self.publish()
        self.assertEqual(self.github.writes, [])

    def test_published_matching_release_is_read_only(self):
        self.github.existing(draft=False)
        self.assertTrue(self.publish()['already_published'])
        self.assertEqual(self.github.writes, [])

    def test_missing_attachment_in_public_release_is_not_silently_repaired(self):
        self.github.existing(draft=False)
        self.github.assets = []
        with self.assertRaisesRegex(ValueError, 'attachment set'):
            self.publish()
        self.assertEqual(self.github.writes, [])

    def test_stable_release_cannot_be_reclassified(self):
        self.github.existing(draft=False)
        self.github.release['prerelease'] = False
        with self.assertRaisesRegex(ValueError, 'not this candidate'):
            self.publish()
        self.assertEqual(self.github.writes, [])

    def test_altered_draft_asset_is_not_overwritten(self):
        self.github.existing(draft=True)
        self.github.assets[0]['digest'] = 'sha256:' + 'b' * 64
        with self.assertRaisesRegex(ValueError, 'refusing overwrite'):
            self.publish()
        self.assertEqual(self.github.writes, [])

    def test_matching_partial_draft_can_resume_without_replacing_assets(self):
        self.github.existing(draft=True)
        self.github.assets = []
        self.assertTrue(self.publish()['verified'])
        self.assertEqual([item[0] for item in self.github.writes], ['upload', 'PATCH'])

    def test_remote_byte_mismatch_keeps_new_release_draft(self):
        self.github.corrupt_upload = True
        with self.assertRaisesRegex(ValueError, 'refusing overwrite'):
            self.publish()
        self.assertTrue(self.github.release['draft'])
        self.assertNotIn('PATCH', [item[0] for item in self.github.writes])


if __name__ == '__main__':
    unittest.main()
