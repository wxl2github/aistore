// Package ec provides erasure coding (EC) based data protection for AIStore.
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package ec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/NVIDIA/aistore/3rdparty/atomic"
	"github.com/NVIDIA/aistore/3rdparty/glog"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/transport"
	"github.com/klauspost/reedsolomon"
)

// a mountpath getJogger: processes GET requests to one mountpath
type getJogger struct {
	parent *XactGet
	client *http.Client
	mpath  string // mountpath that the jogger manages

	workCh chan *Request // channel to request TOP priority operation (restore)
	stopCh chan struct{} // jogger management channel: to stop it

	jobID  uint64
	jobs   map[uint64]bgProcess
	jobMtx sync.Mutex
	sema   chan struct{}
}

func (c *getJogger) run() {
	glog.Infof("started EC for mountpath: %s, bucket %s", c.mpath, c.parent.bck)

	for {
		select {
		case req := <-c.workCh:
			c.parent.stats.updateWaitTime(time.Since(req.tm))
			req.tm = time.Now()
			c.ec(req)
		case <-c.stopCh:
			return
		}
	}
}

func (c *getJogger) stop() {
	glog.Infof("stopping EC for mountpath: %s, bucket: %s", c.mpath, c.parent.bck)
	c.stopCh <- struct{}{}
	close(c.stopCh)
}

// starts EC process
func (c *getJogger) ec(req *Request) {
	switch req.Action {
	case ActRestore:
		c.sema <- struct{}{}
		toDisk := useDisk(0 /*size of the original object is unknown*/)
		c.jobID++
		jobID := c.jobID
		ch := req.ErrCh
		cb := func(err error) {
			c.jobMtx.Lock()
			delete(c.jobs, jobID)
			c.jobMtx.Unlock()
			if ch != nil {
				ch <- err
				close(ch)
			}
		}
		restore := func(req *Request, toDisk bool, cb func(error)) {
			lom, err := req.LIF.LOM(c.parent.t.Bowner().Get())
			defer cluster.FreeLOM(lom)
			if err == nil {
				err = lom.Load()
				if os.IsNotExist(err) {
					err = nil
				}
			}
			if err != nil {
				if cb != nil {
					cb(err)
				}
				c.parent.DecPending()
				return
			}
			err = c.restore(lom, toDisk)
			c.parent.stats.updateDecodeTime(time.Since(req.tm), err != nil)
			if cb != nil {
				cb(err)
			}
			if err == nil {
				c.parent.stats.updateObjTime(time.Since(req.putTime))
				err = lom.Persist(true)
			}
			<-c.sema
			// In case of everything is OK, a transport bundle calls `DecPending`
			// on finishing transferring all the data
			if err != nil {
				c.parent.DecPending()
			}
		}
		c.jobMtx.Lock()
		c.jobs[jobID] = restore
		c.jobMtx.Unlock()
		go restore(req, toDisk, cb)
	default:
		c.parent.DecPending()
		err := fmt.Errorf("invalid EC action for getJogger: %v", req.Action)
		glog.Errorf("Error restoring object [%s], err: %v", req.LIF.Uname, err)
		if req.ErrCh != nil {
			req.ErrCh <- err
			close(req.ErrCh)
		}
	}
}

