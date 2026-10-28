"""Pinned directories and no-replace publication for the administrative copier.

POSIX operations use directory descriptors. Windows locks each ancestor against
rename and opens reparse points themselves instead of following them. This is
not a host Shell or an Agent permission grant.
"""
import ctypes
import errno
import os
from pathlib import Path
import stat
import uuid


if os.name == 'nt':
    from ctypes import wintypes
    import msvcrt
    class FileInfo(ctypes.Structure):
        _fields_=[('attributes',wintypes.DWORD),('created',wintypes.FILETIME),
                  ('accessed',wintypes.FILETIME),('written',wintypes.FILETIME),
                  ('volume',wintypes.DWORD),('size_high',wintypes.DWORD),
                  ('size_low',wintypes.DWORD),('links',wintypes.DWORD),
                  ('index_high',wintypes.DWORD),('index_low',wintypes.DWORD)]
    kernel=ctypes.WinDLL('kernel32',use_last_error=True)
    kernel.CreateFileW.argtypes=[wintypes.LPCWSTR,wintypes.DWORD,wintypes.DWORD,
                                ctypes.c_void_p,wintypes.DWORD,wintypes.DWORD,wintypes.HANDLE]
    kernel.CreateFileW.restype=wintypes.HANDLE
    kernel.GetFileInformationByHandle.argtypes=[wintypes.HANDLE,ctypes.POINTER(FileInfo)]
    kernel.GetFileInformationByHandle.restype=wintypes.BOOL
    kernel.CloseHandle.argtypes=[wintypes.HANDLE]
    kernel.CloseHandle.restype=wintypes.BOOL
    kernel.MoveFileW.argtypes=[wintypes.LPCWSTR,wintypes.LPCWSTR]
    kernel.MoveFileW.restype=wintypes.BOOL


def _name(value):
    value=str(value)
    if not value or value in ('.','..') or Path(value).name!=value or '\0' in value or os.name=='nt' and ':' in value:
        raise ValueError('migration_invalid_leaf_name')
    return value


def _windows_path(path):
    text=str(path)
    if not text.startswith('\\\\?\\'):
        text='\\\\?\\UNC\\'+text[2:] if text.startswith('\\\\') else '\\\\?\\'+text
    return text


def _win_open(path,directory):
    # No FILE_SHARE_DELETE: keep this object/ancestor stable until close.
    # FILE_LIST_DIRECTORY participates in Windows share-access checking;
    # READ_ATTRIBUTES alone does not prevent a concurrent directory rename.
    handle=kernel.CreateFileW(_windows_path(path),0x81 if directory else 0x80000000,3,None,3,
                              0x00200000|(0x02000000 if directory else 0),None)
    if handle==ctypes.c_void_p(-1).value:raise ctypes.WinError(ctypes.get_last_error())
    try:
        info=FileInfo()
        if not kernel.GetFileInformationByHandle(handle,ctypes.byref(info)):
            raise ctypes.WinError(ctypes.get_last_error())
        if info.attributes & 0x400:raise ValueError('migration_link_not_allowed')
        if bool(info.attributes & 0x10)!=directory:raise ValueError('migration_unexpected_file_type')
        return handle
    except BaseException:
        kernel.CloseHandle(handle)
        raise


