// Package tutils provides common low-level utilities for all aistore unit and integration tests
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package tutils

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/devtools/tutils/tassert"
	"github.com/NVIDIA/aistore/dsort/extract"
	"github.com/NVIDIA/aistore/ec"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/ios"
)

type (
	FileContent struct {
		Name    string
		Ext     string
		Content []byte
	}

	DirTreeDesc struct {
		InitDir string // Directory where the tree is created (can be empty).
		Dirs    int    // Number of (initially empty) directories at each depth (we recurse into single directory at each depth).
		Files   int    // Number of files at each depth.
		Depth   int    // Depth of tree/nesting.
		Empty   bool   // Determines if there is a file somewhere in the directories.
	}

	ContentTypeDesc struct {
		Type       string
		ContentCnt int
	}

	ObjectsDesc struct {
		CTs           []ContentTypeDesc // Content types which are interesting for the test.
		MountpathsCnt int               // Number of mountpaths to be created.
		ObjectSize    int64
	}

	ObjectsOut struct {
		Dir             string
		T               cluster.Target
		Bck             cmn.Bck
		FQNs            map[string][]string // ContentType => FQN
		MpathObjectsCnt map[string]int      // mpath -> # objects on the mpath
	}
)

type dummyFile struct {
	name string
	size int64
}

func newDummyFile(name string, size int64) *dummyFile {
	return &dummyFile{
		name: name,
		size: size,
	}
}

func (f *dummyFile) Name() string       { return f.name }
func (f *dummyFile) Size() int64        { return f.size }
func (f *dummyFile) Mode() os.FileMode  { return 0 }
func (f *dummyFile) ModTime() time.Time { return time.Now() }
func (f *dummyFile) IsDir() bool        { return false }
func (f *dummyFile) Sys() interface{}   { return nil }

// GetFileInfosFromTarBuffer returns all file infos contained in buffer which
// presumably is tar or gzipped tar.
func GetFileInfosFromTarBuffer(buffer bytes.Buffer, gzipped bool) ([]os.FileInfo, error) {
	var tr *tar.Reader
	if gzipped {
		gzr, err := gzip.NewReader(&buffer)
		if err != nil {
			return nil, err
		}
		tr = tar.NewReader(gzr)
	} else {
		tr = tar.NewReader(&buffer)
	}

	var files []os.FileInfo // nolint:prealloc // cannot determine the size
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break // End of archive
		}

		if err != nil {
			return nil, err
		}

		files = append(files, newDummyFile(hdr.Name, hdr.Size))
	}

	return files, nil
}

// GetFilesFromTarBuffer returns all file infos contained in buffer which
// presumably is tar or gzipped tar.
func GetFilesFromTarBuffer(buffer bytes.Buffer, extension string) ([]FileContent, error) {
	tr := tar.NewReader(&buffer)

	var files []FileContent // nolint:prealloc // cannot determine the size
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break // End of archive
		}

		if err != nil {
			return nil, err
		}

		var buf bytes.Buffer
		fExt := extract.Ext(hdr.Name)
		if extension == fExt {
			if _, err := io.CopyN(&buf, tr, hdr.Size); err != nil {
				return nil, err
			}
		}

		files = append(files, FileContent{Name: hdr.Name, Ext: fExt, Content: buf.Bytes()})
	}

	return files, nil
}

// GetFileInfosFromZipBuffer returns all file infos contained in buffer which
// presumably is zip.
func GetFileInfosFromZipBuffer(buffer bytes.Buffer) ([]os.FileInfo, error) {
	reader := bytes.NewReader(buffer.Bytes())
	zr, err := zip.NewReader(reader, int64(buffer.Len()))
	if err != nil {
		return nil, err
	}

	files := make([]os.FileInfo, len(zr.File))
	for idx, file := range zr.File {
		files[idx] = file.FileInfo()
	}

	return files, nil
}

func RandomObjDir(dirLen, maxDepth int) (dir string) {
	depth := rand.Intn(maxDepth)
	for i := 0; i < depth; i++ {
		dir = filepath.Join(dir, cmn.RandString(dirLen))
	}
	return
}

func SetXattrCksum(fqn string, bck cmn.Bck, cksum *cmn.Cksum) error {
	lom := &cluster.LOM{FQN: fqn}
	_ = lom.Init(bck)
	_ = lom.LoadMetaFromFS()
	lom.SetCksum(cksum)
	return lom.Persist()
}

func CheckPathExists(t *testing.T, path string, dir bool) {
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else {
		if dir && !fi.IsDir() {
			t.Fatalf("expected path %q to be directory", path)
		} else if !dir && fi.IsDir() {
			t.Fatalf("expected path %q to not be directory", path)
		}
	}
}

