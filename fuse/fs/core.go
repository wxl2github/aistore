// Package fs implements an AIStore file system.
/*
 * Copyright (c) 2019, NVIDIA CORPORATION. All rights reserved.
 */
package fs

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/fuse/ais"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseops"
	"github.com/jacobsa/fuse/fuseutil"
)

///////////////////////////////////////////////////////////////////////////////////
//
// Locking order:
//  - Lock inodes before locking the file system.
//  - When locking multiple inodes, lock them in the ascending
//    order of their IDs.
//  - Lock handles before locking inodes.
//  - When locking multiple handles, lock them in the ascending
//    order of their IDs.
//
///////////////////////////////////////////////////////////////////////////////////
//
// Explanation of some variable names:
//  - separator  -- '/' (slash)
//  - objName    -- Full name of an object in a cluster (example: "a/b/c").
//  - entryName  -- Name tied to a directory entry, i.e. file or directory name.
//                  Example: "a" (directory), "b" (directory), "c" (file).
//  - taggedName -- Files: entryName -- Directories: entryName + separator
//                  Example: "a/" (directory), "b/" (directory), "c" (file)
//  - [fs]path   -- Path from root directory to another directory or file.
//                  (i.e. parent.path + taggedName)
//                  Root path: ""
//                  Examples: "a/" (directory), "a/b/" (directory), "a/b/c" (file)
//                  NOTE: path does NOT start with a separator, and can be used
//                        as a prefix when listing objects in a bucket.
//                  NOTE: For files, [fs]path is the same as objName of the
//                        backing object.
//
///////////////////////////////////////////////////////////////////////////////////

const (
	Name = "aisfs"

	rootPath       = ""
	invalidInodeID = fuseops.InodeID(fuseops.RootInodeID + 1)

	httpTransportTimeout = 60 * time.Second  // FIXME: Too long?
	httpClientTimeout    = 300 * time.Second // FIXME: Too long?
)

var (
	glMem2 *memsys.Mem2 // Global memory manager
)

func init() {
	glMem2 = memsys.GMM()
}

// File system implementation.
type aisfs struct {
	// Embedding this struct ensures that fuseutil.FileSystem is implemented.
	// Every method implementation simply returns fuse.ENOSYS.
	// This struct overrides a subset of methods.
	// If at any time in the future all methods are implemented, this can be removed.
	fuseutil.NotImplementedFileSystem

	// Cluster
	aisURL     string
	bucketName string
	httpClient *http.Client

	// File System
	mountPath   string
	root        *DirectoryInode
	inodeTable  map[fuseops.InodeID]Inode
	lastInodeID uint64

	// Handles
	fileHandles  map[fuseops.HandleID]*fileHandle
	dirHandles   map[fuseops.HandleID]*dirHandle
	lastHandleID uint64

	// Access
	owner    *Owner
	modeBits *ModeBits

	// Logging
	errLog *log.Logger

	// Guard
	mu sync.RWMutex
}

func NewAISFileSystemServer(mountPath, aisURL, bucketName string, owner *Owner, errLog *log.Logger) fuse.Server {
	// Init HTTP client.
	httpTransport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: httpTransportTimeout,
		}).DialContext,
	}
	httpClient := &http.Client{
		Timeout:   httpClientTimeout,
		Transport: httpTransport,
	}

	// Create an aisfs instance.
	aisfs := &aisfs{
		// Cluster
		aisURL:     aisURL,
		httpClient: httpClient,
		bucketName: bucketName,

		// File System
		mountPath:  mountPath,
		inodeTable: make(map[fuseops.InodeID]Inode),

		// Handles
		fileHandles:  make(map[fuseops.HandleID]*fileHandle),
		dirHandles:   make(map[fuseops.HandleID]*dirHandle),
		lastInodeID:  uint64(invalidInodeID),
		lastHandleID: uint64(0),

		// Permissions
		owner: owner,
		modeBits: &ModeBits{
			File:      FilePermissionBits,
			Directory: DirectoryPermissionBits | os.ModeDir,
		},

		// Logging
		errLog: errLog,
	}

	// Create a bucket.
	apiParams := aisfs.aisAPIParams()
	bucket := ais.NewBucket(bucketName, apiParams)

	// Create the root inode.
	aisfs.root = NewDirectoryInode(
		fuseops.RootInodeID,
		aisfs.dirAttrs(aisfs.modeBits.Directory),
		rootPath,
		nil, /* parent */
		bucket).(*DirectoryInode)

	aisfs.root.IncLookupCount()
	aisfs.inodeTable[fuseops.RootInodeID] = aisfs.root

	return fuseutil.NewFileSystemServer(aisfs)
}

