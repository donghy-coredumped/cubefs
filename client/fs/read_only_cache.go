package fs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/util/log"
)

type addressPointer struct {
	Offset int64
	Size   uint64
}

type persistentAttr struct {
	Addr addressPointer //  attr address
}

type dentryData struct {
	Type uint32
	Ino  uint64
}

type persistentDentry struct {
	DentryHead  addressPointer        // dentry address
	EntryBuffer map[string]dentryData // buffer entry until all of the dir's the entry are cached
	IsPersist   bool                  // flag used to identify whether it is persisted to the file
}

type persistentFileHandler struct {
	DataFile    *os.File
	EndPosition int64
}

type ReadOnlyMetaCache struct {
	sync.RWMutex
	AttrBinaryFile      *persistentFileHandler       // AttrBinary File's Handle
	DentryBinaryFile    *persistentFileHandler       // DentryBinary File's Handle
	Inode2PersistAttr   map[uint64]*persistentAttr   // transfer inode to persisent attr
	Inode2PersistDentry map[uint64]*persistentDentry // transfer inode to persisent dentry
}

func NewReadOnlyMetaCache(sub_dir string) (*ReadOnlyMetaCache, error) {
	meta_cache := &ReadOnlyMetaCache{
		AttrBinaryFile:      nil,
		DentryBinaryFile:    nil,
		Inode2PersistAttr:   make(map[uint64]*persistentAttr),
		Inode2PersistDentry: make(map[uint64]*persistentDentry),
	}
	attr_file_path := sub_dir + "read_only_attr_cache"
	dentry_file_path := sub_dir + "read_only_dentry_cache"
	if err := meta_cache.ParseAllPersistentAttr(attr_file_path); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][NewReadOnlyMetaCache] parse attr file fail")
		return meta_cache, err
	}
	if err := meta_cache.ParseAllPersistentDentry(dentry_file_path); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][NewReadOnlyMetaCache] parse dentry file fail")
		return meta_cache, err
	}
	return meta_cache, nil
}