// the final step of replica restoration process: the main target detects which
// nodes do not have replicas and copy it to them
// * bucket/objName - object path
// * reader - replica content to send to remote targets
// * metadata - object's EC metadata
// * nodes - targets that have metadata and replica - filled by requestMeta
// * replicaCnt - total number of replicas including main one
func (c *getJogger) copyMissingReplicas(lom *cluster.LOM, reader cmn.ReadOpenCloser, metadata *Metadata,
	nodes map[string]*Metadata, replicaCnt int) error {
	if err := lom.Load(); err != nil {
		return err
	}
	targets, err := cluster.HrwTargetList(lom.Uname(), c.parent.smap.Get(), replicaCnt)
	if err != nil {
		freeObject(reader)
		return err
	}

	// fill the list of daemonIDs that do not have replica
	daemons := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.ID() == c.parent.si.ID() {
			continue
		}

		if _, ok := nodes[target.ID()]; !ok {
			daemons = append(daemons, target.ID())
		}
	}

	// if any target lost its replica send the replica to it, and free allocated
	// memory on completion
	// Otherwise just free allocated memory and return immediately
	if len(daemons) == 0 {
		freeObject(reader)
		c.parent.DecPending()
		return nil
	}
	var srcReader cmn.ReadOpenCloser

	switch r := reader.(type) {
	case *memsys.SGL:
		srcReader = memsys.NewReader(r)
	case *cmn.FileHandle:
		srcReader, err = cmn.NewFileHandle(lom.FQN)
	default:
		cmn.Assertf(false, "unsupported reader type: %v", reader)
	}

	if err != nil {
		freeObject(reader)
		return err
	}

	// _ io.ReadCloser: pass copyMisssingReplicas reader argument(memsys.SGL type)
	// instead of callback's reader argument(memsys.Reader type) to freeObject
	// Reason: memsys.Reader does not provide access to internal memsys.SGL that must be freed
	cb := func(hdr transport.ObjHdr, _ io.ReadCloser, _ unsafe.Pointer, err error) {
		if err != nil {
			glog.Errorf("%s failed to send %s to %v: %v",
				c.parent.t.Snode(), lom, daemons, err)
		}
		freeObject(reader)
		c.parent.DecPending()
	}
	src := &dataSource{
		reader:   srcReader,
		size:     lom.Size(),
		metadata: metadata,
		reqType:  reqPut,
	}
	return c.parent.writeRemote(daemons, lom, src, cb)
}

// starting point of restoration of the object that was replicated
// * req - original request from a target
// * meta - rebuilt object's metadata
// * nodes - filled by requestMeta the list of targets what responsed to GET
//      metadata request with valid metafile
func (c *getJogger) restoreReplicatedFromMemory(lom *cluster.LOM, meta *Metadata, nodes map[string]*Metadata) error {
	var (
		writer *memsys.SGL
		mm     = c.parent.t.SmallMMSA()
	)
	// try read a replica from targets one by one until the replica is got
	for node := range nodes {
		uname := unique(node, lom.Bck(), lom.ObjName)
		iReqBuf := c.parent.newIntraReq(reqGet, meta, lom.Bck()).NewPack(mm)

		w := mm.NewSGL(cmn.KiB)
		if _, err := c.parent.readRemote(lom, node, uname, iReqBuf, w); err != nil {
			glog.Errorf("%s failed to read from %s", c.parent.t.Snode(), node)
			w.Free()
			mm.Free(iReqBuf)
			w = nil
			continue
		}
		mm.Free(iReqBuf)
		if w.Size() != 0 {
			// a valid replica is found - break and do not free SGL
			writer = w
			break
		}
		w.Free()
	}
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Found meta -> obj get %s, writer found: %v", lom, writer != nil)
	}

	if writer == nil {
		return errors.New("failed to read a replica from any target")
	}

	lom.SetSize(writer.Size())
	args := &WriteArgs{
		Reader:     memsys.NewReader(writer),
		MD:         cmn.MustMarshal(meta),
		CksumType:  meta.CksumType,
		CksumValue: meta.CksumValue,
	}
	if err := WriteReplicaAndMeta(c.parent.t, lom, args); err != nil {
		writer.Free()
		return err
	}

	// now a client can read the object, but EC needs to restore missing
	// replicas. So, execute copying replicas in background and return
	return c.copyMissingReplicas(lom, writer, meta, nodes, meta.Parity+1)
}

