// Package dfc provides distributed file-based cache with Amazon and Google Cloud backends.
/*
 * Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.
 *
 */
package dfc

import (
	"container/heap"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
)

// types
type fileinfo struct {
	fqn     string
	usetime time.Time
	size    int64
	index   int
}

type lructx struct {
	cursize int64
	totsize int64
	newest  time.Time
}

type maxheap []*fileinfo

// globals
var maxheapmap = make(map[string]*maxheap)
var lructxmap = make(map[string]*lructx)

// FIXME: mountpath.enabled is never used
func (t *targetrunner) runLRU() {
	// FIXME: if LRU config has changed we need to force new LRU transaction
	xlru := t.xactinp.renewLRU(t)
	if xlru == nil {
		return
	}
	fschkwg := &sync.WaitGroup{}
	fsmap := t.mpath2Fsid()

	// init context maps to avoid insert-key races
	for mpath := range ctx.mountpaths {
		maxheapmap[mpath+"/"+ctx.config.CloudBuckets] = nil
		lructxmap[mpath+"/"+ctx.config.CloudBuckets] = nil
		maxheapmap[mpath+"/"+ctx.config.LocalBuckets] = nil
		lructxmap[mpath+"/"+ctx.config.LocalBuckets] = nil
	}

	glog.Infof("LRU: %s started: dont-evict-time %v", xlru.tostring(), ctx.config.LRUConfig.DontEvictTime)
	for _, mpath := range fsmap {
		fschkwg.Add(1)
		go t.oneLRU(mpath+"/"+ctx.config.LocalBuckets, fschkwg, xlru)
	}
	fschkwg.Wait()
	for _, mpath := range fsmap {
		fschkwg.Add(1)
		go t.oneLRU(mpath+"/"+ctx.config.CloudBuckets, fschkwg, xlru)
	}
	fschkwg.Wait()

	// final check
	rr := getstorstatsrunner()
	rr.updateCapacity()

	for mpath := range ctx.mountpaths {
		fscapacity := rr.Capacity[mpath]
		if fscapacity.Usedpct > ctx.config.LRUConfig.LowWM+1 {
			glog.Warningf("LRU mpath %s: failed to reach lwm %d%% (used %d%%)", mpath, ctx.config.LRUConfig.LowWM, fscapacity.Usedpct)
		}
	}
	xlru.etime = time.Now()
	glog.Infoln(xlru.tostring())
	t.xactinp.del(xlru.id)
}

// TODO: local-buckets-first LRU policy
func (t *targetrunner) oneLRU(mpath string, fschkwg *sync.WaitGroup, xlru *xactLRU) error {
	defer fschkwg.Done()
	h := &maxheap{}
	heap.Init(h)
	maxheapmap[mpath] = h
	toevict, err := get_toevict(mpath, ctx.config.LRUConfig.HighWM, ctx.config.LRUConfig.LowWM)
	if err != nil {
		return err
	}
	glog.Infof("LRU %s: to evict %.2f MB", mpath, float64(toevict)/1000/1000)

	// init LRU context
	lructxmap[mpath] = &lructx{totsize: toevict}
	defer func() { maxheapmap[mpath], lructxmap[mpath] = nil, nil }() // GC

	if err = filepath.Walk(mpath, xlru.lruwalkfn); err != nil {
		s := err.Error()
		if strings.Contains(s, "xaction") {
			glog.Infof("Stopping %q traversal: %s", mpath, s)
		} else {
			glog.Errorf("Failed to traverse %q, err: %v", mpath, err)
		}
		return err
	}

	if err := t.doLRU(toevict, mpath); err != nil {
		glog.Errorf("doLRU %q, err: %v", mpath, err)
		return err
	}
	return nil
}