// API parameters needed to talk to the cluster
func (fs *aisfs) aisAPIParams() *api.BaseParams {
	return &api.BaseParams{
		Client: fs.httpClient,
		URL:    fs.aisURL,
	}
}

func (fs *aisfs) nextInodeID() fuseops.InodeID {
	return fuseops.InodeID(atomic.AddUint64(&fs.lastInodeID, 1))
}

func (fs *aisfs) nextHandleID() fuseops.HandleID {
	return fuseops.HandleID(atomic.AddUint64(&fs.lastHandleID, 1))
}

// Assumes that object != nil
func (fs *aisfs) fileAttrs(mode os.FileMode, object *ais.Object) fuseops.InodeAttributes {
	// Nlink will always be 1, the filesystem does not support hard links.
	return fuseops.InodeAttributes{
		Mode:  mode,
		Nlink: 1,
		Size:  object.Size,
		Uid:   fs.owner.UID,
		Gid:   fs.owner.GID,
		Atime: object.Atime,
		Mtime: object.Atime,
		Ctime: object.Atime,
	}
}

func (fs *aisfs) dirAttrs(mode os.FileMode) fuseops.InodeAttributes {
	// Size of the directory will be 0. Size greater than 0 only makes
	// sense if directory entries are persisted somewhere, which is not
	// the case here. It's similar with virtual file systems like /proc:
	// `ls -ld /proc` shows directory size to be 0.
	//
	// Nlink will always be 1, the filesystem does not support hard links.
	return fuseops.InodeAttributes{
		Mode:  mode,
		Nlink: 1,
		Size:  0,
		Uid:   fs.owner.UID,
		Gid:   fs.owner.GID,
	}
}

// REQUIRES_LOCK(fs.mu)
func (fs *aisfs) allocateDirHandle(dir *DirectoryInode) fuseops.HandleID {
	id := fs.nextHandleID()
	fs.dirHandles[id] = newDirHandle(id, dir)
	return id
}

// REQUIRES_LOCK(fs.mu), READ_LOCKS(file)
func (fs *aisfs) allocateFileHandle(file *FileInode) fuseops.HandleID {
	id := fs.nextHandleID()
	file.RLock()
	fs.fileHandles[id] = newFileHandle(id, file)
	file.RUnlock()
	return id
}

// REQUIRES_READ_LOCK(fs.mu)
func (fs *aisfs) lookupMustExist(id fuseops.InodeID) Inode {
	inode, ok := fs.inodeTable[id]
	if !ok {
		fs.fatalf("inode lookup: failed to find %d\n", id)
	}
	return inode
}

// REQUIRES_READ_LOCK(fs.mu)
func (fs *aisfs) lookupDirMustExist(id fuseops.InodeID) *DirectoryInode {
	inode := fs.lookupMustExist(id)
	dirInode, ok := inode.(*DirectoryInode)
	if !ok {
		fs.fatalf("directory inode lookup: %d not a directory\n", id)
	}
	return dirInode
}

// REQUIRES_READ_LOCK(fs.mu)
func (fs *aisfs) lookupFileMustExist(id fuseops.InodeID) *FileInode {
	inode := fs.lookupMustExist(id)
	fileInode, ok := inode.(*FileInode)
	if !ok {
		fs.fatalf("file inode lookup: %d not a file\n", id)
	}
	return fileInode
}

// REQUIRES_READ_LOCK(fs.mu)
func (fs *aisfs) lookupDhandleMustExist(id fuseops.HandleID) *dirHandle {
	handle, ok := fs.dirHandles[id]
	if !ok {
		fs.fatalf("directory handle lookup: failed to find %d\n", id)
	}
	return handle
}

