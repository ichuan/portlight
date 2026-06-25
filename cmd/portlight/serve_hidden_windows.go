//go:build windows

package main

import (
	"os"
	"strings"
	"syscall"
)

func platformHidden(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}

func platformUnsafeName(name string) bool {
	return strings.Contains(name, "~")
}

func platformUnsafeInfo(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
