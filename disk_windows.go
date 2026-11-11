//go:build windows

package main

import "golang.org/x/sys/windows"

func availableDisk(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, unused uint64
	err = windows.GetDiskFreeSpaceEx(p, &free, &total, &unused)
	return free, err
}