class Directory:
    def __init__(self,path,create=False):
        self.path=Path(os.path.abspath(path));self.fd=None;self.handles=[]
        try:
            if os.name=='nt':
                current=Path(self.path.anchor)
                self.handles.append(_win_open(current,True))
                for part in self.path.parts[1:]:
                    current=current/part
                    try:handle=_win_open(current,True)
                    except FileNotFoundError:
                        if not create:raise
                        try:current.mkdir(mode=0o700)
                        except FileExistsError:pass
                        handle=_win_open(current,True)
                    self.handles.append(handle)
            else:
                if not hasattr(os,'O_NOFOLLOW') or not hasattr(os,'O_DIRECTORY'):
                    raise ValueError('migration_directory_backend_unavailable')
                flags=os.O_RDONLY|os.O_NOFOLLOW|os.O_DIRECTORY
                self.fd=os.open(self.path.anchor,flags)
                for part in self.path.parts[1:]:
                    try:next_fd=os.open(part,flags,dir_fd=self.fd)
                    except FileNotFoundError:
                        if not create:raise
                        try:os.mkdir(part,0o700,dir_fd=self.fd)
                        except FileExistsError:pass
                        next_fd=os.open(part,flags,dir_fd=self.fd)
                    old,self.fd=self.fd,next_fd;os.close(old)
        except BaseException:
            self.close();raise

    def close(self):
        if self.fd is not None:os.close(self.fd);self.fd=None
        for handle in reversed(self.handles):kernel.CloseHandle(handle)
        self.handles=[]

    def __enter__(self):return self
    def __exit__(self,*_):self.close()

    def child(self,name):
        name=_name(name)
        if os.name=='nt':return Directory(self.path/name)
        child=object.__new__(Directory);child.path=self.path/name;child.handles=[]
        child.fd=os.open(name,os.O_RDONLY|os.O_NOFOLLOW|os.O_DIRECTORY,dir_fd=self.fd)
        return child

    def names(self):
        # POSIX scandir duplicates the supplied descriptor; it does not consume it.
        with os.scandir(self.path if os.name=='nt' else self.fd) as entries:
            return sorted(entry.name for entry in entries)

    def lstat(self,name):
        name=_name(name)
        return (self.path/name).lstat() if os.name=='nt' else os.stat(name,dir_fd=self.fd,follow_symlinks=False)

    def open_regular(self,name):
        name=_name(name)
        if os.name=='nt':
            handle=_win_open(self.path/name,False)
            try:fd=msvcrt.open_osfhandle(handle,os.O_RDONLY|os.O_BINARY)
            except BaseException:kernel.CloseHandle(handle);raise
        else:
            try:fd=os.open(name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=self.fd)
            except OSError as exc:
                if exc.errno==errno.ELOOP:raise ValueError('migration_link_not_allowed') from None
                raise
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):raise ValueError('migration_file_not_regular')
            return os.fdopen(fd,'rb')
        except BaseException:os.close(fd);raise

    def temporary(self):
        name='.migration-'+uuid.uuid4().hex
        flags=os.O_WRONLY|os.O_CREAT|os.O_EXCL|getattr(os,'O_BINARY',0)
        if os.name=='nt':fd=os.open(self.path/name,flags,0o600)
        else:fd=os.open(name,flags|os.O_NOFOLLOW,0o600,dir_fd=self.fd)
        try:return name,os.fdopen(fd,'wb')
        except BaseException:
            os.close(fd);self.remove(name);raise

    def remove(self,name):
        name=_name(name)
        try:
            if os.name=='nt':os.unlink(self.path/name)
            else:os.unlink(name,dir_fd=self.fd)
        except FileNotFoundError:pass

    def still_named(self):
        if os.name=='nt':return  # Ancestor handles deny rename, including Junction replacement.
        with Directory(self.path) as current:
            before,after=os.fstat(self.fd),os.fstat(current.fd)
            if (before.st_dev,before.st_ino)!=(after.st_dev,after.st_ino):
                raise ValueError('migration_directory_changed')

    def publish(self,pending,name):
        pending,name=_name(pending),_name(name)
        self.still_named()
        try:
            if os.name=='nt':os.link(self.path/pending,self.path/name,follow_symlinks=False)
            else:os.link(pending,name,src_dir_fd=self.fd,dst_dir_fd=self.fd,follow_symlinks=False)
        except FileExistsError:raise
        except (OSError,NotImplementedError) as exc:
            unsupported=isinstance(exc,NotImplementedError) or exc.errno in (errno.EPERM,errno.EOPNOTSUPP,errno.ENOSYS) or getattr(exc,'winerror',None) in (1,50)
            if not unsupported:raise ValueError('migration_atomic_publish_unavailable') from exc
            try:self.rename_no_replace(pending,name)
            except FileExistsError:raise
            except (OSError,NotImplementedError) as failure:
                raise ValueError('migration_atomic_publish_unavailable') from failure
        if os.name!='nt':os.fsync(self.fd)

    def rename_no_replace(self,pending,name):
        # Never use os.replace/Path.replace or an existence-check plus rename.
        # These OS primitives combine the existence check and publication.
        pending,name=_name(pending),_name(name)
        self.still_named()
        if os.name=='nt':
            if not kernel.MoveFileW(_windows_path(self.path/pending),_windows_path(self.path/name)):
                raise ctypes.WinError(ctypes.get_last_error())
        else:
            library=ctypes.CDLL(None,use_errno=True)
            rename=getattr(library,'renameat2',None)
            if rename is None:raise NotImplementedError('renameat2 unavailable')
            rename.argtypes=[ctypes.c_int,ctypes.c_char_p,ctypes.c_int,ctypes.c_char_p,ctypes.c_uint]
            rename.restype=ctypes.c_int
            if rename(self.fd,os.fsencode(pending),self.fd,os.fsencode(name),1)!=0:
                code=ctypes.get_errno();raise OSError(code,os.strerror(code))


def is_link(info):
    return stat.S_ISLNK(info.st_mode) or bool(getattr(info,'st_file_attributes',0)&0x400)


def source_files(source,excluded,excluded_paths):
    """Enumerate only pinned source directories, rejecting special/link entries."""
    def omit(relative):
        value=relative.as_posix()
        return any(value==item or value.startswith(item+'/') for item in excluded_paths)
    def walk(directory,prefix):
        for name in directory.names():
            relative=prefix/name
            if name in excluded or omit(relative) or name.endswith(('.log','.session.json')) or name.startswith('.env'):continue
            info=directory.lstat(name)
            if is_link(info):raise ValueError('source_link_requires_explicit_registration')
            if stat.S_ISDIR(info.st_mode):
                with directory.child(name) as child:yield from walk(child,relative)
            elif stat.S_ISREG(info.st_mode):yield source/relative,relative,info.st_size
            else:raise ValueError('source_not_regular')
    with Directory(source) as root:yield from walk(root,Path())