func (c *getJogger) restoreReplicatedFromDisk(lom *cluster.LOM, meta *Metadata, nodes map[string]*Metadata) error {
	var (
		writer *os.File
		n      int64
		mm     = c.parent.t.SmallMMSA()
	)
	// try read a replica from targets one by one until the replica is got
	objFQN := lom.FQN
	tmpFQN := fs.CSM.GenContentFQN(lom, fs.WorkfileType, "ec-restore-repl")

	for node := range nodes {
		uname := unique(node, lom.Bck(), lom.ObjName)

		w, err := lom.CreateFile(tmpFQN)
		if err != nil {
			glog.Errorf("Failed to create file: %v", err)
			break
		}
		iReqBuf := c.parent.newIntraReq(reqGet, meta, lom.Bck()).NewPack(mm)
		n, err = c.parent.readRemote(lom, node, uname, iReqBuf, w)
		mm.Free(iReqBuf)
		cmn.Close(w)

		if err == nil && n != 0 {
			// a valid replica is found - break and do not free SGL
			lom.SetSize(n)
			writer = w
			break
		}

		errRm := os.RemoveAll(tmpFQN)
		debug.AssertNoErr(errRm)
	}
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Found meta -> obj get %s, writer found: %v", lom, writer != nil)
	}

	if writer == nil {
		return errors.New("failed to read a replica from any target")
	}
	if err := cmn.Rename(tmpFQN, objFQN); err != nil {
		return err
	}

	if err := lom.Persist(); err != nil {
		return err
	}

	b := cmn.MustMarshal(meta)
	ctMeta := cluster.NewCTFromLOM(lom, MetaType)
	if err := ctMeta.Write(c.parent.t, bytes.NewReader(b), -1); err != nil {
		return err
	}

	// now a client can read the object, but EC needs to restore missing
	// replicas. So, execute copying replicas in background and return
	reader, err := cmn.NewFileHandle(objFQN)
	if err != nil {
		return err
	}
	return c.copyMissingReplicas(lom, reader, meta, nodes, meta.Parity+1)
}

// Main object is not found and it is clear that it was encoded. Request
// all data and parity slices from targets in a cluster:
// * req - original request
// * meta - reconstructed metadata
// * nodes - targets that responded with valid metadata, it does not make sense
//    to request slice from the entire cluster
// Returns:
// * []slice - a list of received slices in correct order (missing slices = nil)
// * map[int]string - a map of slice locations: SliceID <-> DaemonID
func (c *getJogger) requestSlices(lom *cluster.LOM, meta *Metadata, nodes map[string]*Metadata,
	toDisk bool) ([]*slice, map[int]string, error) {
	var (
		wgSlices = cmn.NewTimeoutGroup()
		sliceCnt = meta.Data + meta.Parity
		slices   = make([]*slice, sliceCnt)
		daemons  = make([]string, 0, len(nodes)) // target to be requested for a slice
		idToNode = make(map[int]string)          // which target what slice returned
	)
	for k, v := range nodes {
		if v.SliceID < 1 || v.SliceID > sliceCnt {
			glog.Warningf("Node %s has invalid slice ID %d", k, v.SliceID)
			continue
		}

		if glog.FastV(4, glog.SmoduleEC) {
			glog.Infof("Slice %s[%d] requesting from %s", lom, v.SliceID, k)
		}
		// create SGL to receive the slice data and save it to correct
		// position in the slice list
		var writer *slice
		copyLOM := lom.Clone(lom.FQN)
		if toDisk {
			prefix := fmt.Sprintf("ec-restore-%d", v.SliceID)
			fqn := fs.CSM.GenContentFQN(lom, fs.WorkfileType, prefix)
			fh, err := lom.CreateFile(fqn)
			if err != nil {
				return slices, nil, err
			}
			writer = &slice{
				writer:  fh,
				wg:      wgSlices,
				lom:     copyLOM,
				workFQN: fqn,
			}
		} else {
			writer = &slice{
				writer: mm.NewSGL(cmn.KiB * 512),
				wg:     wgSlices,
				lom:    copyLOM,
			}
		}
		slices[v.SliceID-1] = writer
		idToNode[v.SliceID] = k
		wgSlices.Add(1)
		uname := unique(k, lom.Bck(), lom.ObjName)
		if c.parent.regWriter(uname, writer) {
			daemons = append(daemons, k)
		}
	}

	iReq := c.parent.newIntraReq(reqGet, meta, lom.Bck())
	iReq.isSlice = true
	mm := c.parent.t.SmallMMSA()
	request := iReq.NewPack(mm)
	hdr := transport.ObjHdr{
		Bck:     lom.Bck().Bck,
		ObjName: lom.ObjName,
		Opaque:  request,
	}

	// broadcast slice request and wait for all targets respond
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Requesting daemons %v for slices of %s", daemons, lom)
	}
	if err := c.parent.sendByDaemonID(daemons, hdr, nil, nil, true); err != nil {
		freeSlices(slices)
		mm.Free(request)
		return nil, nil, err
	}
	conf := cmn.GCO.Get()
	if wgSlices.WaitTimeout(conf.Timeout.SendFile) {
		glog.Errorf("%s timed out waiting for %s slices", c.parent.t.Snode(), lom)
	}
	mm.Free(request)
	return slices, idToNode, nil
}

