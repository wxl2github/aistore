// Package ec provides erasure coding (EC) based data protection for AIStore.
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package ec

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/NVIDIA/aistore/3rdparty/atomic"
	"github.com/NVIDIA/aistore/3rdparty/glog"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/transport"
	"github.com/klauspost/reedsolomon"
)

// to avoid starving ecencode xaction, allow to run ecencode after every put batch
const putBatchSize = 8

type encodeCtx struct {
	fh            *cmn.FileHandle
	slices        []*slice
	sliceSize     int64
	fileSize      int64
	cksums        []*cmn.CksumHash
	readers       []io.Reader
	cksmReaders   []io.Reader
	wgCksmReaders *sync.WaitGroup
	errCksumCh    chan error
}

// a mountpath putJogger: processes PUT/DEL requests to one mountpath
type putJogger struct {
	parent *XactPut
	slab   *memsys.Slab
	buffer []byte
	mpath  string

	putCh  chan *Request // top priority operation (object PUT)
	xactCh chan *Request // low priority operation (ec-encode)
	stopCh chan struct{} // jogger management channel: to stop it

	toDisk bool // use files or SGL
}

func (c *putJogger) freeResources() {
	c.slab.Free(c.buffer)
	c.buffer = nil
	c.slab = nil
}

func (c *putJogger) processRequest(req *Request) {
	var memRequired int64
	lom, err := req.LIF.LOM(c.parent.t.Bowner().Get())
	defer cluster.FreeLOM(lom)
	if err != nil {
		return
	}
	if req.Action == ActSplit {
		if err = lom.Load(); err != nil {
			return
		}
		ecConf := lom.Bprops().EC
		memRequired = lom.Size() * int64(ecConf.DataSlices+ecConf.ParitySlices) / int64(ecConf.ParitySlices)
		c.toDisk = useDisk(memRequired)
	}

	c.parent.stats.updateWaitTime(time.Since(req.tm))
	req.tm = time.Now()
	err = c.ec(req, lom)
	// In case of everything is OK, a transport bundle calls `DecPending`
	// on finishing transferring all the data
	if err != nil || req.Action == ActDelete {
		c.parent.DecPending()
	}
	if req.Callback != nil {
		req.Callback(lom, err)
	}
}

func (c *putJogger) run() {
	glog.Infof("Started EC for mountpath: %s, bucket %s", c.mpath, c.parent.bck)
	c.buffer, c.slab = mm.Alloc()
	putsDone := 0

	for {
		// first, process requests with high priority
		select {
		case req := <-c.putCh:
			c.processRequest(req)
			// repeat in case of more objects in the HIGH-priority queue
			putsDone++
			if putsDone < putBatchSize {
				continue
			}
		case <-c.stopCh:
			c.freeResources()
			return
		default:
		}

		putsDone = 0
		// process all other requests
		select {
		case req := <-c.putCh:
			c.processRequest(req)
		case req := <-c.xactCh:
			c.processRequest(req)
		case <-c.stopCh:
			c.freeResources()
			return
		}
	}
}

func (c *putJogger) stop() {
	glog.Infof("Stopping EC for mountpath: %s, bucket %s", c.mpath, c.parent.bck)
	c.stopCh <- struct{}{}
	close(c.stopCh)
}

// starts EC process
func (c *putJogger) ec(req *Request, lom *cluster.LOM) error {
	var (
		err error
		act = "encoding"
	)
	switch req.Action {
	case ActSplit:
		err = c.encode(req, lom)
		c.parent.stats.updateEncodeTime(time.Since(req.tm), err != nil)
	case ActDelete:
		err = c.cleanup(lom)
		act = "cleaning up"
		c.parent.stats.updateDeleteTime(time.Since(req.tm), err != nil)
	default:
		err = fmt.Errorf("invalid EC action for putJogger: %v", req.Action)
	}

	if err != nil {
		glog.Errorf("Error %s object %s, fqn: %q, err: %v",
			act, lom, lom.FQN, err)
	}

	if req.ErrCh != nil {
		req.ErrCh <- err
		close(req.ErrCh)
	}
	if err == nil {
		c.parent.stats.updateObjTime(time.Since(req.putTime))
	}
	return err
}

