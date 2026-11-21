import base64
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('configure',Path(__file__).with_name('configure-desktop-updates.py'))
configure=importlib.util.module_from_spec(spec);spec.loader.exec_module(configure)

class ConfigureTest(unittest.TestCase):
    def test_private_key_inside_repository_is_rejected(self):
        with self.assertRaises(ValueError):configure.key_paths(configure.ROOT/'private.key')

    def test_symlink_key_is_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder);(path/'key').write_text('fixture');(path/'link').symlink_to(path/'key')
            with self.assertRaises(ValueError):configure.key_paths(path/'link')

    def test_existing_public_identity_cannot_be_replaced(self):
        with tempfile.TemporaryDirectory() as folder:
            public=Path(folder)/'public';public.write_text('old')
            with patch.object(configure,'PUBLIC',public):
                with self.assertRaises(ValueError):configure.install_public('new')
                self.assertEqual(public.read_text(),'old')
                configure.install_public('old');self.assertEqual(public.read_text(),'old\n')

    def test_public_key_format_is_checked(self):
        with tempfile.TemporaryDirectory() as folder:
            public=Path(folder)/'public'
            raw=b'untrusted comment: fixture\n'+base64.b64encode(b'Ed'+b'x'*40)+b'\n'
            encoded=base64.b64encode(raw).decode();public.write_text(encoded)
            self.assertEqual(configure.public_text(public),encoded)
            public.write_text('private material')
            with self.assertRaises(ValueError):configure.public_text(public)

    def test_existing_secrets_are_not_overwritten(self):
        with patch.object(configure,'safe_run',return_value=json.dumps([{'name':configure.SECRETS[0]}])) as run:
            with self.assertRaises(ValueError):configure.upload_secrets('secret','password')
            self.assertEqual(run.call_count,1)

    def test_secret_values_use_stdin_not_command_arguments(self):
        with patch.object(configure,'safe_run',side_effect=['[]','','']) as run:
            configure.upload_secrets('private-fixture','password-fixture')
            calls=run.call_args_list
            self.assertEqual(len(calls),3)
            for call in calls:
                self.assertNotIn('private-fixture',call.args[0]);self.assertNotIn('password-fixture',call.args[0])
                self.assertEqual(call.kwargs['env']['GH_HOST'],'github.com')
                self.assertNotIn('GH_DEBUG',call.kwargs['env'])
            self.assertEqual(calls[1].kwargs['secret_input'],'password-fixture')
            self.assertEqual(calls[2].kwargs['secret_input'],'private-fixture')

    def test_process_error_does_not_print_signer_output(self):
        from subprocess import CompletedProcess
        with patch.object(configure.subprocess,'run',return_value=CompletedProcess([],1,'secret-output','secret-error')):
            with self.assertRaises(RuntimeError) as raised:configure.safe_run(['signer'])
            self.assertNotIn('secret-output',str(raised.exception));self.assertNotIn('secret-error',str(raised.exception))

if __name__=='__main__':unittest.main()
