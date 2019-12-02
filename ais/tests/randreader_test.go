// Package ais_test contains AIS integration tests.
/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 */
package ais_test

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/tutils"
	"github.com/NVIDIA/aistore/tutils/tassert"
)

func TestRandomReaderPutStress(t *testing.T) {
	var (
		numworkers = 1000
		numobjects = 10 // NOTE: increase this number if need be ...
		bucket     = "RRTestBucket"
		proxyURL   = getPrimaryURL(t, proxyURLReadOnly)
		wg         = &sync.WaitGroup{}
		dir        = t.Name()
	)
	tutils.CreateFreshBucket(t, proxyURL, bucket)
	for i := 0; i < numworkers; i++ {
		reader, err := tutils.NewRandReader(fileSize, true)
		tassert.CheckFatal(t, err)
		wg.Add(1)
		go func() {
			defer wg.Done()
			putRR(t, reader, bucket, dir, numobjects)
		}()
	}
	wg.Wait()
	tutils.DestroyBucket(t, proxyURL, bucket)
}

func putRR(t *testing.T, reader tutils.Reader, bucket, dir string, objCount int) []string {
	var (
		objNames = make([]string, objCount)
	)
	for i := 0; i < objCount; i++ {
		fname := tutils.GenRandomString(fnlen)
		objName := filepath.Join(dir, fname)
		putArgs := api.PutObjectArgs{
			BaseParams: tutils.DefaultBaseAPIParams(t),
			Bucket:     bucket,
			Object:     objName,
			Hash:       reader.XXHash(),
			Reader:     reader,
		}
		err := api.PutObject(putArgs)
		tassert.CheckFatal(t, err)

		objNames[i] = objName
	}

	return objNames
}