func noSliceWriter(lom *cluster.LOM, writers []io.Writer, restored []*slice, cksums []*cmn.CksumHash,
	cksumType string, idToNode map[int]string, toDisk bool, id int, sliceSize int64) error {
	if toDisk {
		prefix := fmt.Sprintf("ec-rebuild-%d", id)
		fqn := fs.CSM.GenContentFQN(lom, fs.WorkfileType, prefix)
		file, err := lom.CreateFile(fqn)
		if err != nil {
			return err
		}
		if cksumType != cmn.ChecksumNone {
			cksums[id] = cmn.NewCksumHash(cksumType)
			writers[id] = cmn.NewWriterMulti(cksums[id].H, file)
		} else {
			writers[id] = file
		}
		restored[id] = &slice{workFQN: fqn, n: sliceSize}
	} else {
		sgl := mm.NewSGL(sliceSize)
		restored[id] = &slice{obj: sgl, n: sliceSize}
		if cksumType != cmn.ChecksumNone {
			cksums[id] = cmn.NewCksumHash(cksumType)
			writers[id] = cmn.NewWriterMulti(cksums[id].H, sgl)
		} else {
			writers[id] = sgl
		}
	}

	// id from slices object differs from id of idToNode object
	delete(idToNode, id+1)

	return nil
}

func checkSliceChecksum(reader io.Reader, recvCksm *cmn.Cksum, wg *sync.WaitGroup, errCh chan int, i int,
	sliceSize int64, objName string) {
	defer wg.Done()

	cksumType := recvCksm.Type()
	if cksumType == cmn.ChecksumNone {
		return
	}

	buf, slab := mm.Alloc(sliceSize)
	_, actualCksm, err := cmn.CopyAndChecksum(ioutil.Discard, reader, buf, cksumType)
	slab.Free(buf)

	if err != nil {
		glog.Errorf("Couldn't compute checksum of a slice %d: %v", i, err)
		errCh <- i
		return
	}

	if !actualCksm.Equal(recvCksm) {
		err := cmn.NewBadDataCksumError(recvCksm, &actualCksm.Cksum, fmt.Sprintf("%s, slice %d", objName, i))
		glog.Error(err)
		errCh <- i
	}
}

