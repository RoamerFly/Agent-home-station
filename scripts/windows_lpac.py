"""Launch a process in a low privilege AppContainer. No unrestricted fallback.

The caller is a trusted broker, not Agent code. Paths are broker-derived. This
module never grants broad capabilities or exposes management credentials.
"""
import ctypes as c
from ctypes import wintypes as w
import hashlib
import os
from pathlib import Path
import subprocess
import time


class SECURITY_CAPABILITIES(c.Structure):
    _fields_ = [("AppContainerSid", c.c_void_p), ("Capabilities", c.c_void_p),
                ("CapabilityCount", w.DWORD), ("Reserved", w.DWORD)]


class STARTUPINFO(c.Structure):
    _fields_ = [("cb", w.DWORD), ("lpReserved", w.LPWSTR), ("lpDesktop", w.LPWSTR),
                ("lpTitle", w.LPWSTR), ("dwX", w.DWORD), ("dwY", w.DWORD),
                ("dwXSize", w.DWORD), ("dwYSize", w.DWORD), ("dwXCountChars", w.DWORD),
                ("dwYCountChars", w.DWORD), ("dwFillAttribute", w.DWORD),
                ("dwFlags", w.DWORD), ("wShowWindow", w.WORD), ("cbReserved2", w.WORD),
                ("lpReserved2", c.c_void_p), ("hStdInput", w.HANDLE),
                ("hStdOutput", w.HANDLE), ("hStdError", w.HANDLE)]


class STARTUPINFOEX(c.Structure):
    _fields_ = [("StartupInfo", STARTUPINFO), ("lpAttributeList", c.c_void_p)]


class PROCESS_INFORMATION(c.Structure):
    _fields_ = [("hProcess", w.HANDLE), ("hThread", w.HANDLE),
                ("dwProcessId", w.DWORD), ("dwThreadId", w.DWORD)]


