// Package mirror provides local mirroring and replica management
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package mirror

import (
	"fmt"
	"os"

	"github.com/NVIDIA/aistore/3rdparty/glog"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/fs/mpather"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/xaction/xreg"
)

type (
	mncProvider struct {
		xreg.BaseBckEntry
		xact *xactMNC

		t      cluster.Target
		uuid   string
		copies int
	}

	// xactMNC runs in a background, traverses all local mountpaths, and makes sure
	// the bucket is N-way replicated (where N >= 1).
	xactMNC struct {
		xactBckBase
		copies int
	}
)

// interface guard
var _ cluster.Xact = (*xactMNC)(nil)

func (*mncProvider) New(args xreg.XactArgs) xreg.BucketEntry {
	return &mncProvider{t: args.T, uuid: args.UUID, copies: args.Custom.(int)}
}

func (p *mncProvider) Start(bck cmn.Bck) error {
	slab, err := p.t.MMSA().GetSlab(memsys.MaxPageSlabSize)
	cmn.AssertNoErr(err)
	p.xact = newXactMNC(bck, p.t, slab, p.uuid, p.copies)
	return nil
}
func (*mncProvider) Kind() string        { return cmn.ActMakeNCopies }
func (p *mncProvider) Get() cluster.Xact { return p.xact }

func newXactMNC(bck cmn.Bck, t cluster.Target, slab *memsys.Slab, id string, copies int) *xactMNC {
	xact := &xactMNC{
		copies: copies,
	}
	xact.xactBckBase = *newXactBckBase(id, cmn.ActMakeNCopies, bck, &mpather.JoggerGroupOpts{
		Bck:      bck,
		T:        t,
		CTs:      []string{fs.ObjectType},
		VisitObj: xact.visitObj,
		Slab:     slab,
		DoLoad:   mpather.Load, // Required to fetch `NumCopies()` and skip copies.
		Throttle: true,
	})
	return xact
}

func (r *xactMNC) Run() {
	if err := fs.ValidateNCopies(r.Target().Sname(), r.copies); err != nil {
		r.Finish(err)
		return
	}
	r.xactBckBase.runJoggers()
	glog.Infoln(r.String(), "copies=", r.copies)
	err := r.xactBckBase.waitDone()
	r.Finish(err)
}

func (r *xactMNC) visitObj(lom *cluster.LOM, buf []byte) (err error) {
	var size int64
	if n := lom.NumCopies(); n == r.copies {
		return nil
	} else if n > r.copies {
		size, err = delCopies(lom, r.copies)
	} else {
		size, err = addCopies(lom, r.copies, buf)
	}

	if os.IsNotExist(err) {
		return nil
	}
	if err != nil && cmn.IsErrOOS(err) {
		what := fmt.Sprintf("%s(%q)", r.Kind(), r.ID())
		return cmn.NewAbortedErrorDetails(what, err.Error())
	}

	r.ObjectsInc()
	r.BytesAdd(size)

	if r.ObjCount()%100 == 0 {
		if cs := fs.GetCapStatus(); cs.Err != nil {
			what := fmt.Sprintf("%s(%q)", r.Kind(), r.ID())
			return cmn.NewAbortedErrorDetails(what, cs.Err.Error())
		}
	}
	return nil
}