func CheckPathNotExists(t *testing.T, path string) {
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func PrepareDirTree(tb testing.TB, desc DirTreeDesc) (string, []string) {
	fileNames := make([]string, 0, 100)
	topDirName, err := ioutil.TempDir(desc.InitDir, "")
	tassert.CheckFatal(tb, err)

	nestedDirectoryName := topDirName
	for depth := 1; depth <= desc.Depth; depth++ {
		names := make([]string, 0, desc.Dirs)
		for i := 1; i <= desc.Dirs; i++ {
			name, err := ioutil.TempDir(nestedDirectoryName, "")
			tassert.CheckFatal(tb, err)
			names = append(names, name)
		}
		for i := 1; i <= desc.Files; i++ {
			f, err := ioutil.TempFile(nestedDirectoryName, "")
			tassert.CheckFatal(tb, err)
			fileNames = append(fileNames, f.Name())
			f.Close()
		}
		sort.Strings(names)
		if desc.Dirs > 0 {
			// We only recurse into last directory.
			nestedDirectoryName = names[len(names)-1]
		}
	}

	if !desc.Empty {
		f, err := ioutil.TempFile(nestedDirectoryName, "")
		tassert.CheckFatal(tb, err)
		fileNames = append(fileNames, f.Name())
		f.Close()
	}
	return topDirName, fileNames
}

func PrepareObjects(t *testing.T, desc ObjectsDesc) *ObjectsOut {
	var (
		buf       = make([]byte, desc.ObjectSize)
		fqns      = make(map[string][]string, len(desc.CTs))
		mpathCnts = make(map[string]int, desc.MountpathsCnt)

		bck = cmn.Bck{
			Name:     cmn.RandString(10),
			Provider: cmn.ProviderAIS,
			Ns:       cmn.NsGlobal,
			Props: &cmn.BucketProps{
				Cksum: cmn.CksumConf{Type: cmn.ChecksumXXHash},
				BID:   0xa5b6e7d8,
			},
		}
		bmd   = cluster.NewBaseBownerMock(cluster.NewBckEmbed(bck))
		tMock cluster.Target
	)

	mios := ios.NewIOStaterMock()
	fs.Init(mios)
	fs.DisableFsIDCheck()

	_ = fs.CSM.RegisterContentType(fs.WorkfileType, &fs.WorkfileContentResolver{})
	_ = fs.CSM.RegisterContentType(fs.ObjectType, &fs.ObjectContentResolver{})
	_ = fs.CSM.RegisterContentType(ec.SliceType, &ec.SliceSpec{})
	_ = fs.CSM.RegisterContentType(ec.MetaType, &ec.MetaSpec{})

	dir := t.TempDir()

	for i := 0; i < desc.MountpathsCnt; i++ {
		mpath, err := ioutil.TempDir(dir, "")
		tassert.CheckFatal(t, err)
		mp, err := fs.Add(mpath, "daeID")
		tassert.CheckFatal(t, err)
		mpathCnts[mp.Path] = 0
	}

	if len(desc.CTs) == 0 {
		return nil
	}

	tMock = cluster.NewTargetMock(bmd)

	errs := fs.CreateBuckets("testing", bck)
	if len(errs) > 0 {
		tassert.CheckFatal(t, errs[0])
	}

	for _, ct := range desc.CTs {
		for i := 0; i < ct.ContentCnt; i++ {
			fqn, _, err := cluster.HrwFQN(cluster.NewBckEmbed(bck), ct.Type, cmn.RandString(15))
			tassert.CheckFatal(t, err)

			fqns[ct.Type] = append(fqns[ct.Type], fqn)

			f, err := cmn.CreateFile(fqn)
			tassert.CheckFatal(t, err)
			_, _ = rand.Read(buf)
			_, err = f.Write(buf)
			f.Close()
			tassert.CheckFatal(t, err)

			parsedFQN, err := fs.ParseFQN(fqn)
			tassert.CheckFatal(t, err)
			mpathCnts[parsedFQN.MpathInfo.Path]++

			switch ct.Type {
			case fs.ObjectType:
				lom := &cluster.LOM{FQN: fqn}
				err = lom.Init(cmn.Bck{})
				tassert.CheckFatal(t, err)

				lom.SetSize(desc.ObjectSize)
				err = lom.Persist()
				tassert.CheckFatal(t, err)
			case fs.WorkfileType, ec.SliceType, ec.MetaType:
				break
			default:
				cmn.AssertMsg(false, "non-implemented type")
			}
		}
	}

	return &ObjectsOut{
		Dir:             dir,
		T:               tMock,
		Bck:             bck,
		FQNs:            fqns,
		MpathObjectsCnt: mpathCnts,
	}
}

func PrepareMountPaths(t *testing.T, cnt int) fs.MPI {
	PrepareObjects(t, ObjectsDesc{
		MountpathsCnt: cnt,
	})

	AssertMountpathCount(t, cnt, 0)
	available, _ := fs.Get()
	return available
}

func RemoveMountPaths(t *testing.T, mpaths fs.MPI) {
	for _, mpath := range mpaths {
		removedMP, err := fs.Remove(mpath.Path)
		tassert.CheckError(t, err)
		tassert.Errorf(t, removedMP != nil, "expected remove to be successful")
		tassert.CheckError(t, os.RemoveAll(mpath.Path))
	}
}

func AddMpath(t *testing.T, path string) {
	cmn.CreateDir(path) // Create directory if not exists
	_, err := fs.Add(path, "daeID")
	tassert.Errorf(t, err == nil, "Adding a mountpath %q failed, err %v", path, err)
}

func AssertMountpathCount(t *testing.T, availableCount, disabledCount int) {
	availableMountpaths, disabledMountpaths := fs.Get()
	if len(availableMountpaths) != availableCount ||
		len(disabledMountpaths) != disabledCount {
		t.Errorf(
			"wrong mountpaths: %d/%d, %d/%d",
			len(availableMountpaths), availableCount,
			len(disabledMountpaths), disabledCount,
		)
	}
}

func FilesEqual(file1, file2 string) (bool, error) {
	f1, err := ioutil.ReadFile(file1)
	if err != nil {
		return false, err
	}
	f2, err := ioutil.ReadFile(file2)
	if err != nil {
		return false, err
	}
	return bytes.Equal(f1, f2), nil
}