// calculates and stores data and parity slices
func (c *putJogger) encode(req *Request, lom *cluster.LOM) error {
	var (
		cksumValue, cksumType string
		ecConf                = lom.Bprops().EC
	)
	if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("Encoding %q...", lom.FQN)
	}
	if lom.Cksum() != nil {
		cksumType, cksumValue = lom.Cksum().Get()
	}
	meta := &Metadata{
		Size:      lom.Size(),
		Data:      ecConf.DataSlices,
		Parity:    ecConf.ParitySlices,
		IsCopy:    req.IsCopy,
		ObjCksum:  cksumValue,
		CksumType: cksumType,
	}

	// calculate the number of targets required to encode the object
	// For replicated: ParitySlices + original object
	// For encoded: ParitySlices + DataSlices + original object
	reqTargets := ecConf.ParitySlices + 1
	if !req.IsCopy {
		reqTargets += ecConf.DataSlices
	}
	targetCnt := len(c.parent.smap.Get().Tmap)
	if targetCnt < reqTargets {
		return fmt.Errorf("object %s requires %d targets to encode, only %d found",
			lom, reqTargets, targetCnt)
	}

	// Save metadata before encoding the object
	ctMeta := cluster.NewCTFromLOM(lom, MetaType)
	metaBuf := bytes.NewReader(meta.Marshal())
	if err := ctMeta.Write(c.parent.t, metaBuf, -1); err != nil {
		return err
	}

	c.parent.ObjectsInc()
	c.parent.BytesAdd(lom.Size())

	// if an object is small just make `parity` copies
	if meta.IsCopy {
		if err := c.createCopies(lom, meta); err != nil {
			c.cleanup(lom)
			return err
		}
		return nil
	}

	// big object is erasure encoded
	if slices, err := c.sendSlices(lom, meta); err != nil {
		freeSlices(slices)
		c.cleanup(lom)
		return err
	}
	return nil
}

func (c *putJogger) ctSendCallback(hdr transport.ObjHdr, _ io.ReadCloser, _ unsafe.Pointer, err error) {
	c.parent.t.SmallMMSA().Free(hdr.Opaque)
	if err != nil {
		glog.Errorf("failed to send o[%s/%s], err: %v", hdr.Bck, hdr.ObjName, err)
	}
}

// a client has deleted the main object and requested to cleanup all its
// replicas and slices
// Just remove local metafile if it exists and broadcast the request to all
func (c *putJogger) cleanup(lom *cluster.LOM) error {
	fqnMeta, _, err := cluster.HrwFQN(lom.Bck(), MetaType, lom.ObjName)
	if err != nil {
		glog.Errorf("Failed to get path for metadata of %s: %v", lom, err)
		return nil
	}

	if err := os.RemoveAll(fqnMeta); err != nil {
		// logs the error but move on - notify all other target to do cleanup
		glog.Errorf("Error removing metafile %q", fqnMeta)
	}

	mm := c.parent.t.SmallMMSA()
	request := c.parent.newIntraReq(reqDel, nil, lom.Bck()).NewPack(mm)
	o := transport.AllocSend()
	o.Hdr = transport.ObjHdr{Bck: lom.Bucket(), ObjName: lom.ObjName, Opaque: request}
	o.Callback = c.ctSendCallback
	return c.parent.mgr.req().Send(o, nil)
}

// Sends object replicas to targets that must have replicas after the client
// uploads the main replica
func (c *putJogger) createCopies(lom *cluster.LOM, metadata *Metadata) error {
	copies := lom.Bprops().EC.ParitySlices

	// generate a list of target to send the replica (all excluding this one)
	targets, err := cluster.HrwTargetList(lom.Uname(), c.parent.smap.Get(), copies+1)
	if err != nil {
		return err
	}
	targets = targets[1:]

	// Because object encoding is called after the main replica is saved to
	// disk it needs to read it from the local storage
	fh, err := cmn.NewFileHandle(lom.FQN)
	if err != nil {
		return err
	}

	nodes := make([]string, 0, len(targets))
	for _, tgt := range targets {
		nodes = append(nodes, tgt.ID())
	}

	// broadcast the replica to the targets
	cb := func(hdr transport.ObjHdr, reader io.ReadCloser, _ unsafe.Pointer, err error) {
		if err != nil {
			glog.Errorf("Failed to to %v: %v", nodes, err)
		}
		c.parent.DecPending()
	}
	src := &dataSource{
		reader:   fh,
		size:     lom.Size(),
		metadata: metadata,
		reqType:  reqPut,
	}
	err = c.parent.writeRemote(nodes, lom, src, cb)

	return err
}

