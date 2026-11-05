import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from organize_voice_library import organize
from singing_config import load_singing_config


class RoleLibraryTests(unittest.TestCase):
    def test_role_folders_preserve_identity_bytes_versions_and_repeat(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/'files').mkdir()
            data=b'synthetic-model';sha=hashlib.sha256(data).hexdigest();identity=sha+'.ckpt'
            (root/'files'/identity).write_bytes(data)
            row={'id':'synthetic-mom','name':'mom','engine':'GPT-SoVITS','weights':[{'file':identity,'filename':'mom-e5.ckpt','sha256':sha,'bytes':len(data),'kind':'gpt'}],
                 'references':[],'previews':[],'recommended_weights':{'gpt':identity}}
            (root/'catalog.json').write_text(json.dumps({'items':[row]}),encoding='utf-8')
            self.assertFalse(organize(root)['applied'])
            result=organize(root,apply=True)
            self.assertEqual(result['roles'],1)
            self.assertEqual((root/'mom/weights/mom-e5.ckpt').read_bytes(),data)
            after=json.loads((root/'catalog.json').read_text())['items'][0]
            self.assertEqual(after['weights'][0]['file'],identity)
            self.assertEqual(after['recommended_weights'],row['recommended_weights'])
            self.assertEqual(after['weights'][0]['relative_path'],'mom/weights/mom-e5.ckpt')
            self.assertEqual(organize(root,apply=True)['moves'],{})

    def test_public_rvc_relocation_preserves_pinned_config_and_refuses_wrong_model(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);legacy=root/'manbo-tlme';legacy.mkdir()
            weights=[]
            config={'engine':'rvc'}
            for name,key in [('model.pth','rvc_model'),('added.index','rvc_index')]:
                data=('synthetic '+name).encode();path=legacy/name;path.write_bytes(data)
                sha=hashlib.sha256(data).hexdigest()
                weights.append({'filename':name,'path':str(path),'sha256':sha,'bytes':len(data)})
                config[key]=str(path);config[key+'_sha256']=sha
            frozen=json.dumps(config).encode();(legacy/'singing-voice.config').write_bytes(frozen)
            (root/'catalog.json').write_text(json.dumps({'items':[{'id':'manbo-tlme','name':'曼波 · 合成测试','engine':'RVC','weights':weights}]}),encoding='utf-8')
            organize(root,shared=True,apply=True)
            relocated=root/'曼波/weights/singing-voice.config'
            self.assertEqual(relocated.read_bytes(),frozen)
            self.assertEqual(load_singing_config(relocated)['rvc_model'],str(root/'曼波/weights/model.pth'))
            (root/'曼波/weights/model.pth').write_bytes(b'wrong-model')
            with self.assertRaisesRegex(ValueError,'relocated_singing_weight_hash_mismatch'):
                load_singing_config(relocated)

    def test_escape_file_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/'catalog.json').write_text(json.dumps({'items':[{'id':'x','name':'x','engine':'GPT-SoVITS','weights':[{'file':'a'*64+'.ckpt','filename':'x.ckpt','relative_path':'../outside','sha256':'a'*64,'bytes':1}]}]}))
            with self.assertRaises(ValueError):organize(root,apply=True)

    def test_failed_publication_restores_files_and_role_metadata(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/'files').mkdir();(root/'mom').mkdir()
            data=b'synthetic-model';sha=hashlib.sha256(data).hexdigest();identity=sha+'.ckpt'
            source=root/'files'/identity;source.write_bytes(data)
            metadata=root/'mom/role.json';metadata.write_bytes(b'{"previous":true}')
            before=json.dumps({'items':[{'id':'x','name':'mom','engine':'GPT-SoVITS','weights':[{'file':identity,'filename':'mom.ckpt','sha256':sha,'bytes':len(data)}]}]}).encode()
            (root/'catalog.json').write_bytes(before)
            from organize_voice_library import atomic as real_atomic
            def fail_index(path,value):
                if path==root/'catalog.json':raise OSError('synthetic publication failure')
                real_atomic(path,value)
            with patch('organize_voice_library.atomic',side_effect=fail_index):
                with self.assertRaises(OSError):organize(root,apply=True)
            self.assertEqual(source.read_bytes(),data)
            self.assertFalse((root/'mom/weights/mom.ckpt').exists())
            self.assertEqual(metadata.read_bytes(),b'{"previous":true}')
            self.assertEqual((root/'catalog.json').read_bytes(),before)


if __name__=='__main__':unittest.main()
