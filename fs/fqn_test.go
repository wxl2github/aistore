// Package fs_test provides tests for fs package
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package fs_test

import (
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/devtools/tutils/tassert"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/ios"
)

func TestParseFQN(t *testing.T) {
	const tmpMpath = "/tmp/ais-fqn-test"
	tests := []struct {
		testName        string
		fqn             string
		mpaths          []string
		wantMPath       string
		wantBck         cmn.Bck
		wantContentType string
		wantObjName     string
		wantErr         bool
	}{
		// good
		{
			"smoke test",
			tmpMpath + "/@ais/#namespace/bucket/%ob/objname",
			[]string{tmpMpath},
			tmpMpath,
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAIS, Ns: cmn.Ns{Name: "namespace"}},
			fs.ObjectType, "objname", false,
		},
		{
			"smoke test (namespace global)",
			tmpMpath + "/@ais/bucket/%ob/objname",
			[]string{tmpMpath},
			tmpMpath,
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAIS, Ns: cmn.NsGlobal},
			fs.ObjectType, "objname", false,
		},
		{
			"content type (work)",
			tmpMpath + "/@aws/bucket/%wk/objname",
			[]string{tmpMpath},
			tmpMpath,
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAmazon, Ns: cmn.NsGlobal},
			fs.WorkfileType, "objname", false,
		},
		{
			"cloud as bucket type (aws)",
			tmpMpath + "/@aws/bucket/%ob/objname",
			[]string{tmpMpath},
			tmpMpath,
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAmazon, Ns: cmn.NsGlobal},
			fs.ObjectType, "objname", false,
		},
		{
			"cloud as bucket type (gcp)",
			tmpMpath + "/@gcp/bucket/%ob/objname",
			[]string{tmpMpath},
			tmpMpath,
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderGoogle, Ns: cmn.NsGlobal},
			fs.ObjectType, "objname", false,
		},
		{
			"non-empty namespace",
			tmpMpath + "/@ais/#namespace/bucket/%ob/objname",
			[]string{tmpMpath},
			tmpMpath,
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAIS, Ns: cmn.Ns{Name: "namespace"}},
			fs.ObjectType, "objname", false,
		},
		{
			"cloud namespace",
			tmpMpath + "/@ais/@uuid#namespace/bucket/%ob/objname",
			[]string{tmpMpath},
			tmpMpath,
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAIS, Ns: cmn.Ns{UUID: "uuid", Name: "namespace"}},
			fs.ObjectType, "objname", false,
		},
		{
			"long mount path name",
			tmpMpath + "/super/long/@aws/bucket/%ob/objname",
			[]string{tmpMpath + "/super/long"},
			tmpMpath + "/super/long",
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAmazon, Ns: cmn.NsGlobal},
			fs.ObjectType, "objname", false,
		},
		{
			"long mount path name and objname in folder",
			tmpMpath + "/super/long/@aws/bucket/%ob/folder/objname",
			[]string{tmpMpath + "/super/long"},
			tmpMpath + "/super/long",
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAmazon, Ns: cmn.NsGlobal},
			fs.ObjectType, "folder/objname", false,
		},
		{
			"multiple mpaths matching, choose the longest",
			tmpMpath + "/super/long/long/@aws/bucket/%ob/folder/objname",
			[]string{tmpMpath + "/super/long", tmpMpath + "/super/long/long"},
			tmpMpath + "/super/long/long",
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderAmazon, Ns: cmn.NsGlobal},
			fs.ObjectType, "folder/objname", false,
		},
		{
			"dirty mpath",
			tmpMpath + "/super/long/long/@gcp/bucket/%ob/folder/objname",
			[]string{tmpMpath + "/super/long", tmpMpath + "/.////super/../super//./long///////////long"},
			tmpMpath + "/super/long/long",
			cmn.Bck{Name: "bucket", Provider: cmn.ProviderGoogle, Ns: cmn.NsGlobal},
			fs.ObjectType, "folder/objname", false,
		},

		// bad
		{
			"too short name",
			tmpMpath + "/bucket/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"invalid content type (not prefixed with '%')",
			tmpMpath + "/@gcp/bucket/ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"invalid content type (empty)",
			tmpMpath + "/@ais/bucket/name",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"invalid content type (unknown)",
			tmpMpath + "/@gcp/bucket/%un/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"empty bucket name",
			tmpMpath + "/@ais//%ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"empty bucket name (without slash)",
			tmpMpath + "/@ais/%ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"empty object name",
			tmpMpath + "/@ais/bucket/%ob/",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"empty backend provider",
			tmpMpath + "/bucket/%ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"invalid backend provider (not prefixed with '@')",
			tmpMpath + "/gcp/bucket/%ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"invalid backend provider (unknown)",
			tmpMpath + "/@unknown/bucket/%ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"invalid backend provider (cloud)",
			tmpMpath + "/@cloud/bucket/%ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"invalid cloud namespace",
			tmpMpath + "/@cloud/@uuid/bucket/%ob/objname",
			[]string{tmpMpath},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"no matching mountpath",
			tmpMpath + "/@ais/bucket/%obj/objname",
			[]string{tmpMpath + "/a", tmpMpath + "/b"},
			"",
			cmn.Bck{},
			"", "", true,
		},
		{
			"fqn is mpath",
			tmpMpath + "/mpath",
			[]string{tmpMpath + "/mpath"},
			"",
			cmn.Bck{},
			"", "", true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			mios := ios.NewIOStaterMock()
			fs.Init(mios)
			fs.DisableFsIDCheck()

			for _, mpath := range tt.mpaths {
				if _, err := os.Stat(mpath); os.IsNotExist(err) {
					cmn.CreateDir(mpath)
					defer os.RemoveAll(mpath)
				}
				_, err := fs.Add(mpath, "daeID")
				tassert.CheckFatal(t, err)
			}
			fs.CSM.RegisterContentType(fs.ObjectType, &fs.ObjectContentResolver{})
			fs.CSM.RegisterContentType(fs.WorkfileType, &fs.WorkfileContentResolver{})

			parsedFQN, err := fs.ParseFQN(tt.fqn)
			if (err != nil) != tt.wantErr {
				t.Errorf("fqn2info() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			gotMpath, gotBck, gotContentType, gotObjName :=
				parsedFQN.MpathInfo.Path, parsedFQN.Bck, parsedFQN.ContentType, parsedFQN.ObjName
			if gotMpath != tt.wantMPath {
				t.Errorf("gotMpath = %v, want %v", gotMpath, tt.wantMPath)
			}
			if !gotBck.Equal(tt.wantBck) {
				t.Errorf("gotBck = %v, want %v", gotBck, tt.wantBck)
			}
			if gotContentType != tt.wantContentType {
				t.Errorf("gotContentType = %v, want %v", gotContentType, tt.wantContentType)
			}
			if gotObjName != tt.wantObjName {
				t.Errorf("gotObjName = %v, want %v", gotObjName, tt.wantObjName)
			}
		})
	}
}