// reconstruct the main object from slices, save it locally
// * req - original request
// * meta - rebuild metadata
// * slices - all slices received from targets
// * idToNode - remote location of the slices (SliceID <-> DaemonID)
// Returns:
// * list of created SGLs to be freed later
func (c *getJogger) restoreMainObj(lom *cluster.LOM, meta *Metadata, slices []*slice, idToNode map[int]string,
	toDisk bool) ([]*slice, error) {
	var (
		err       error
		sliceCnt  = meta.Data + meta.Parity
		sliceSize = SliceSize(meta.Size, meta.Data)
		readers   = make([]io.Reader, sliceCnt)
		writers   = make([]io.Writer, sliceCnt)
		restored  = make([]*slice, sliceCnt)
		cksums    = make([]*cmn.CksumHash, sliceCnt)
		conf      = lom.CksumConf()
		cksmWg    = &sync.WaitGroup{}
		cksmErrCh = make(chan int, sliceCnt)
	)

	// allocate memory for reconstructed(missing) slices - EC requirement,
	// and open existing slices for reading
	for i, sl := range slices {
		if sl != nil && sl.writer != nil {
			sz := sl.n
			if glog.FastV(4, glog.SmoduleEC) {
				glog.Infof("Got slice %d size %d (want %d) of %s", i+1, sz, sliceSize, lom)
			}
			if sz == 0 {
				freeObject(sl.obj)
				sl.obj = nil
				freeObject(sl.writer)
				sl.writer = nil
			}
		}
		if sl == nil || sl.writer == nil {
			err = noSliceWriter(lom, writers, restored, cksums, conf.Type, idToNode, toDisk, i, sliceSize)
			if err != nil {
				break
			}
		} else {
			var cksmReader io.Reader
			if sgl, ok := sl.writer.(*memsys.SGL); ok {
				readers[i] = memsys.NewReader(sgl)
				cksmReader = memsys.NewReader(sgl)
			} else if sl.workFQN != "" {
				readers[i], err = cmn.NewFileHandle(sl.workFQN)
				cksmReader, _ = cmn.NewFileHandle(sl.workFQN)
				if err != nil {
					break
				}
			} else {
				err = fmt.Errorf("unsupported slice source: %T", sl.writer)
				break
			}

			cksmWg.Add(1)
			go checkSliceChecksum(cksmReader, sl.cksum, cksmWg, cksmErrCh, i, sliceSize, lom.ObjName)
		}
	}

	if err != nil {
		return restored, err
	}

	// reconstruct the main object from slices
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Reconstructing %s", lom)
	}
	stream, err := reedsolomon.NewStreamC(meta.Data, meta.Parity, true, true)
	if err != nil {
		return restored, err
	}

	// Wait for checksum checks to complete
	cksmWg.Wait()
	close(cksmErrCh)

	for i := range cksmErrCh {
		// slice's checksum did not match, however we might be able to restore object anyway
		glog.Warningf("Slice checksum mismatch for %s", lom.ObjName)
		err := noSliceWriter(lom, writers, restored, cksums, conf.Type, idToNode, toDisk, i, sliceSize)
		if err != nil {
			return restored, err
		}
		readers[i] = nil
	}

	if err := stream.Reconstruct(readers, writers); err != nil {
		return restored, err
	}

	version := ""
	for idx, rst := range restored {
		if rst == nil {
			continue
		}
		if cksums[idx] != nil {
			cksums[idx].Finalize()
			rst.cksum = cksums[idx].Clone()
		}
		if version == "" && rst.version != "" {
			version = rst.version
		}
	}

	for _, slice := range slices {
		if slice == nil {
			continue
		}
		if version == "" && slice.version != "" {
			version = slice.version
		}
	}

	srcReaders := make([]io.Reader, meta.Data)
	for i := 0; i < meta.Data; i++ {
		if slices[i] != nil && slices[i].writer != nil {
			if sgl, ok := slices[i].writer.(*memsys.SGL); ok {
				srcReaders[i] = memsys.NewReader(sgl)
			} else if slices[i].workFQN != "" {
				srcReaders[i], err = cmn.NewFileHandle(slices[i].workFQN)
				if err != nil {
					return restored, err
				}
			} else {
				return restored, fmt.Errorf("invalid writer: %T", slices[i].writer)
			}
		} else {
			if restored[i].workFQN != "" {
				srcReaders[i], err = cmn.NewFileHandle(restored[i].workFQN)
				if err != nil {
					return restored, err
				}
			} else if sgl, ok := restored[i].obj.(*memsys.SGL); ok {
				srcReaders[i] = memsys.NewReader(sgl)
			} else {
				return restored, fmt.Errorf("empty slice %s[%d]", lom, i)
			}
		}
	}

	src := io.MultiReader(srcReaders...)
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Saving main object %s to %q", lom, lom.FQN)
	}

	if version != "" {
		lom.SetVersion(version)
	}
	lom.SetSize(meta.Size)
	mainMeta := *meta
	mainMeta.SliceID = 0
	args := &WriteArgs{
		Reader:    src,
		MD:        mainMeta.Marshal(),
		CksumType: conf.Type,
	}
	err = WriteReplicaAndMeta(c.parent.t, lom, args)
	return restored, err
}

// *slices - slices to search through
// *start - id which search should start from
// Returns:
// slice or nil if not found
// first index after found slice
func getNextNonEmptySlice(slices []*slice, start int) (*slice, int) {
	i := cmn.Max(0, start)

	for i < len(slices) && slices[i] == nil {
		i++
	}

	if i == len(slices) {
		return nil, i
	}

	return slices[i], i + 1
}

func (c *getJogger) emptyTargets(lom *cluster.LOM, meta *Metadata, idToNode map[int]string) ([]string, error) {
	sliceCnt := meta.Data + meta.Parity
	nodeToID := make(map[string]int, len(idToNode))
	// transpose SliceID <-> DaemonID map for faster lookup
	for k, v := range idToNode {
		nodeToID[v] = k
	}
	// generate the list of targets that should have a slice and find out
	// the targets without any one
	targets, err := cluster.HrwTargetList(lom.Uname(), c.parent.smap.Get(), sliceCnt+1)
	if err != nil {
		glog.Warning(err)
		return nil, err
	}
	empty := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.ID() == c.parent.si.ID() {
			continue
		}
		if _, ok := nodeToID[t.ID()]; ok {
			continue
		}
		empty = append(empty, t.ID())
	}
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Empty nodes for %s are %#v", lom, empty)
	}
	return empty, nil
}