// REQUIRES_READ_LOCK(fs.mu)
func (fs *aisfs) lookupFhandleMustExist(id fuseops.HandleID) *fileHandle {
	handle, ok := fs.fileHandles[id]
	if !ok {
		fs.fatalf("file handle lookup: failed to find %d\n", id)
	}
	return handle
}

// REQUIRES_LOCK(fs.mu)
func (fs *aisfs) createFileInode(parent *DirectoryInode, mode os.FileMode, object *ais.Object) Inode {
	inodeID := fs.nextInodeID()
	attrs := fs.fileAttrs(mode, object)
	inode := NewFileInode(inodeID, attrs, parent, object)
	fs.inodeTable[inodeID] = inode
	return inode
}

// REQUIRES_LOCK(fs.mu)
func (fs *aisfs) createDirectoryInode(parent *DirectoryInode, mode os.FileMode, entryName string) Inode {
	inodeID := fs.nextInodeID()
	attrs := fs.dirAttrs(mode)
	fspath := path.Join(parent.Path(), entryName) + separator
	bucket := ais.NewBucket(fs.bucketName, fs.aisAPIParams())
	inode := NewDirectoryInode(inodeID, attrs, fspath, parent, bucket)
	fs.inodeTable[inodeID] = inode
	return inode
}

////////////////////////////////
// FileSystem interface methods
////////////////////////////////

func (fs *aisfs) GetInodeAttributes(ctx context.Context, req *fuseops.GetInodeAttributesOp) (err error) {
	fs.mu.RLock()
	inode := fs.lookupMustExist(req.Inode)
	fs.mu.RUnlock()

	inode.RLock()
	req.Attributes = inode.Attributes()
	inode.RUnlock()
	return
}

func (fs *aisfs) SetInodeAttributes(ctx context.Context, req *fuseops.SetInodeAttributesOp) (err error) {
	fs.mu.RLock()
	inode := fs.lookupMustExist(req.Inode)
	fs.mu.RUnlock()

	inode.Lock()
	attrs := inode.Attributes()

	if req.Mtime != nil {
		attrs.Mtime = *req.Mtime
	}

	if req.Size != nil {
		attrs.Size = *req.Size
	}

	if req.Atime != nil {
		attrs.Atime = *req.Atime
	}

	if req.Mode != nil {
		attrs.Mode = *req.Mode
	}

	inode.SetAttributes(attrs)
	inode.Unlock()

	req.Attributes = attrs
	return
}

func (fs *aisfs) LookUpInode(ctx context.Context, req *fuseops.LookUpInodeOp) (err error) {
	var inode Inode

	fs.mu.RLock()
	parent := fs.lookupDirMustExist(req.Parent)
	fs.mu.RUnlock()

	parent.Lock()
	defer func() {
		parent.Unlock()
		if inode != nil {
			inode.IncLookupCount()
		}
	}()

	result, err := parent.LookupEntry(req.Name)
	if err != nil {
		return fs.handleIOError(err)
	}

	if result.NoEntry() {
		return fuse.ENOENT
	}

	fs.mu.Lock()
	if result.NoInode() {
		if result.IsDir() {
			inode = fs.createDirectoryInode(parent, fs.modeBits.Directory, result.Entry.Name)
		} else {
			inode = fs.createFileInode(parent, fs.modeBits.File, result.Object)
		}
	} else {
		inode = fs.lookupMustExist(result.Entry.Inode)
	}
	fs.mu.Unlock()

	parent.NewEntry(req.Name, inode.ID())

	// Locking this inode with parent already locked doesn't break
	// the valid locking order since (currently) child inodes
	// have higher ID than their respective parent inodes.
	inode.RLock()
	req.Entry = inode.AsChildEntry()
	inode.RUnlock()
	return
}

func (fs *aisfs) ForgetInode(ctx context.Context, req *fuseops.ForgetInodeOp) (err error) {
	fs.mu.RLock()
	inode := fs.lookupMustExist(req.Inode)
	fs.mu.RUnlock()

	inode.DecLookupCountN(req.N)
	// TODO: Destroy inode if lookup count dropped to 0.
	return
}