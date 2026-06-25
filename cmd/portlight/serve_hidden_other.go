//go:build !windows

package main

import "os"

func platformHidden(info os.FileInfo) bool {
	return false
}

func platformUnsafeName(name string) bool {
	return false
}

func platformUnsafeInfo(info os.FileInfo) bool {
	return false
}
