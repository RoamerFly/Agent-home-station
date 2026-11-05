from pathlib import Path
import tempfile
import unittest
from rvc_backend import layout,convert


class BackendTests(unittest.TestCase):
    def test_explicit_layouts_use_their_exact_inference_signature(self):
        class Fake:
            def vc_single(self,*args):
                self.args=args
                return '状态：成功',(40000,[1,2])
        fake=Fake()
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            with self.assertRaises(ValueError):layout(root)
            legacy=root/'infer/modules/vc/modules.py';legacy.parent.mkdir(parents=True);legacy.touch()
            self.assertEqual(layout(root),'legacy-231006')
            convert(fake,'source',0,'index',.65,.33,layout(root))
            self.assertEqual(fake.args,(0,'source',0,None,'rmvpe','index',None,.65,3,0,.5,.33))
            modern=root/'infer/vc/modules.py';modern.parent.mkdir(parents=True);modern.touch()
            self.assertEqual(layout(root),'official-2026')
            result=convert(fake,'source',2,'index',.35,.33,layout(root))
            self.assertEqual(fake.args,(0,'source',2,'rmvpe','index',.35,0,.5,.33))
            self.assertEqual(result[1][0],40000)
        with self.assertRaises(ValueError):convert(fake,'x',0,'',0,0,'unknown')


if __name__=='__main__':unittest.main()
