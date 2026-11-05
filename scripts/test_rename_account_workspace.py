import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from rename_account_workspace import migrate,cleanup_legacy


class RenameAccountTests(unittest.TestCase):
    def test_verified_migration_keeps_original_messages_and_assets(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            work,admin,state=root/'work',root/'control/admin',root/'control/state'
            account='synthetic-account'
            scope=hashlib.sha256(account.encode()).hexdigest()
            old=work/'pri_workspace'/scope
            for path in [old/'music/jobs',old/'assets/objects',old/'conversations/thread/inbox',admin/'voice-library/files',state,work/'public_workspace/manifests']:
                path.mkdir(parents=True,exist_ok=True)
            original=json.dumps({'uploaded_path_literal':str(old/'unrelated')}).encode()
            (old/'assets/objects/upload.json').write_bytes(original)
            (old/'conversations/thread/inbox/upload.json').write_bytes(original)
            voice=b'synthetic-personal-voice'
            name=hashlib.sha256(voice).hexdigest()+'.pth'
            (admin/'voice-library/files'/name).write_bytes(voice)
            (admin/'voice-library/catalog.json').write_text('{"items":[]}',encoding='utf-8')
            (state/'state.json').write_text(json.dumps({'path':str(old/'music/song.wav'),'text':str(old/'literal-in-history')}),encoding='utf-8')
            before=(state/'state.json').read_bytes()
            backup=root/'control/backup'
            result=migrate(work,admin,state,account,'Readable-account',backup)
            self.assertFalse(result['applied'])
            self.assertEqual((state/'state.json').read_bytes(),before)
            migrate(work,admin,state,account,'Readable-account',backup,apply=True)
            new=work/'pri_workspace/Readable-account'
            self.assertFalse(old.exists())
            self.assertEqual((new/'assets/objects/upload.json').read_bytes(),original)
            self.assertEqual((new/'conversations/thread/inbox/upload.json').read_bytes(),original)
            self.assertEqual((new/'voice-library/files'/name).read_bytes(),voice)
            updated=json.loads((state/'state.json').read_bytes())
            self.assertEqual(updated['path'],str(new/'music/song.wav'))
            self.assertEqual(updated['text'],str(old/'literal-in-history'))
            self.assertEqual(json.loads((new/'account.json').read_bytes())['scope'],scope)
            self.assertEqual((admin/'workspace-directory').read_text().strip(),'Readable-account')
            self.assertGreater(cleanup_legacy(admin,new),len(voice))
            self.assertFalse((admin/'voice-library').exists())
            self.assertEqual((new/'voice-library/files'/name).read_bytes(),voice)

    def test_name_escape_or_existing_target_never_moves_data(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            account='synthetic'
            old=root/'pri_workspace'/hashlib.sha256(account.encode()).hexdigest()
            old.mkdir(parents=True)
            admin,state=root/'admin',root/'state'
            admin.mkdir();state.mkdir()
            for name in ['../foreign','/outside','wrong\\target']:
                with self.assertRaises(ValueError):migrate(root,admin,state,account,name,root/'backup',apply=True)
            self.assertTrue(old.exists())


if __name__=='__main__':unittest.main()