// upload missing slices to targets (that must have them):
// * req - original request
// * meta - rebuilt object's metadata
// * slices - object slices reconstructed by `restoreMainObj`
// * idToNode - a map of targets that already contain a slice (SliceID <-> target)
func (c *getJogger) uploadRestoredSlices(lom *cluster.LOM, meta *Metadata, slices []*slice, idToNode map[int]string) error {
	emptyNodes, err := c.emptyTargets(lom, meta, idToNode)
	if err != nil {
		return err
	}

	var (
		sliceID   int
		sl        *slice
		remoteErr error
		counter   = atomic.NewInt32(0)
	)
	// First, count the number of slices and initialize the counter to avoid
	// races when network is faster than FS and transport callback comes before
	// the next slice is being sent
	for sl, id := getNextNonEmptySlice(slices, 0); sl != nil && len(emptyNodes) != 0; sl, id = getNextNonEmptySlice(slices, id) {
		counter.Inc()
	}
	if counter.Load() == 0 {
		c.parent.DecPending()
	}
	// Last, send reconstructed slices one by one to targets that are "empty".
	// Do not wait until the data transfer is complete
	for sl, sliceID = getNextNonEmptySlice(slices, 0); sl != nil && len(emptyNodes) != 0; sl, sliceID = getNextNonEmptySlice(slices, sliceID) {
		tid := emptyNodes[0]
		emptyNodes = emptyNodes[1:]

		// clone the object's metadata and set the correct SliceID before sending
		sliceMeta := meta.Clone()
		sliceMeta.SliceID = sliceID
		if sl.cksum != nil {
			sliceMeta.CksumType, sliceMeta.CksumValue = sl.cksum.Get()
		}

		var reader cmn.ReadOpenCloser
		if sl.workFQN != "" {
			reader, _ = cmn.NewFileHandle(sl.workFQN)
		} else {
			s, ok := sl.obj.(*memsys.SGL)
			cmn.Assert(ok)
			reader = memsys.NewReader(s)
		}
		dataSrc := &dataSource{
			reader:   reader,
			size:     sl.n,
			metadata: sliceMeta,
			isSlice:  true,
			reqType:  reqPut,
		}

		if glog.FastV(4, glog.SmoduleEC) {
			glog.Infof("Sending slice %s[%d] to %s", lom, sliceMeta.SliceID, tid)
		}

		// every slice's SGL must be freed upon transfer completion
		cb := func(daemonID string, s *slice) transport.ObjSentCB {
			return func(hdr transport.ObjHdr, reader io.ReadCloser, _ unsafe.Pointer, err error) {
				if err != nil {
					glog.Errorf("%s failed to send %s to %v: %v", c.parent.t.Snode(), lom, daemonID, err)
				}
				s.free()
				if cnt := counter.Dec(); cnt == 0 {
					c.parent.DecPending()
				}
			}
		}(tid, sl)
		if err := c.parent.writeRemote([]string{tid}, lom, dataSrc, cb); err != nil {
			remoteErr = err
			glog.Errorf("%s failed to send slice %s[%d] to %s", c.parent.t.Snode(), lom, sliceID, tid)
		}
	}

	for sl, sliceID = getNextNonEmptySlice(slices, sliceID); sl != nil; sl, sliceID = getNextNonEmptySlice(slices, sliceID) {
		sl.free()
	}
	return remoteErr
}