// Fills slices with calculated checksums, reports errors to errCh
func checksumDataSlices(slices []*slice, wg *sync.WaitGroup, errCh chan error, cksmReaders []io.Reader,
	cksumType string, sliceSize int64) {
	defer wg.Done()
	buf, slab := mm.Alloc(sliceSize)
	defer slab.Free(buf)
	for i, reader := range cksmReaders {
		_, cksum, err := cmn.CopyAndChecksum(ioutil.Discard, reader, buf, cksumType)
		if err != nil {
			errCh <- fmt.Errorf("failure computing checksum of a slice: %s", err)
			return
		}
		slices[i].cksum = cksum.Clone()
	}
}

// generateSlicesToMemory gets FQN to the original file and encodes it into EC slices
// * fqn - the path to original object
// * dataSlices - the number of data slices
// * paritySlices - the number of parity slices
// Returns:
// * SGL that hold all the objects data
// * constructed from the main object slices
func generateSlicesToMemory(lom *cluster.LOM, dataSlices, paritySlices int) (cmn.ReadOpenCloser, []*slice, error) {
	ctx, err := initializeSlices(lom, dataSlices, paritySlices)
	if err != nil {
		return ctx.fh, ctx.slices, err
	}

	// writers are slices created by EC encoding process(memory is allocated)
	conf := lom.CksumConf()
	initSize := cmn.MinI64(ctx.sliceSize, cmn.MiB)
	sliceWriters := make([]io.Writer, paritySlices)
	for i := 0; i < paritySlices; i++ {
		writer := mm.NewSGL(initSize)
		ctx.slices[i+dataSlices] = &slice{obj: writer}
		if conf.Type == cmn.ChecksumNone {
			sliceWriters[i] = writer
		} else {
			ctx.cksums[i] = cmn.NewCksumHash(conf.Type)
			sliceWriters[i] = cmn.NewWriterMulti(writer, ctx.cksums[i].H)
		}
	}

	err = finalizeSlices(ctx, lom, sliceWriters, dataSlices, paritySlices)
	return ctx.fh, ctx.slices, err
}

func initializeSlices(lom *cluster.LOM, dataSlices, paritySlices int) (*encodeCtx, error) {
	var (
		fqn      = lom.FQN
		totalCnt = paritySlices + dataSlices
		conf     = lom.CksumConf()
	)
	ctx := &encodeCtx{slices: make([]*slice, totalCnt)}

	stat, err := os.Stat(fqn)
	if err != nil {
		return ctx, err
	}
	ctx.fileSize = stat.Size()

	ctx.fh, err = cmn.NewFileHandle(fqn)
	if err != nil {
		return ctx, err
	}

	ctx.sliceSize = SliceSize(ctx.fileSize, dataSlices)
	padSize := ctx.sliceSize*int64(dataSlices) - ctx.fileSize

	// readers are slices of original object(no memory allocated)
	ctx.readers = make([]io.Reader, dataSlices)
	ctx.cksmReaders = make([]io.Reader, dataSlices)

	sizeLeft := ctx.fileSize
	for i := 0; i < dataSlices; i++ {
		var (
			reader     cmn.ReadOpenCloser
			cksmReader cmn.ReadOpenCloser
		)
		if sizeLeft < ctx.sliceSize {
			reader = cmn.NewSectionHandle(ctx.fh, int64(i)*ctx.sliceSize, sizeLeft, padSize)
			cksmReader = cmn.NewSectionHandle(ctx.fh, int64(i)*ctx.sliceSize, sizeLeft, padSize)
		} else {
			reader = cmn.NewSectionHandle(ctx.fh, int64(i)*ctx.sliceSize, ctx.sliceSize, 0)
			cksmReader = cmn.NewSectionHandle(ctx.fh, int64(i)*ctx.sliceSize, ctx.sliceSize, 0)
		}
		ctx.slices[i] = &slice{obj: ctx.fh, reader: reader}
		ctx.readers[i] = reader
		ctx.cksmReaders[i] = cksmReader
		sizeLeft -= ctx.sliceSize
	}

	// We have established readers of data slices, we can already start calculating hashes for them
	// during calculating parity slices and their hashes
	ctx.wgCksmReaders = &sync.WaitGroup{}
	ctx.wgCksmReaders.Add(1)
	ctx.errCksumCh = make(chan error, 1)
	if conf.Type != cmn.ChecksumNone {
		ctx.cksums = make([]*cmn.CksumHash, paritySlices)
		go checksumDataSlices(ctx.slices, ctx.wgCksmReaders, ctx.errCksumCh, ctx.cksmReaders, conf.Type, ctx.sliceSize)
	}
	return ctx, nil
}