// the walking callback is execited by the LRU xaction
// (notice the receiver)
func (xlru *xactLRU) lruwalkfn(fqn string, osfi os.FileInfo, err error) error {
	if err != nil {
		glog.Errorf("walkfunc callback invoked with err: %v", err)
		return err
	}
	// skip system files and directories
	if strings.HasPrefix(osfi.Name(), ".") || osfi.Mode().IsDir() {
		return nil
	}
	_, err = os.Stat(fqn)
	if os.IsNotExist(err) {
		glog.Infof("Warning (LRU race?): %s does not exist", fqn)
		glog.Flush()
		return nil
	}
	// abort?
	select {
	case <-xlru.abrt:
		s := fmt.Sprintf("%s aborted, exiting lruwalkfn", xlru.tostring())
		glog.Infoln(s)
		glog.Flush()
		return errors.New(s)
	case <-time.After(time.Millisecond):
		break
	}
	if xlru.finished() {
		return fmt.Errorf("%s aborted - exiting lruwalkfn", xlru.tostring())
	}

	atime, mtime, stat := get_amtimes(osfi)
	usetime := atime
	if mtime.After(atime) {
		usetime = mtime
	}
	now := time.Now()
	dontevictime := now.Add(-ctx.config.LRUConfig.DontEvictTime)
	if usetime.After(dontevictime) {
		if glog.V(3) {
			glog.Infof("DEBUG: not evicting %s (usetime %v, dontevictime %v)", fqn, usetime, dontevictime)
		}
		return nil
	}
	// remove invalid object files.
	if isinvalidobj(fqn) {
		err = osremove("lru-invalid", fqn)
		if err != nil {
			glog.Errorf("LRU: failed to delete file %s, err: %v", fqn, err)
		} else if glog.V(3) {
			glog.Infof("LRU: removed invalid file %s", fqn)
		}
		return nil
	}
	var (
		h *maxheap
		c *lructx
	)
	for mpath, hh := range maxheapmap {
		rel, err := filepath.Rel(mpath, fqn)
		if err == nil && !strings.HasPrefix(rel, "../") {
			h = hh
			c = lructxmap[mpath]
			break
		}
	}
	assert(h != nil && c != nil, fqn)
	// partial optimization:
	// 	do nothing if the heap's cursize >= totsize &&
	// 	the file is more recent then the the heap's newest
	// full optimization (tbd) entails compacting the heap when its cursize >> totsize
	if c.cursize >= c.totsize && usetime.After(c.newest) {
		if glog.V(3) {
			glog.Infof("DEBUG: use-time-after (usetime=%v, newest=%v) %s", usetime, c.newest, fqn)
		}
		return nil
	}
	// push and update the context
	fi := &fileinfo{
		fqn:     fqn,
		usetime: usetime,
		size:    stat.Size,
	}
	heap.Push(h, fi)
	c.cursize += fi.size
	if usetime.After(c.newest) {
		c.newest = usetime
	}
	return nil
}

func (t *targetrunner) doLRU(toevict int64, mpath string) error {
	h := maxheapmap[mpath]
	var (
		fevicted, bevicted int64
	)
	for h.Len() > 0 && toevict > 10 {
		fi := heap.Pop(h).(*fileinfo)
		if err := osremove("lru", fi.fqn); err != nil {
			glog.Errorf("Failed to evict %q, err: %v", fi.fqn, err)
			continue
		}
		if glog.V(3) {
			glog.Infof("LRU %s: removed %q", mpath, fi.fqn)
		}
		toevict -= fi.size
		bevicted += fi.size
		fevicted++
	}
	if ctx.rg != nil { // FIXME: for *_test only
		stats := getstorstats()
		stats.add("bytesevicted", bevicted)
		stats.add("filesevicted", fevicted)
	}
	return nil
}

//===========================================================================
//
// max-heap
//
//===========================================================================
func (mh maxheap) Len() int { return len(mh) }

func (mh maxheap) Less(i, j int) bool {
	return mh[i].usetime.Before(mh[j].usetime)
}

func (mh maxheap) Swap(i, j int) {
	mh[i], mh[j] = mh[j], mh[i]
	mh[i].index = i
	mh[j].index = j
}

func (mh *maxheap) Push(x interface{}) {
	n := len(*mh)
	fi := x.(*fileinfo)
	fi.index = n
	*mh = append(*mh, fi)
}

func (mh *maxheap) Pop() interface{} {
	old := *mh
	n := len(old)
	fi := old[n-1]
	fi.index = -1
	*mh = old[0 : n-1]
	return fi
}