// open and read the Attr file to build Inode2PersistAttr, it will also set AttrBinaryFile correct
func (persistent_meta_cache *ReadOnlyMetaCache) ParseAllPersistentAttr(attr_file_path string) error {
	var err error
	persistent_meta_cache.AttrBinaryFile.DataFile, err = os.OpenFile(attr_file_path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	// stat the attr file and set file size as EndPosisiton
	info, _ := persistent_meta_cache.AttrBinaryFile.DataFile.Stat()
	persistent_meta_cache.AttrBinaryFile.EndPosition = info.Size()

	buf := make([]byte, 16+8) // 16 bytes for address, 8 bytes for Ino
	bytes_buf := &bytes.Buffer{}
	bytes_buf.Grow(16 + 8)
	for i := int64(0); i < persistent_meta_cache.AttrBinaryFile.EndPosition; {
		address := &addressPointer{}
		persistent_meta_cache.AttrBinaryFile.DataFile.ReadAt(buf, i)
		bytes_buf.Read(buf)
		if err = binary.Read(bytes_buf, binary.BigEndian, &address.Offset); err != nil {
			log.LogErrorf("[ReadOnlyCache][ParseAllPersistentAttr] parse byte buffer into address offset fail")
		}
		if err = binary.Read(bytes_buf, binary.BigEndian, &address.Size); err != nil {
			log.LogErrorf("[ReadOnlyCache][ParseAllPersistentAttr] parse byte buffer into address size fail")
		}
		var ino uint64
		if err = binary.Read(bytes_buf, binary.BigEndian, &ino); err != nil {
			log.LogErrorf("[ReadOnlyCache][ParseAllPersistentAttr] parse byte buffer into ino fail")
		}
		// skip the real attr , just read the next address
		i = address.Offset + int64(address.Size)
		persistent_meta_cache.Inode2PersistAttr[ino] = &persistentAttr{Addr: *address}
	}
	return nil
}

// open and read the Dentry file to build Inode2PersistDentry, it will also set DentryBinaryFile correct
func (persistent_meta_cache *ReadOnlyMetaCache) ParseAllPersistentDentry(dentry_file string) error {
	var err error
	persistent_meta_cache.DentryBinaryFile.DataFile, err = os.OpenFile(dentry_file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	// stat the attr file and set file size as EndPosisiton
	info, _ := persistent_meta_cache.DentryBinaryFile.DataFile.Stat()
	persistent_meta_cache.DentryBinaryFile.EndPosition = info.Size()

	buf := make([]byte, 16+8) // 16 bytes for address, 8 bytes for Ino
	bytes_buf := &bytes.Buffer{}
	bytes_buf.Grow(16 + 8)
	for i := int64(0); i < persistent_meta_cache.DentryBinaryFile.EndPosition; {
		address := &addressPointer{}
		persistent_meta_cache.DentryBinaryFile.DataFile.ReadAt(buf, i)
		bytes_buf.Read(buf)
		if err = binary.Read(bytes_buf, binary.BigEndian, &address.Offset); err != nil {
			log.LogErrorf("[ReadOnlyCache][ParseAllPersistentAttr] parse byte buffer into address offset fail")
		}
		if err = binary.Read(bytes_buf, binary.BigEndian, &address.Size); err != nil {
			log.LogErrorf("[ReadOnlyCache][ParseAllPersistentAttr] parse byte buffer into address size fail")
		}
		var ino uint64
		if err = binary.Read(bytes_buf, binary.BigEndian, &ino); err != nil {
			log.LogErrorf("[ReadOnlyCache][ParseAllPersistentAttr] parse byte buffer into ino fail")
		}
		// skip the real attr , just read the next address
		i = address.Offset + int64(address.Size)
		persistent_meta_cache.Inode2PersistDentry[ino] = &persistentDentry{DentryHead: *address}
	}
	return nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) PutAttr(attr *proto.InodeInfo) error {
	if _, ok := persistent_meta_cache.Inode2PersistAttr[attr.Inode]; !ok {
		persistent_attr := &persistentAttr{
			Addr: addressPointer{},
		}
		err := persistent_meta_cache.WriteAttrToFile(attr, persistent_attr)
		if err != nil {
			log.LogErrorf("[ReadOnlyCache][PutAttr] : persist attr to file fail, err: %s, ino: %d", err.Error(), attr.Inode)
			return err
		}
		persistent_meta_cache.Inode2PersistAttr[attr.Inode] = persistent_attr
	}
	return nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) GetAttr(ino uint64, inode_info *proto.InodeInfo) error {
	persistent_attr, ok := persistent_meta_cache.Inode2PersistAttr[ino]
	if !ok {
		return errors.New(fmt.Sprintf("inode %d is not exist in read only cache", ino))
	}
	err := persistent_meta_cache.ReadAttrFromFile(&persistent_attr.Addr, inode_info)
	if err != nil {
		log.LogErrorf("[ReadOnlyCache][GetAttr] : get attr from file fail, err : %s, ino: %d", err.Error(), ino)
		return err
	}
	return nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) PutDentry(parentInode uint64, dentries []proto.Dentry, is_end bool) error {
	var (
		persistent_dentry *persistentDentry
		ok                bool
	)

	persistent_dentry, ok = persistent_meta_cache.Inode2PersistDentry[parentInode]
	if !ok {
		persistent_dentry = &persistentDentry{
			IsPersist: false,
		}
		persistent_meta_cache.Inode2PersistDentry[parentInode] = persistent_dentry
	}

	// add new dentry to entry buffer
	for _, dentry := range dentries {
		if _, ok := persistent_dentry.EntryBuffer[dentry.Name]; !ok {
			persistent_dentry.EntryBuffer[dentry.Name] = dentryData{
				Type: dentry.Type,
				Ino:  dentry.Inode,
			}
		}
	}

	if is_end {
		err := persistent_meta_cache.WriteDentryToFile(parentInode, persistent_dentry)
		if err != nil {
			log.LogErrorf("[ReadOnlyCache][PutDentry] : persist dentry to file fail, err: %s, ino: %d", err.Error(), parentInode)
			return err
		}
		persistent_dentry.IsPersist = true
		// clear entry buffer in memory after persisted
		for k := range persistent_dentry.EntryBuffer {
			delete(persistent_dentry.EntryBuffer, k)
		}
	}
	return nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) Lookup(ino uint64, name string) (uint64, error) {
	var (
		persistent_dentry *persistentDentry
		dentry            dentryData
		ok                bool
	)
	persistent_dentry, ok = persistent_meta_cache.Inode2PersistDentry[ino]
	if !ok {
		return 0, errors.New(fmt.Sprintf("dentry cache of inode %d is not exist in read only cache", ino))
	}

	// try to find in EntryBuffer if it has not been persisted
	if dentry, ok = persistent_dentry.EntryBuffer[name]; ok {
		return dentry.Ino, nil
	}

	var all_entries map[string]dentryData
	err := persistent_meta_cache.ReadDentryFromFile(&persistent_dentry.DentryHead, all_entries)
	if err != nil {
		log.LogErrorf("[ReadOnlyCache][Lookup] : get dentry from file fail, err : %s, ino: %d", err.Error(), ino)
		return 0, err
	}
	dentry, ok = all_entries[name]
	if !ok {
		return 0, errors.New(fmt.Sprintf("%s is not found in dentry cache of inode %d in read only cache", name, ino))
	}
	return dentry.Ino, nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) GetDentry(ino uint64, res []proto.Dentry) error {
	persistent_dentry, ok := persistent_meta_cache.Inode2PersistDentry[ino]
	// don'try to find in EntryBuffer if it has not been persisted, because it may not return complete entries in ino
	if !ok || !persistent_dentry.IsPersist {
		return errors.New(fmt.Sprintf("dentry cache of inode %d is not exist completely in read only cache", ino))
	}

	var all_entries map[string]dentryData
	err := persistent_meta_cache.ReadDentryFromFile(&persistent_dentry.DentryHead, all_entries)
	if err != nil {
		log.LogErrorf("[ReadOnlyCache][GetDentry] : get dentry from file fail, err : %s, ino: %d", err.Error(), ino)
		return err
	}
	for name, dentry := range all_entries {
		res = append(res, proto.Dentry{
			Name:  name,
			Type:  dentry.Type,
			Inode: dentry.Ino,
		})
	}
	log.LogInfo("[ReadOnlyCache][GetDentry] : num of entry in %d is %d", ino, len(res))
	return nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) ReadAttrFromFile(address *addressPointer, attr *proto.InodeInfo) error {
	buf := make([]byte, address.Size)
	_, err := persistent_meta_cache.AttrBinaryFile.DataFile.ReadAt(buf, address.Offset)
	if err != nil && err != io.EOF {
		return err
	}
	// unmarshal the data
	if err := AttrUnmarshal(buf, attr); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][ReadAttrFromFile] unmarshal Attr fail")
		return err
	}
	return nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) WriteAttrToFile(attr *proto.InodeInfo, address *persistentAttr) error {
	bytes_buf := &bytes.Buffer{}
	bs, err := AttrMarshal(attr)
	if err != nil {
		return err
	}
	address.Addr.Size = uint64(len(bs))
	address.Addr.Offset = persistent_meta_cache.AttrBinaryFile.EndPosition + 16 // 16 bytes for address
	if err := binary.Write(bytes_buf, binary.BigEndian, &address.Addr.Offset); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][WriteAttrToFile] writing offset %d to bytes buffer fail", address.Addr.Offset)
		return err
	}
	if err := binary.Write(bytes_buf, binary.BigEndian, &address.Addr.Size); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][WriteAttrToFile] writing size %d to bytes buffer fail", address.Addr.Size)
		return err
	}
	bytes_buf.Write(bs)
	var length int
	length, err = persistent_meta_cache.AttrBinaryFile.DataFile.WriteAt(bytes_buf.Bytes(), int64(persistent_meta_cache.AttrBinaryFile.EndPosition))
	if err != nil {
		log.LogErrorf("ReadOnlyMetaCache][WriteAttrToFile] writing inode %d to binary file fail", attr.Inode)
		return err
	}
	persistent_meta_cache.AttrBinaryFile.EndPosition += int64(length)
	return nil
}

