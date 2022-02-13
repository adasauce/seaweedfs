package mount

import (
	"context"
	"fmt"
	"github.com/chrislusf/seaweedfs/weed/filer"
	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/pb/filer_pb"
	"github.com/hanwen/go-fuse/v2/fuse"
	"time"
)

func (wfs *WFS) Mknod(cancel <-chan struct{}, in *fuse.MknodIn, name string, out *fuse.EntryOut) (code fuse.Status) {

	if s := checkName(name); s != fuse.OK {
		return s
	}

	newEntry := &filer_pb.Entry{
		Name:        name,
		IsDirectory: false,
		Attributes: &filer_pb.FuseAttributes{
			Mtime:       time.Now().Unix(),
			Crtime:      time.Now().Unix(),
			FileMode:    uint32(toFileMode(in.Mode) &^ wfs.option.Umask),
			Uid:         in.Uid,
			Gid:         in.Gid,
			Collection:  wfs.option.Collection,
			Replication: wfs.option.Replication,
			TtlSec:      wfs.option.TtlSec,
		},
	}

	dirFullPath := wfs.inodeToPath.GetPath(in.NodeId)

	entryFullPath := dirFullPath.Child(name)

	err := wfs.WithFilerClient(false, func(client filer_pb.SeaweedFilerClient) error {

		wfs.mapPbIdFromLocalToFiler(newEntry)
		defer wfs.mapPbIdFromFilerToLocal(newEntry)

		request := &filer_pb.CreateEntryRequest{
			Directory:  string(dirFullPath),
			Entry:      newEntry,
			Signatures: []int32{wfs.signature},
		}

		glog.V(1).Infof("mknod: %v", request)
		if err := filer_pb.CreateEntry(client, request); err != nil {
			glog.V(0).Infof("mknod %s: %v", entryFullPath, err)
			return err
		}

		if err := wfs.metaCache.InsertEntry(context.Background(), filer.FromPbEntry(request.Directory, request.Entry)); err != nil {
			return fmt.Errorf("local mknod %s: %v", entryFullPath, err)
		}

		return nil
	})

	glog.V(0).Infof("mknod %s: %v", entryFullPath, err)

	if err != nil {
		return fuse.EIO
	}

	inode := wfs.inodeToPath.GetInode(entryFullPath)

	wfs.outputPbEntry(out, inode, newEntry)

	return fuse.OK

}

func (wfs *WFS) Unlink(cancel <-chan struct{}, header *fuse.InHeader, name string) (code fuse.Status) {

	dirFullPath := wfs.inodeToPath.GetPath(header.NodeId)
	entryFullPath := dirFullPath.Child(name)

	entry, status := wfs.maybeLoadEntry(entryFullPath)
	if status != fuse.OK {
		return status
	}

	// first, ensure the filer store can correctly delete
	glog.V(3).Infof("remove file: %v", entryFullPath)
	isDeleteData := entry != nil && entry.HardLinkCounter <= 1
	err := filer_pb.Remove(wfs, string(dirFullPath), name, isDeleteData, false, false, false, []int32{wfs.signature})
	if err != nil {
		glog.V(0).Infof("remove %s: %v", entryFullPath, err)
		return fuse.ENOENT
	}

	// then, delete meta cache
	if err = wfs.metaCache.DeleteEntry(context.Background(), entryFullPath); err != nil {
		glog.V(3).Infof("local DeleteEntry %s: %v", entryFullPath, err)
		return fuse.EIO
	}

	wfs.metaCache.DeleteEntry(context.Background(), entryFullPath)
	wfs.inodeToPath.RemovePath(entryFullPath)

	// TODO handle open files, hardlink

	return fuse.OK

}