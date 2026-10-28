"""Exercise the real hidden LPAC broker; never use an unrestricted shell."""
import ctypes
from ctypes import wintypes
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest


@unittest.skipUnless(os.name == 'nt', 'requires Windows LPAC')
class HiddenWindowsTests(unittest.TestCase):
    def assert_no_window(self, pids):
        found = []
        @ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
        def inspect(hwnd, unused):
            pid = wintypes.DWORD()
            ctypes.windll.user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
            if pid.value in pids and ctypes.windll.user32.IsWindowVisible(hwnd): found.append(pid.value)
            return True
        ctypes.windll.user32.EnumWindows(inspect, 0)
        self.assertEqual(found, [])

    def launch(self, base, command, sandbox=True):
        base = Path(base)
        spool, workspace = base/'control', base/'session'
        spool.mkdir(); workspace.mkdir()
        request = spool/'request.json'
        request.write_text(json.dumps({'sandbox':sandbox,'cwd':str(workspace),'roots':[str(workspace)],'read_only':[], 'command':command,'shell':str(Path(os.environ['SystemRoot'])/'System32/cmd.exe')}),encoding='utf8')
        pythonw = Path(sys.executable).with_name('pythonw.exe')
        self.assertTrue(pythonw.is_file())
        child = subprocess.Popen([str(pythonw),str(Path(__file__).with_name('windows_agent_host.pyw')),str(request)],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,creationflags=subprocess.CREATE_NO_WINDOW)
        self.addCleanup(lambda: child.kill() if child.poll() is None else None)
        return child, spool, workspace

    def result(self, spool):
        for _ in range(200):
            path=spool/'result.json'
            if path.exists(): return json.loads(path.read_text(encoding='utf8'))
            time.sleep(.1)
        self.fail('LPAC helper did not return')

    def test_real_exit_and_write_in_workspace(self):
        with tempfile.TemporaryDirectory() as base:
            child,spool,workspace=self.launch(base,'echo synthetic>inside.txt & exit /b 7')
            result=self.result(spool)
            self.assertEqual(result.get('exit_code'),7,result)
            self.assertIn('synthetic',(workspace/'inside.txt').read_text())
            self.assertEqual(child.wait(timeout=5),0)

    def test_outside_read_and_write_denied_even_by_child(self):
        with tempfile.TemporaryDirectory() as base:
            outside=Path(base)/'outside.txt';outside.write_text('private-value')
            cmd=f'cmd.exe /d /c type "{outside}" & echo leak>"{Path(base)/"escape.txt"}"'
            child,spool,workspace=self.launch(base,cmd)
            result=self.result(spool)
            self.assertNotEqual(result.get('exit_code'),0,result)
            self.assertNotIn('private-value',(spool/'output.log').read_text(errors='replace'))
            self.assertFalse((Path(base)/'escape.txt').exists())
            child.wait(timeout=5)

    def test_cancel_hidden_process_tree(self):
        with tempfile.TemporaryDirectory() as base:
            child,spool,workspace=self.launch(base,'for /L %i in (1,0,2) do @echo waiting>nul')
            for _ in range(100):
                if (spool/'running.json').exists(): break
                time.sleep(.1)
            running=json.loads((spool/'running.json').read_text())
            self.assert_no_window({running['pid'],running['helper_pid']})
            (spool/'cancel').touch()
            result=self.result(spool)
            self.assertTrue(result.get('cancelled'),result)
            self.assertNotEqual(result['exit_code'],0)
            child.wait(timeout=5)

    def test_legacy_request_rejected(self):
        with tempfile.TemporaryDirectory() as base:
            child,spool,workspace=self.launch(base,'echo forbidden>inside.txt',sandbox=False)
            result=self.result(spool)
            self.assertIn('sandbox_required',result.get('error',''))
            self.assertFalse((workspace/'inside.txt').exists())
            child.wait(timeout=5)


if __name__=='__main__': unittest.main()
