import sys,unittest,tempfile
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from yue_bridge import archive_completed_intermediates
class CleanupTests(unittest.TestCase):
 def test_completed_keeps_final_and_download(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d);state=root/'jobs';state.mkdir();jid='a'*32
   final=state/(jid+'-rvc.wav');final.write_bytes(b'final');(state/'download.mp3').write_bytes(b'original')
   work=state/(jid+'-rvc-voice-work');work.mkdir();(work/'vocals.wav').write_bytes(b'temp')
   source=state/(jid+'-source.audio');source.write_bytes(b'clip')
   job={'state':'running','file':final.name,'bytes':5}
   self.assertEqual(archive_completed_intermediates(state,jid,job,root/'pending'),[])
   job['state']='completed';self.assertEqual(len(archive_completed_intermediates(state,jid,job,root/'pending')),2)
   self.assertTrue(final.exists());self.assertTrue((state/'download.mp3').exists());self.assertFalse(work.exists())
   self.assertEqual(archive_completed_intermediates(state,jid,job,root/'pending'),[])
 def test_missing_or_changed_final_keeps_scratch(self):
  with tempfile.TemporaryDirectory() as d:
   state=Path(d);jid='b'*32;work=state/(jid+'-rvc-voice-work');work.mkdir()
   self.assertEqual(archive_completed_intermediates(state,jid,{'state':'completed','file':'absent.wav','bytes':4},state/'pending'),[]);self.assertTrue(work.exists())
if __name__=='__main__':unittest.main()
