package agenthost

import (
	"runtime"
	"testing"
)

func TestWindowsPathMappingAndValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL path mapping")
	}
	for input, want := range map[string]string{`E:\项目 目录\子目录`: `E:\项目 目录\子目录`, "/mnt/e/项目 目录": `E:\项目 目录`, "/mnt/c": `C:\`, `E:\..\file`: `E:\file`, `E:\dir\..\file`: `E:\file`} {
		local, e := WindowsLocalPath(input)
		if e != nil {
			t.Fatal(e)
		}
		got, e := windowsPath(local)
		if e != nil || got != want {
			t.Fatal(input, got, want, e)
		}
	}
	for _, invalid := range []string{"/home/fly", "relative", "/mnt/ab/path", "/mnt/1/x", "/mnt/e/../other"} {
		if _, e := WindowsLocalPath(invalid); e == nil {
			t.Fatal(invalid)
		}
	}
	if e := (WindowsConfig{Pythonw: "/mnt/e/python.exe", Helper: "/mnt/e/tool.pyw", Spool: "/mnt/e/private"}).Validate(); e == nil {
		t.Fatal("console executable accepted")
	}
}