func (persistent_meta_cache *ReadOnlyMetaCache) ReadDentryFromFile(address *addressPointer, entries map[string]dentryData) error {
	bytes_buf := &bytes.Buffer{}
	buf := make([]byte, address.Size)
	_, err := persistent_meta_cache.DentryBinaryFile.DataFile.ReadAt(buf, address.Offset)
	if err != nil && err != io.EOF {
		return err
	}
	_, err = bytes_buf.Read(buf)
	if err != nil {
		log.LogErrorf("ReadOnlyMetaCache][ReadDentryFromFile] bytes buffer read data from buf fail ")
		return err
	}
	var parentIno uint64
	if err = binary.Read(bytes_buf, binary.BigEndian, &parentIno); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][ReadDentryFromFile] parse bytes buffer data to parent Ino fail")
		return err
	}
	if err := DentryBatchUnMarshal(bytes_buf.Bytes(), entries); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][ReadDentryFromFile] unmarshal all entries fail")
		return err
	}

	return nil
}

// write all dentry of one directory to the DentryFile
func (persistent_meta_cache *ReadOnlyMetaCache) WriteDentryToFile(parentIno uint64, persistent_dentry *persistentDentry) error {
	bytes_buf := &bytes.Buffer{}
	bs, err := DentryBatchMarshal(persistent_dentry.EntryBuffer)
	if err != nil {
		return err
	}
	persistent_dentry.DentryHead.Size = uint64(len(bs) + 8)                                       // 8 bytes for parentIno
	persistent_dentry.DentryHead.Offset = persistent_meta_cache.DentryBinaryFile.EndPosition + 16 // 16 bytes for address
	if err := binary.Write(bytes_buf, binary.BigEndian, &persistent_dentry.DentryHead.Offset); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][WriteDentryToFile] writing offset %d to bytes buffer fail", persistent_dentry.DentryHead.Offset)
		return err
	}
	if err := binary.Write(bytes_buf, binary.BigEndian, &persistent_dentry.DentryHead.Size); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][WriteDentryToFile] writing size %d to bytes buffer fail", persistent_dentry.DentryHead.Size)
		return err
	}
	if err := binary.Write(bytes_buf, binary.BigEndian, &parentIno); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][WriteDentryToFile] writing parent ino %d to bytes buffer fail", parentIno)
		return err
	}
	bytes_buf.Write(bs)
	var length int
	length, err = persistent_meta_cache.AttrBinaryFile.DataFile.WriteAt(bytes_buf.Bytes(), int64(persistent_meta_cache.DentryBinaryFile.EndPosition))
	if err != nil {
		log.LogErrorf("ReadOnlyMetaCache][WriteDentryToFile] writing dentry of inode %d to binary file fail", parentIno)
		return err
	}
	persistent_meta_cache.DentryBinaryFile.EndPosition += int64(length)
	return nil
}