def launch(argv, cwd, writable, readonly, output):
    """Return a process handle, pid and cleanup. Child inherits only stdio.

    LPAC additionally opts out of implicit ALL APPLICATION PACKAGES grants.
    Each distinct mount policy receives its own SID, so ACLs from a previous
    administrator operation cannot widen an ordinary session's policy.
    """
    if os.name != "nt":
        raise RuntimeError("windows_lpac_unavailable")
    import msvcrt
    k = c.WinDLL("kernel32", use_last_error=True)
    u = c.WinDLL("userenv", use_last_error=True)
    adv = c.WinDLL("advapi32", use_last_error=True)
    k.GetCurrentProcess.restype = w.HANDLE
    k.CloseHandle.argtypes = [w.HANDLE]
    k.LocalFree.argtypes=[c.c_void_p]
    adv.FreeSid.argtypes=[c.c_void_p]
    k.TerminateProcess.argtypes=[w.HANDLE,w.UINT]
    k.GetExitCodeProcess.argtypes=[w.HANDLE,c.POINTER(w.DWORD)]
    k.TerminateJobObject.argtypes=[w.HANDLE,w.UINT]
    k.CreateJobObjectW.argtypes=[c.c_void_p,w.LPCWSTR]
    k.CreateJobObjectW.restype=w.HANDLE
    k.SetInformationJobObject.argtypes=[w.HANDLE,c.c_int,c.c_void_p,w.DWORD]
    k.AssignProcessToJobObject.argtypes=[w.HANDLE,w.HANDLE]
    k.ResumeThread.argtypes=[w.HANDLE]
    k.InitializeProcThreadAttributeList.argtypes = [c.c_void_p, w.DWORD, w.DWORD, c.POINTER(c.c_size_t)]
    k.UpdateProcThreadAttribute.argtypes = [c.c_void_p, w.DWORD, c.c_size_t, c.c_void_p, c.c_size_t, c.c_void_p, c.c_void_p]
    k.DeleteProcThreadAttributeList.argtypes = [c.c_void_p]
    k.CreateProcessW.argtypes = [w.LPCWSTR, w.LPWSTR, c.c_void_p, c.c_void_p, w.BOOL,
                                w.DWORD, c.c_void_p, w.LPCWSTR, c.POINTER(STARTUPINFOEX), c.POINTER(PROCESS_INFORMATION)]
    u.CreateAppContainerProfile.argtypes = [w.LPCWSTR, w.LPCWSTR, w.LPCWSTR, c.c_void_p, w.DWORD, c.POINTER(c.c_void_p)]
    u.DeriveAppContainerSidFromAppContainerName.argtypes = [w.LPCWSTR, c.POINTER(c.c_void_p)]
    adv.ConvertSidToStringSidW.argtypes = [c.c_void_p, c.POINTER(w.LPWSTR)]
    sid = c.c_void_p()
    for original in [*writable, *readonly]:
        path = Path(original)
        for parent in [path, *path.parents]:
            if parent.is_symlink() or parent.is_junction():
                raise RuntimeError("lpac_mount_reparse_point")
    paths = [(str(Path(p).resolve(strict=True)), "M") for p in writable]
    paths += [(str(Path(p).resolve(strict=True)), "RX") for p in readonly]
    for p, _ in paths:
        if Path(p).parent == Path(p) or Path(p).is_junction() or Path(p).is_symlink():
            raise RuntimeError("lpac_invalid_mount")
    name = "WechatAgent." + hashlib.sha256(repr(sorted(paths)).encode()).hexdigest()[:32]
    hr = u.CreateAppContainerProfile(name, name, "Managed WeChat workspace", None, 0, c.byref(sid))
    if hr != 0 and u.DeriveAppContainerSidFromAppContainerName(name, c.byref(sid)) != 0:
        raise RuntimeError("lpac_profile_unavailable")
    sid_text = w.LPWSTR()
    if not adv.ConvertSidToStringSidW(sid, c.byref(sid_text)):
        raise c.WinError(c.get_last_error())
    try:
        for path, mode in paths:
            # No /T: grants inherit to descendants; unrelated objects unchanged.
            subprocess.run([str(Path(os.environ["SystemRoot"]) / "System32/icacls.exe"), path,
                            "/grant", "*" + sid_text.value + ":(OI)(CI)" + mode, "/Q"], check=True,
                           stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                           creationflags=subprocess.CREATE_NO_WINDOW)
    finally:
        k.LocalFree(c.cast(sid_text, c.c_void_p))
    size = c.c_size_t()
    k.InitializeProcThreadAttributeList(None, 3, 0, c.byref(size))
    buf = c.create_string_buffer(size.value)
    if not k.InitializeProcThreadAttributeList(buf, 3, 0, c.byref(size)):
        raise c.WinError(c.get_last_error())
    si = STARTUPINFOEX()
    si.StartupInfo.cb = c.sizeof(si)
    si.lpAttributeList = c.cast(buf, c.c_void_p)
    caps = SECURITY_CAPABILITIES(sid, None, 0, 0)
    optout = w.DWORD(1)
    handle = msvcrt.get_osfhandle(output.fileno())
    os.set_handle_inheritable(handle, True)
    stdin = open(os.devnull, "rb")
    input_handle = msvcrt.get_osfhandle(stdin.fileno())
    os.set_handle_inheritable(input_handle, True)
    handles = (w.HANDLE * 2)(handle, input_handle)
    try:
        for key, value in [(0x20009, caps), (0x2000F, optout), (0x20002, handles)]:
            if not k.UpdateProcThreadAttribute(buf, 0, key, c.byref(value), c.sizeof(value), None, None):
                raise c.WinError(c.get_last_error())
        si.StartupInfo.dwFlags = 0x100
        si.StartupInfo.hStdInput = input_handle
        si.StartupInfo.hStdOutput = handle
        si.StartupInfo.hStdError = handle
        # Explicit clean environment, not inherited credentials/WSL interop.
        env = {"LOCALAPPDATA":str(Path(writable[0])/"scratch"),"APPDATA":str(Path(writable[0])/"scratch"),"USERPROFILE":str(writable[0]),"SystemDrive":os.environ.get("SystemDrive","C:"),"ALLUSERSPROFILE":os.environ.get("ProgramData",r"C:\ProgramData"),"ProgramData":os.environ.get("ProgramData",r"C:\ProgramData"),"SystemRoot": os.environ["SystemRoot"], "WINDIR": os.environ["SystemRoot"],
               "PATH": str(Path(os.environ["SystemRoot"]) / "System32"),
               "TEMP": str(Path(writable[0]) / "scratch"), "TMP": str(Path(writable[0]) / "scratch")}
        Path(env["TEMP"]).mkdir(exist_ok=True)
        block = c.create_unicode_buffer("\0".join(k + "=" + v for k, v in sorted(env.items())) + "\0\0")
        pi = PROCESS_INFORMATION()
        line=subprocess.list2cmdline(argv)
        if Path(argv[0]).name.lower()=="cmd.exe" and len(argv)==5:
            line=subprocess.list2cmdline(argv[:4])+' "'+argv[4]+'"'
        cmd = c.create_unicode_buffer(line)
        if not k.CreateProcessW(argv[0], cmd, None, None, True, 0x08000000 | 0x80000 | 0x400 | 4,
                                block, str(cwd), c.byref(si), c.byref(pi)):
            raise c.WinError(c.get_last_error())
        job=k.CreateJobObjectW(None,None)
        limits=EXTENDED_LIMIT();limits.BasicLimitInformation.LimitFlags=0x2000
        if not job or not k.SetInformationJobObject(job,9,c.byref(limits),c.sizeof(limits)) or not k.AssignProcessToJobObject(job,pi.hProcess):
            k.TerminateProcess(pi.hProcess,1);k.CloseHandle(pi.hThread);k.CloseHandle(pi.hProcess)
            if job:k.CloseHandle(job)
            raise RuntimeError("lpac_job_isolation_failed")
        k.ResumeThread(pi.hThread);k.CloseHandle(pi.hThread)
        return SandboxedProcess(k,pi.hProcess,job,pi.dwProcessId)
    finally:
        os.set_handle_inheritable(handle, False)
        stdin.close()
        k.DeleteProcThreadAttributeList(buf)
        adv.FreeSid(sid)