// main function that starts restoring an object that was encoded
// * req - original request
// * meta - rebuild object's metadata
// * nodes - the list of targets that responded with valid metadata
func (c *getJogger) restoreEncoded(lom *cluster.LOM, meta *Metadata, nodes map[string]*Metadata, toDisk bool) error {
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Starting EC restore %s", lom)
	}

	// download all slices from the targets that have sent metadata
	slices, idToNode, err := c.requestSlices(lom, meta, nodes, toDisk)

	freeWriters := func() {
		for _, slice := range slices {
			if slice != nil && slice.lom != nil {
				cluster.FreeLOM(slice.lom)
			}
		}
		for k := range nodes {
			uname := unique(k, lom.Bck(), lom.ObjName)
			c.parent.unregWriter(uname)
		}
	}

	if err != nil {
		freeWriters()
		return err
	}

	// restore and save locally the main replica
	restored, err := c.restoreMainObj(lom, meta, slices, idToNode, toDisk)
	if err != nil {
		glog.Errorf("%s failed to restore main object %s: %v",
			c.parent.t.Snode(), lom, err)
		freeWriters()
		freeSlices(restored)
		freeSlices(slices)
		return err
	}

	c.parent.ObjectsInc()
	c.parent.BytesAdd(lom.Size())

	// main replica is ready to download by a client.
	// Start a background process that uploads reconstructed data to
	// remote targets and then return from the function
	copyLOM := lom.Clone(lom.FQN)
	go func(lom *cluster.LOM) {
		defer cluster.FreeLOM(lom)
		c.uploadRestoredSlices(lom, meta, restored, idToNode)

		// do not free `restored` here - it is done in transport callback when
		// transport completes sending restored slices to correct target
		freeSlices(slices)
		if glog.FastV(4, glog.SmoduleEC) {
			glog.Infof("Slices %s restored successfully", lom)
		}
	}(copyLOM)

	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Main object %s restored successfully", lom)
	}
	freeWriters()
	return nil
}

// Entry point: restores main objects and slices if possible
func (c *getJogger) restore(lom *cluster.LOM, toDisk bool) error {
	if lom.Bprops() == nil || !lom.Bprops().EC.Enabled {
		return ErrorECDisabled
	}

	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Restoring %s", lom)
	}
	meta, nodes, err := c.requestMeta(lom)
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Find meta for %s: %v, err: %v", lom, meta != nil, err)
	}
	if err != nil {
		return err
	}

	if meta.IsCopy {
		if toDisk {
			return c.restoreReplicatedFromDisk(lom, meta, nodes)
		}
		return c.restoreReplicatedFromMemory(lom, meta, nodes)
	}

	if len(nodes) < meta.Data {
		return fmt.Errorf("cannot restore: too many slices missing (found %d slices, need %d or more)",
			meta.Data, len(nodes))
	}

	return c.restoreEncoded(lom, meta, nodes, toDisk)
}

// broadcast request for object's metadata. The function returns the list of
// nodes(with their EC metadata) that have the lastest object version
func (c *getJogger) requestMeta(lom *cluster.LOM) (meta *Metadata, nodes map[string]*Metadata, err error) {
	var (
		tmap   = c.parent.smap.Get().Tmap
		wg     = cmn.NewLimitedWaitGroup(cluster.MaxBcastParallel(), len(tmap))
		mtx    = &sync.Mutex{}
		metas  = make(map[string]*Metadata, len(tmap))
		chk    = make(map[string]int, len(tmap))
		chkVal string
		chkMax int
	)
	for _, node := range tmap {
		if node.ID() == c.parent.si.ID() {
			continue
		}
		wg.Add(1)
		go func(si *cluster.Snode) {
			defer wg.Done()
			md, err := requestECMeta(lom.Bucket(), lom.ObjName, si, c.client)
			if err != nil {
				if glog.FastV(4, glog.SmoduleEC) {
					glog.Infof("No EC meta %s from %s: %v", lom.ObjName, si, err)
				}
				return
			}

			mtx.Lock()
			metas[si.ID()] = md
			// detect the metadata with the latest version on the fly.
			// At this moment it is the most frequent hash in the list.
			// TODO: fix when an EC Metadata versioning is introduced
			cnt := chk[md.ObjCksum]
			cnt++
			chk[md.ObjCksum] = cnt
			if cnt > chkMax {
				chkMax = cnt
				chkVal = md.ObjCksum
			}
			mtx.Unlock()
		}(node)
	}
	wg.Wait()

	// no target has object's metadata
	if len(metas) == 0 {
		return meta, nodes, ErrorNoMetafile
	}

	// cleanup: delete all metadatas that have "obsolete" information
	nodes = make(map[string]*Metadata)
	for k, v := range metas {
		if v.ObjCksum == chkVal {
			meta = v
			nodes[k] = v
		} else {
			glog.Warningf("Hashes of target %s[slice id %d] mismatch: %s == %s",
				k, v.SliceID, chkVal, v.ObjCksum)
		}
	}

	return meta, nodes, nil
}