func DentryBatchMarshal(entries map[string]uint64) ([]byte, error) {
	bytes_buf := bytes.NewBuffer(make([]byte, 0))
	if err := binary.Write(bytes_buf, binary.BigEndian, uint32(len(*entries))); err != nil {
		return nil, err
	}
	for k, v := range *entries {
		bs, err := DentryMarshal(k, v)
		if err != nil {
			log.LogErrorf("ReadOnlyMetaCache][DentryBatchMarshal] marshal entry[%s, %d] fail", v.Name, v.Ino)
			return nil, err
		}
		if err = binary.Write(bytes_buf, binary.BigEndian, uint32(len(bs))); err != nil {
			log.LogErrorf("ReadOnlyMetaCache][DentryBatchMarshal] write len of entry to byte buffer fail")
			return nil, err
		}
		if _, err := bytes_buf.Write(bs); err != nil {
			return nil, err
		}
	}
	return bytes_buf.Bytes(), nil
}

func DentryBatchUnMarshal(raw []byte, entries map[string]uint64) error {
	bytes_buf := bytes.NewBuffer(raw)
	var batchLen uint32
	if err := binary.Read(bytes_buf, binary.BigEndian, &batchLen); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][DentryBatchUnMarshal] parse bytes buffer data to the count  of entries fail")
		return err
	}
	var dataLen uint32
	for i := 0; i < int(batchLen); i++ {
		if err := binary.Read(bytes_buf, binary.BigEndian, &dataLen); err != nil {
			return err
		}
		data := make([]byte, int(dataLen))
		if _, err := bytes_buf.Read(data); err != nil {
			return err
		}
		var (
			name string
			ino  uint64
		)
		if name, ino, err = DentryUnmarshal(data); err != nil {
			log.LogErrorf("ReadOnlyMetaCache][DentryBatchUnMarshal] unmarsal %d entry fail ", i)
			return err
		}
		entries[name] = ino
	}
	return nil
}

