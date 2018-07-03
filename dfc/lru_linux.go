// Package dfc is a scalable object-storage based caching system with Amazon and Google Cloud backends.
/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 *
 */
package dfc

import (
	"os"
	"syscall"
	"time"

	"github.com/NVIDIA/dfcpub/3rdparty/glog"
)

func getFSStats(mountPath string) (blocks uint64, bavail uint64, bsize int64, err error) {
	fsStats := syscall.Statfs_t{}
	if err = syscall.Statfs(mountPath, &fsStats); err != nil {
		glog.Errorf("Failed to fsStats mount path %q, err: %v", mountPath, err)
		return
	}
	return fsStats.Blocks, fsStats.Bavail, fsStats.Bsize, nil
}

func getAmTimes(osfi os.FileInfo) (time.Time, time.Time, *syscall.Stat_t) {
	stat := osfi.Sys().(*syscall.Stat_t)
	atime := time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
	// atime controversy, see e.g. https://en.wikipedia.org/wiki/Stat_(system_call)#Criticism_of_atime
	mtime := osfi.ModTime()
	return atime, mtime, stat
}