func finalizeSlices(ctx *encodeCtx, lom *cluster.LOM, writers []io.Writer, dataSlices, paritySlices int) error {
	stream, err := reedsolomon.NewStreamC(dataSlices, paritySlices, true, true)
	if err != nil {
		return err
	}

	// Calculate parity slices and their checksums
	if err := stream.Encode(ctx.readers, writers); err != nil {
		return err
	}

	ctx.wgCksmReaders.Wait()
	conf := lom.CksumConf()
	if conf.Type != cmn.ChecksumNone {
		for i := range ctx.cksums {
			ctx.cksums[i].Finalize()
			ctx.slices[i+dataSlices].cksum = ctx.cksums[i].Clone()
		}
	}
	return nil
}

// generateSlicesToDisk gets FQN to the original file and encodes it into EC slices
// * fqn - the path to original object
// * dataSlices - the number of data slices
// * paritySlices - the number of parity slices
// Returns:
// * Main object file handle
// * constructed from the main object slices
func generateSlicesToDisk(lom *cluster.LOM, dataSlices, paritySlices int) (cmn.ReadOpenCloser, []*slice, error) {
	ctx, err := initializeSlices(lom, dataSlices, paritySlices)
	if err != nil {
		return ctx.fh, ctx.slices, err
	}

	// writers are slices created by EC encoding process(memory is allocated)
	// hashes are writers, which calculate hash when their're written to
	// sliceWriters combine writers and hashes to calculate slices and hashes at the same time
	writers := make([]io.Writer, paritySlices)
	sliceWriters := make([]io.Writer, paritySlices)

	defer func() {
		for _, wr := range writers {
			if wr == nil {
				continue
			}
			// writer can be only *os.File within this function
			f, ok := wr.(*os.File)
			cmn.Assert(ok)
			cmn.Close(f)
		}
	}()

	conf := lom.CksumConf()
	for i := 0; i < paritySlices; i++ {
		workFQN := fs.CSM.GenContentFQN(lom, fs.WorkfileType, fmt.Sprintf("ec-write-%d", i))
		writer, err := lom.CreateFile(workFQN)
		if err != nil {
			return ctx.fh, ctx.slices, err
		}
		ctx.slices[i+dataSlices] = &slice{writer: writer, workFQN: workFQN}
		writers[i] = writer
		if conf.Type == cmn.ChecksumNone {
			sliceWriters[i] = writer
		} else {
			ctx.cksums[i] = cmn.NewCksumHash(conf.Type)
			sliceWriters[i] = cmn.NewWriterMulti(writer, ctx.cksums[i].H)
		}
	}

	err = finalizeSlices(ctx, lom, sliceWriters, dataSlices, paritySlices)
	return ctx.fh, ctx.slices, err
}