// DentryMarshal marshals the dentry into a byte array
func DentryMarshal(name string, ino uint64) ([]byte, error) {
	bytes_buf := bytes.NewBuffer(make([]byte, 0))
	if err := binary.Write(bytes_buf, binary.BigEndian, uint32(len(name))); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][DentryMarshal] write len of entry %s to byte buffer fail", d.Name)
		return nil, err
	}
	bytes_buf.Write([]byte(name))
	if err := binary.Write(bytes_buf, binary.BigEndian, &ino); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][DentryMarshal] write entry ino %d to byte buffer fail", d.Ino)
		return nil, err
	}
	return bytes_buf.Bytes(), nil
}

// DentryUnmarshal unmarshals one byte array into the dentry
func DentryUnmarshal(raw []byte) (string, uint64, error) {
	bytes_buf := bytes.NewBuffer(raw)
	var nameLen uint32
	if err := binary.Read(bytes_buf, binary.BigEndian, &nameLen); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][DentryMarshal] parse byte buffer to len of entry name fail")
		return "", 0, err
	}
	data := make([]byte, int(nameLen))
	bytes_buf.Read(data)
	name := string(data)
	var ino uint64
	if err := binary.Read(bytes_buf, binary.BigEndian, &ino); err != nil {
		log.LogErrorf("ReadOnlyMetaCache][DentryMarshal] parse byte buffer to entry ino fail")
		return "", 0, err
	}
	return name, ino, nil
}

func AttrMarshal(a *proto.InodeInfo) ([]byte, error) {
	var err error
	buff := bytes.NewBuffer(make([]byte, 0, 128))
	buff.Grow(64)
	if err = binary.Write(buff, binary.BigEndian, &a.Inode); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &a.Mode); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &a.Nlink); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &a.Size); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &a.Uid); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &a.Gid); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &a.Generation); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, a.CreateTime.Unix()); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, a.AccessTime.Unix()); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, a.ModifyTime.Unix()); err != nil {
		panic(err)
	}
	// write Target
	targetSize := uint32(len(a.Target))
	if err = binary.Write(buff, binary.BigEndian, &targetSize); err != nil {
		panic(err)
	}
	if _, err = buff.Write(a.Target); err != nil {
		panic(err)
	}
	return buff.Bytes(), nil
}

func AttrUnmarshal(raw []byte, a *proto.InodeInfo) error {
	buff := bytes.NewBuffer(raw)
	var err error
	if err = binary.Read(buff, binary.BigEndian, &a.Inode); err != nil {
		return err
	}
	if err = binary.Read(buff, binary.BigEndian, &a.Mode); err != nil {
		return err
	}
	if err = binary.Read(buff, binary.BigEndian, &a.Nlink); err != nil {
		return err
	}
	if err = binary.Read(buff, binary.BigEndian, &a.Size); err != nil {
		return err
	}
	if err = binary.Read(buff, binary.BigEndian, &a.Uid); err != nil {
		return err
	}
	if err = binary.Read(buff, binary.BigEndian, &a.Gid); err != nil {
		return err
	}
	if err = binary.Read(buff, binary.BigEndian, &a.Generation); err != nil {
		return err
	}

	var time_unix int64
	err = binary.Read(buff, binary.BigEndian, &time_unix)
	if err != nil {
		return err
	}
	a.CreateTime = time.Unix(time_unix, 0)

	err = binary.Read(buff, binary.BigEndian, &time_unix)
	if err != nil {
		return err
	}
	a.AccessTime = time.Unix(time_unix, 0)

	err = binary.Read(buff, binary.BigEndian, time_unix)
	if err != nil {
		return err
	}
	a.ModifyTime = time.Unix(time_unix, 0)

	// read Target
	targetSize := uint32(0)
	if err = binary.Read(buff, binary.BigEndian, &targetSize); err != nil {
		return err
	}
	if targetSize > 0 {
		a.Target = make([]byte, targetSize)
		if _, err = io.ReadFull(buff, a.Target); err != nil {
			log.LogErrorf("ReadOnlyMetaCache][AttrUnmarshal] read target of Inode %d fail", a.Inode)
		}
	}
	return nil
}