class BASIC_LIMIT(c.Structure):
    _fields_=[("PerProcessUserTimeLimit",c.c_int64),("PerJobUserTimeLimit",c.c_int64),("LimitFlags",w.DWORD),("MinimumWorkingSetSize",c.c_size_t),("MaximumWorkingSetSize",c.c_size_t),("ActiveProcessLimit",w.DWORD),("Affinity",c.c_size_t),("PriorityClass",w.DWORD),("SchedulingClass",w.DWORD)]
class IO_COUNTERS(c.Structure):
    _fields_=[(name,c.c_uint64) for name in ["ReadOperationCount","WriteOperationCount","OtherOperationCount","ReadTransferCount","WriteTransferCount","OtherTransferCount"]]
class EXTENDED_LIMIT(c.Structure):
    _fields_=[("BasicLimitInformation",BASIC_LIMIT),("IoInfo",IO_COUNTERS),("ProcessMemoryLimit",c.c_size_t),("JobMemoryLimit",c.c_size_t),("PeakProcessMemoryUsed",c.c_size_t),("PeakJobMemoryUsed",c.c_size_t)]
class SandboxedProcess:
    def __init__(self,k,handle,job,pid):self.k,self.handle,self.job,self.pid,self.returncode=k,handle,job,pid,None
    def poll(self):
        if self.returncode is not None:return self.returncode
        code=w.DWORD()
        if not self.k.GetExitCodeProcess(self.handle,c.byref(code)):raise c.WinError(c.get_last_error())
        if code.value!=259:
            self.returncode=code.value;self.k.CloseHandle(self.handle);self.handle=None
            self.k.CloseHandle(self.job);self.job=None
        return self.returncode
    def wait(self,timeout=None):
        started=time.monotonic()
        while self.poll() is None:
            if timeout is not None and time.monotonic()-started>timeout:raise subprocess.TimeoutExpired("lpac",timeout)
            time.sleep(.05)
        return self.returncode
    def kill(self):
        if self.job:self.k.TerminateJobObject(self.job,1)
    def __del__(self):
        if self.job:self.k.CloseHandle(self.job)
        if self.handle:self.k.CloseHandle(self.handle)
