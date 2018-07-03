// Package dfc is a scalable object-storage based caching system with Amazon and Google Cloud backends.
/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 *
 */
package dfc

import "errors"

// NewIostatRunner initalizes iostatrunner struct with default values.
// TODO: For now iostat is not supported for mac, so values may not valid.
func NewIostatRunner() *iostatrunner {
	return &iostatrunner{
		chsts:       make(chan struct{}, 1),
		CPUidle:     "(error: iostat unavailable)",
		Disk:        make(map[string]simplekvs, 0),
		metricnames: make([]string, 0),
	}
}

// iostat -cdxtm 10
func (r *iostatrunner) run() (err error) {
	assert(false, "niy")
	return nil
}

func (r *iostatrunner) stop(err error) {
	assert(false, "niy")
}

func (r *iostatrunner) isZeroUtil(dev string) bool {
	return true
}

func (r *iostatrunner) getMaxUtil(disks StringSet) (maxutil float64) {
	return float64(-1)
}

func CheckIostatVersion() error {
	return errors.New("Not yet implemented")
}

func (r *iostatrunner) getDiskUtilizationFromFileSystem(path string) (utilization float32, ok bool) {
	return float32(-1), false
}

func (r *iostatrunner) getDiskUtilizationFromPath(path string) (utilization float32, ok bool) {
	return float32(-1), false
}

func getFileSystemUsingMountPath(filePath string) (fileSystem string) {
	return
}

func getFileSystemFromPath(fsPath string) (fileSystem string) {
	return
}

func getDiskFromFileSystem(fileSystem string) (disks StringSet) {
	return make(StringSet)
}

func getMountPathFromFilePath(filePath string) (mountPath string) {
	return
}

func getDisksFromLsblkOutput(lsblkOutputBytes []byte, fileSystem string) (disks StringSet) {
	return make(StringSet)
}
