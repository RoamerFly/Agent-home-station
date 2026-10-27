"""Synthetic launch regression tests. No real services or scheduled tasks changed."""
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import ctypes
from ctypes import wintypes

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('hidden_process', str(ROOT/'scripts/hidden_process.pyw'))
spec = importlib.util.spec_from_loader(loader.name, loader)
hidden = importlib.util.module_from_spec(spec)
loader.exec_module(hidden)


class HiddenProcessTests(unittest.TestCase):
    def wait_pid(self, pid):
        kernel=ctypes.windll.kernel32
        kernel.OpenProcess.restype=wintypes.HANDLE
        handle=kernel.OpenProcess(0x100000, False, pid)
        if handle:
            try:self.assertEqual(kernel.WaitForSingleObject(wintypes.HANDLE(handle),10000),0)
            finally:kernel.CloseHandle(wintypes.HANDLE(handle))
    def test_installer_is_gui_entry_and_has_no_boot_or_logon_trigger(self):
        text = (ROOT/'scripts/Install-Local-YuE-Service.ps1').read_text(encoding='utf8')
        self.assertIn('pythonw.exe', text)
        self.assertNotIn('New-ScheduledTaskTrigger', text)
        self.assertIn('hidden_process.pyw', text)
        self.assertNotIn('-Trigger ', text)

    def test_request_failure_is_explicit_and_does_not_run_shell_text(self):
        with tempfile.TemporaryDirectory() as tmp:
            request=Path(tmp)/'request.json'
            request.write_text(json.dumps({'arguments':'not an argv list'}))
            hidden.request_launch(request)
            result=json.loads(request.with_suffix('.result.json').read_text())
            self.assertIn('invalid arguments', result['error'])
            self.assertNotIn('pid',result)

    @unittest.skipUnless(os.name == 'nt', 'native Windows console regression')
    def test_gui_entry_preserves_unicode_argv_and_console_is_never_allocated(self):
        with tempfile.TemporaryDirectory(prefix='鲸鱼 hidden ') as tmp:
            root=Path(tmp);result=root/'child.json'
            child_code=root/'child.py'
            child_code.write_text('import ctypes,json,sys\nfrom pathlib import Path\nPath(sys.argv[1]).write_text(json.dumps({"console":ctypes.windll.kernel32.GetConsoleWindow(),"value":sys.argv[2]}),encoding="utf8")\n',encoding='utf8')
            request=root/'request.json'
            value='中文 $literal & "quoted"'
            request.write_text(json.dumps({'arguments':[sys.executable,str(child_code),str(result),value],'cwd':str(root),'stdout':str(root/'out'),'stderr':str(root/'err')}),encoding='utf8')
            completed=subprocess.run([str(Path(sys.executable).with_name('pythonw.exe')),str(ROOT/'scripts/hidden_process.pyw'),'--request',str(request)],creationflags=subprocess.CREATE_NO_WINDOW,timeout=10)
            self.assertEqual(completed.returncode,0)
            for _ in range(100):
                if result.exists():break
                time.sleep(.05)
            output=json.loads(result.read_text(encoding='utf8'))
            self.assertEqual(output,{'console':0,'value':value})
            self.assertFalse(json.loads(request.with_suffix('.result.json').read_text())['console_allocated'])
            self.wait_pid(json.loads(request.with_suffix('.result.json').read_text())['pid'])

    @unittest.skipUnless(os.name == 'nt', 'native Windows PowerShell regression')
    def test_project_helper_launches_powershell_without_terminal_host(self):
        # Use pwsh on PATH, including the Codex-bundled runtime on this machine.
        shell=shutil.which('pwsh.exe') or shutil.which('powershell.exe')
        with tempfile.TemporaryDirectory(prefix='鲸鱼 pwsh ') as tmp:
            root=Path(tmp);child=root/'child.ps1';out=root/'out';err=root/'err'
            child.write_text("Add-Type -Name NativeConsole -Namespace Smoke -MemberDefinition '[System.Runtime.InteropServices.DllImport(\"kernel32.dll\")] public static extern System.IntPtr GetConsoleWindow();'\n[Smoke.NativeConsole]::GetConsoleWindow().ToInt64()\n",encoding='utf8')
            launcher=root/'launch.ps1'
            launcher.write_text("& '"+str(ROOT/'scripts/Start-HiddenProcess.ps1')+"' -FilePath '"+shell+"' -Arguments @('-NoProfile','-NonInteractive','-File','"+str(child)+"') -StandardOutput '"+str(out)+"' -StandardError '"+str(err)+"' | ConvertTo-Json -Compress",encoding='utf-8-sig')
            start=subprocess.run([shell,'-NoProfile','-NonInteractive','-File',str(launcher)],capture_output=True,encoding='utf8',creationflags=subprocess.CREATE_NO_WINDOW,timeout=20)
            self.assertEqual(start.returncode,0,start.stderr)
            self.assertGreater(json.loads(start.stdout)['pid'],0)
            for _ in range(100):
                if out.exists() and out.read_text().strip():break
                time.sleep(.05)
            self.assertEqual(out.read_text().strip(),'0',err.read_text())
            self.wait_pid(json.loads(start.stdout)['pid'])

    @unittest.skipUnless(os.name == 'nt', 'native Windows argv/exit regression')
    def test_synchronous_native_calls_preserve_argv_exit_code_and_no_console(self):
        value='中文 "双引号" $literal & end\\'
        code='import sys,json,ctypes; print(json.dumps([ctypes.windll.kernel32.GetConsoleWindow(),sys.argv[1:]])); sys.exit(7)'
        quote=lambda x:"'"+str(x).replace("'","''")+"'"
        shells={shutil.which('pwsh.exe'),shutil.which('powershell.exe')} - {None}
        for shell in shells:
            with self.subTest(shell=shell),tempfile.TemporaryDirectory() as tmp:
                script=Path(tmp)/'invoke.ps1'
                script.write_text('& '+quote(ROOT/'scripts/Invoke-HiddenNative.ps1')+' -FilePath '+quote(sys.executable)+' -Arguments @('+','.join(map(quote,['-c',code,value,'']))+')\nexit $LASTEXITCODE',encoding='utf-8-sig')
                result=subprocess.run([shell,'-NoProfile','-NonInteractive','-File',str(script)],capture_output=True,encoding='utf8',creationflags=subprocess.CREATE_NO_WINDOW,timeout=20)
                self.assertEqual(result.returncode,7,result.stderr)
                self.assertEqual(json.loads(result.stdout),[0,[value,'']])


if __name__ == '__main__':unittest.main()