func TestMakeAndParseFQN(t *testing.T) {
	tests := []struct {
		mpath       string
		bck         cmn.Bck
		contentType string
		objName     string
	}{
		{
			mpath: "/tmp/path",
			bck: cmn.Bck{
				Name:     "bucket",
				Provider: cmn.ProviderAIS,
				Ns:       cmn.NsGlobal,
			},
			contentType: fs.ObjectType,
			objName:     "object/name",
		},
		{
			mpath: "/tmp/path",
			bck: cmn.Bck{
				Name:     "bucket",
				Provider: cmn.ProviderAmazon,
				Ns:       cmn.Ns{UUID: "uuid", Name: "namespace"},
			},
			contentType: fs.WorkfileType,
			objName:     "object/name",
		},
		{
			mpath: "/tmp/path",
			bck: cmn.Bck{
				Name:     "bucket",
				Provider: cmn.ProviderAmazon,
				Ns:       cmn.Ns{Name: "alias"},
			},
			contentType: fs.ObjectType,
			objName:     "object/name",
		},
		{
			mpath: "/tmp/path",
			bck: cmn.Bck{
				Name:     "bucket",
				Provider: cmn.ProviderGoogle,
				Ns:       cmn.NsGlobal,
			},
			contentType: fs.ObjectType,
			objName:     "object/name",
		},
	}

	for _, tt := range tests {
		t.Run(strings.Join([]string{tt.mpath, tt.bck.String(), tt.contentType, tt.objName}, "|"), func(t *testing.T) {
			mios := ios.NewIOStaterMock()
			fs.Init(mios)
			fs.DisableFsIDCheck()

			if _, err := os.Stat(tt.mpath); os.IsNotExist(err) {
				cmn.CreateDir(tt.mpath)
				defer os.RemoveAll(tt.mpath)
			}
			_, err := fs.Add(tt.mpath, "daeID")
			tassert.CheckFatal(t, err)

			fs.CSM.RegisterContentType(fs.ObjectType, &fs.ObjectContentResolver{})
			fs.CSM.RegisterContentType(fs.WorkfileType, &fs.WorkfileContentResolver{})

			mpaths, _ := fs.Get()
			fqn := mpaths[tt.mpath].MakePathFQN(tt.bck, tt.contentType, tt.objName)

			parsedFQN, err := fs.ParseFQN(fqn)
			if err != nil {
				t.Fatalf("failed to parse FQN: %v", err)
			}
			gotMpath, gotBck, gotContentType, gotObjName :=
				parsedFQN.MpathInfo.Path, parsedFQN.Bck, parsedFQN.ContentType, parsedFQN.ObjName
			if gotMpath != tt.mpath {
				t.Errorf("gotMpath = %v, want %v", gotMpath, tt.mpath)
			}
			if gotBck != tt.bck {
				t.Errorf("gotBck = %v, want %v", gotBck, tt.bck)
			}
			if gotContentType != tt.contentType {
				t.Errorf("getContentType = %v, want %v", gotContentType, tt.contentType)
			}
			if gotObjName != tt.objName {
				t.Errorf("gotObjName = %v, want %v", gotObjName, tt.objName)
			}
		})
	}
}

var parsedFQN fs.ParsedFQN

func BenchmarkParseFQN(b *testing.B) {
	var (
		mpath = "/tmp/mpath"
		mios  = ios.NewIOStaterMock()
		bck   = cmn.Bck{Name: "bucket", Provider: cmn.ProviderAIS, Ns: cmn.NsGlobal}
	)

	fs.Init(mios)
	fs.DisableFsIDCheck()
	cmn.CreateDir(mpath)
	defer os.RemoveAll(mpath)
	fs.Add(mpath, "daeID")
	fs.CSM.RegisterContentType(fs.ObjectType, &fs.ObjectContentResolver{})

	mpaths, _ := fs.Get()
	fqn := mpaths[mpath].MakePathFQN(bck, fs.ObjectType, "super/long/name")
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		parsedFQN, _ = fs.ParseFQN(fqn)
	}
}