// copies the constructed EC slices to remote targets
// * lom - original object
// * meta - EC metadata
// Returns:
// * list of all slices, sent to targets
func (c *putJogger) sendSlices(lom *cluster.LOM, meta *Metadata) ([]*slice, error) {
	ecConf := lom.Bprops().EC
	totalCnt := ecConf.ParitySlices + ecConf.DataSlices

	// totalCnt+1: first node gets the full object, other totalCnt nodes
	// gets a slice each
	targets, err := cluster.HrwTargetList(lom.Uname(), c.parent.smap.Get(), totalCnt+1)
	if err != nil {
		return nil, err
	}

	// load the data slices from original object and construct parity ones
	var (
		objReader cmn.ReadOpenCloser
		slices    []*slice
	)
	if c.toDisk {
		objReader, slices, err = generateSlicesToDisk(lom, ecConf.DataSlices, ecConf.ParitySlices)
	} else {
		objReader, slices, err = generateSlicesToMemory(lom, ecConf.DataSlices, ecConf.ParitySlices)
	}

	if err != nil {
		freeObject(objReader)
		freeSlices(slices)
		return nil, err
	}

	mainObj := &slice{refCnt: *atomic.NewInt32(int32(ecConf.DataSlices)), obj: objReader}
	sliceSize := SliceSize(lom.Size(), ecConf.DataSlices)
	sliceCnt := cmn.Min(totalCnt, len(targets)-1)
	counter := atomic.NewInt32(int32(sliceCnt))

	// transfer a slice to remote target
	// If the slice is data one - no immediate cleanup is required because this
	// slice is just a reader of global SGL for the entire file (that is why a
	// counter is used here)
	copySlice := func(i int) error {
		var (
			reader cmn.ReadOpenCloser
			err    error
			rc     io.ReadCloser
			data   *slice
		)

		if i < ecConf.DataSlices {
			// the slice is just a reader that does not allocate new memory
			data = mainObj
		} else {
			// the slice uses its own SGL, so the counter is 1
			data = &slice{refCnt: *atomic.NewInt32(1), obj: slices[i].obj, workFQN: slices[i].workFQN}
		}

		// In case of data slice, reopen its reader, because it was read
		// to the end by erasure encoding while calculating parity slices
		if slices[i].reader != nil {
			reader = slices[i].reader
			switch r := reader.(type) {
			case *memsys.Reader:
				_, err = r.Seek(0, io.SeekStart)
			case *cmn.SectionHandle:
				rc, err = r.Open()
				if err == nil {
					reader = rc.(cmn.ReadOpenCloser)
				}
			default:
				cmn.Assertf(false, "unsupported reader type: %v", reader)
			}
		} else {
			if sgl, ok := slices[i].obj.(*memsys.SGL); ok {
				reader = memsys.NewReader(sgl)
			} else if slices[i].workFQN != "" {
				reader, err = cmn.NewFileHandle(slices[i].workFQN)
			} else {
				cmn.Assertf(false, "unsupported reader type: %v", slices[i].obj)
			}
		}
		if err != nil {
			return fmt.Errorf("failed to reset reader: %v", err)
		}

		mcopy := &Metadata{}
		cmn.CopyStruct(mcopy, meta)
		mcopy.SliceID = i + 1
		mcopy.ObjVersion = lom.Version()
		if mcopy.SliceID != 0 && slices[i].cksum != nil {
			mcopy.CksumType, mcopy.CksumValue = slices[i].cksum.Get()
		}

		src := &dataSource{
			reader:   reader,
			size:     sliceSize,
			obj:      data,
			metadata: mcopy,
			isSlice:  true,
			reqType:  reqPut,
		}
		sentCB := func(hdr transport.ObjHdr, _ io.ReadCloser, _ unsafe.Pointer, err error) {
			if data != nil {
				data.release()
			}
			if err != nil {
				glog.Errorf("Failed to send %s/%s: %v", hdr.Bck, hdr.ObjName, err)
			}
			if cnt := counter.Dec(); cnt == 0 {
				c.parent.DecPending()
			}
		}

		// Put in lom actual object's checksum. It will be stored in slice's xattrs on dest target
		return c.parent.writeRemote([]string{targets[i+1].ID()}, lom, src, sentCB)
	}

	// Send as many as possible to be able to restore the object later.
	var copyErr error
	for i := 0; i < sliceCnt; i++ {
		if err := copySlice(i); err != nil {
			copyErr = err
		}
	}

	if copyErr != nil {
		var s string
		if ecConf.DataSlices > 1 {
			s = "s"
		}
		glog.Errorf("Error while copying %d slice%s (with parity=%d) for %q: %v",
			ecConf.DataSlices, s, ecConf.ParitySlices, lom.FQN, err)
	} else if glog.FastV(4, glog.SmoduleEC) {
		glog.Infof("EC created %d slices (with %d parity) for %q: %v",
			ecConf.DataSlices, ecConf.ParitySlices, lom.FQN, err)
	}

	return slices, nil
}
