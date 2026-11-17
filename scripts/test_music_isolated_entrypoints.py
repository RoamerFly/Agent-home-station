import subprocess,sys,unittest
from pathlib import Path

class IsolatedMusicEntrypoints(unittest.TestCase):
 def test_cli_help_under_isolated_python(self):
  for name in ('singing_voice.py','rvc_voice.py'):
   with self.subTest(script=name):
    result=subprocess.run([sys.executable,'-I',str(Path(__file__).with_name(name)),'--help'],capture_output=True,text=True,timeout=20)
    self.assertEqual(result.returncode,0,result.stderr)
    self.assertIn('--output',result.stdout)
if __name__=='__main__':unittest.main()
