package godb

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

//BufferPool provides methods to cache pages that have been read from disk.
//It has a fixed capacity to limit the total amount of memory used by GoDB.
//It is also the primary way in which transactions are enforced, by using page
//level locking (you will not need to worry about this until lab3).

// Permissions used to when reading / locking pages
type RWPerm int

const (
	ReadPerm  RWPerm = iota
	WritePerm RWPerm = iota
)

// replacer interface
type Replacer interface {
	touch(pageNo int)
	evict() (int, error)
}

type FifoReplacer struct {
	data *list.List
}

func NewFifoReplacer(num int) *FifoReplacer {
	var fr = FifoReplacer{}
	fr.data = list.New()
	return &fr
}

func (fr *FifoReplacer) touch(fid int) {
	for e := fr.data.Front(); e != nil; e = e.Next() {
		if e.Value == fid {
			fr.data.Remove(e)
			break
		}
	}
	fr.data.PushBack(fid)
}

func (fr *FifoReplacer) evict() (int, error) {
	if fr.data.Len() == 0 {
		return 0, GoDBError{BufferPoolFullError, "Can't evict from replacer which is empty"}
	}
	e := fr.data.Front()
	fr.data.Remove(e)
	return e.Value.(int), nil
}

type FetchedPageType struct {
	Pid  int
	Perm RWPerm
	File DBFile
}

type BufferPool struct {
	pages []Page
	// pageid to frameid
	coord map[any]int

	pin map[any]int

	freeList list.List
	// replacer
	replacer Replacer

	// sync
	mu  sync.Mutex
	mgr *LockManager
	// transaction
	tranFetchedPid map[TransactionID]*[]FetchedPageType
}

func (bp *BufferPool) Pin(key any) {
	cnt, ok := bp.pin[key]
	if ok {
		bp.pin[key] = cnt + 1
	} else {
		bp.pin[key] = 1
	}
}

func (bp *BufferPool) Unpin(key any) {
	cnt, ok := bp.pin[key]
	if ok && cnt > 0 {
		bp.pin[key] = cnt - 1
		if cnt-1 == 0 {
			// add into replacer
			fid, ok := bp.coord[key]
			if !ok {
				panic("fid shouldn't be nil")
			}
			bp.replacer.touch(fid)
		}
	} else {
		panic("incorrect calling")
	}
}

// Create a new BufferPool with the specified number of pages
func NewBufferPool(numPages int) *BufferPool {
	var bp = BufferPool{}

	bp.pages = make([]Page, numPages)
	bp.coord = make(map[any]int)
	bp.replacer = NewFifoReplacer(numPages)
	bp.mgr = NewLockManager()
	bp.pin = make(map[any]int)

	bp.tranFetchedPid = make(map[TransactionID]*[]FetchedPageType)

	for i := 0; i < numPages; i++ {
		bp.freeList.PushBack(i)
	}

	return &bp
}

// Testing method -- iterate through all pages in the buffer pool
// and flush them using [DBFile.flushPage]. Does not need to be thread/transaction safe
func (bp *BufferPool) FlushAllPages() {
	for i := 0; i < len(bp.pages); i++ {
		if bp.pages[i] != nil && bp.pages[i].isDirty() {
			file := bp.pages[i].getFile()
			(*file).flushPage(&bp.pages[i])
			bp.pages[i].setDirty(false)
		}
	}
}

func (bp *BufferPool) RemoveFromLockMgr(tid TransactionID, p Page) {
	pid := p.(*heapPage).pageId
	key := (*p.getFile()).pageKey(pid)
	bp.mgr.ReleaseLock(tid, key)
}

func (bp *BufferPool) releasePageLock(tid TransactionID, forceWrite bool) {
	pidList, ok := bp.tranFetchedPid[tid]
	if !ok {
		return
	}
	for idx, val := range *pidList {
		file := val.File
		pid := val.Pid
		perm := val.Perm
		key := file.pageKey(pid)

		if perm == WritePerm && forceWrite {
			// fetch page first
			fid, ok := bp.coord[key]
			if !ok {
				fmt.Printf("Current idx %d", idx)
				panic("fid doesn't exist")
			}
			page := bp.pages[fid]
			err := file.flushPage(&page)
			if err != nil {
				panic("should not fail(assumed by lab document")
			}
		}
		bp.mgr.ReleaseLock(tid, key)
	}

}

// Abort the transaction, releasing locks. Because GoDB is FORCE/NO STEAL, none
// of the pages tid has dirtired will be on disk so it is sufficient to just
// release locks to abort. You do not need to implement this for lab 1.
func (bp *BufferPool) AbortTransaction(tid TransactionID) {
	bp.releasePageLock(tid, false)

	// reread dirty page
	pidList := bp.tranFetchedPid[tid]
	for _, val := range *pidList {
		pid := val.Pid
		file := val.File
		key := file.pageKey(pid)
		fid, ok := bp.coord[key]
		if !ok {
			panic("fid should exist")
		}
		page, err := file.readPage(pid)
		hf := file.(*HeapFile)
		if err != nil {
			if pid < (*hf).NumPages() {
				panic("reading page shouldn't fail")
			} else {
				// this is new page
				page = hf.AllocPage(pid)
			}
		}
		bp.pages[fid] = *page
	}

	delete(bp.tranFetchedPid, tid)
}

// Commit the transaction, releasing locks. Because GoDB is FORCE/NO STEAL, none
// of the pages tid has dirtied will be on disk, so prior to releasing locks you
// should iterate through pages and write them to disk.  In GoDB lab3 we assume
// that the system will not crash while doing this, allowing us to avoid using a
// WAL. You do not need to implement this for lab 1.
func (bp *BufferPool) CommitTransaction(tid TransactionID) {
	bp.releasePageLock(tid, true)
	delete(bp.tranFetchedPid, tid)
}

func (bp *BufferPool) BeginTransaction(tid TransactionID) error {
	list := make([]FetchedPageType, 0)
	bp.tranFetchedPid[tid] = &list
	return nil
}

func (bp *BufferPool) changeCoord(file DBFile, pageId int, frameNo int) {
	// old
	oldPage := bp.pages[frameNo]
	// defending codes
	if oldPage != nil {
		// old page id
		oldPageId := oldPage.(*heapPage).pageId
		// delete old
		delete(bp.coord, (*oldPage.getFile()).pageKey(oldPageId))
	}

	bp.coord[file.pageKey(pageId)] = frameNo
}

// This function should be called when heapFile look for the page which is selected to insert/remove a tuple
func (bp *BufferPool) releaseLockOf(tid TransactionID, pid int, file DBFile) {
	// clean up the tran
	reqList := bp.tranFetchedPid[tid]
	for idx, val := range *reqList {
		if val.Pid == pid && val.File == file {
			*reqList = append((*reqList)[:idx], (*reqList)[idx+1:]...)
			break
		}
	}

	// remove from lock
	bp.mgr.ReleaseLock(tid, file.pageKey(pid))
}

// Retrieve the specified page from the specified DBFile (e.g., a HeapFile), on
// behalf of the specified transaction. If a page is not cached in the buffer pool,
// you can read it from disk uing [DBFile.readPage]. If the buffer pool is full (i.e.,
// already stores numPages pages), a page should be evicted.  Should not evict
// pages that are dirty, as this would violate NO STEAL. If the buffer pool is
// full of dirty pages, you should return an error. For lab 1, you do not need to
// implement locking or deadlock detection. [For future labs, before returning the page,
// attempt to lock it with the specified permission. If the lock is
// unavailable, should block until the lock is free. If a deadlock occurs, abort
// one of the transactions in the deadlock]. You will likely want to store a list
// of pages in the BufferPool in a map keyed by the [DBFile.pageKey].
func (bp *BufferPool) GetPage(file DBFile, pageNo int, tid TransactionID, perm RWPerm) (*Page, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	// try fetching lock from lock manager
	key := file.pageKey(pageNo)
	fetchLockOk := false
	for !fetchLockOk {
		fetchLockOk = bp.mgr.AcquireLock(tid, key, perm)
		if fetchLockOk {
			// ok
			break
		}
		// otherwise
		// block current thread
		bp.mu.Unlock()
		time.Sleep(100) // sleep for 100 ms
		bp.mu.Lock()
	}
	bp.Pin(key)
	// get page lock successfully
	currTidPageFetchedList, ok := bp.tranFetchedPid[tid]
	if ok {
		// with transaction
		*currTidPageFetchedList = append(*currTidPageFetchedList, FetchedPageType{pageNo, perm, file})
	}

	fid, ok := bp.coord[file.pageKey(pageNo)]
	// not only pid , but also file is same
	if ok && (*bp.pages[fid].getFile()) == file {
		return &bp.pages[fid], nil
	}

	if bp.freeList.Len() > 0 {
		backElement := bp.freeList.Back()
		fid = backElement.Value.(int)
		bp.freeList.Remove(backElement)

		bp.changeCoord(file, pageNo, fid)
		bp.replacer.touch(fid)

		pg, err := file.readPage(pageNo)
		if err != nil {
			return nil, err
		}
		(*pg).(*heapPage).pageId = pageNo
		bp.pages[fid] = *pg

		return &bp.pages[fid], nil
	}

	// read
	fid, err := bp.replacer.evict()
	if err != nil {
		return nil, err
	}

	if bp.pages[fid].isDirty() {
		// flush to disk
		pgFile := *bp.pages[fid].getFile()
		err := pgFile.flushPage(&bp.pages[fid])
		if err != nil {
			return nil, err
		}
	}
	// must be the first step
	bp.changeCoord(file, pageNo, fid)

	pg, err := file.readPage(pageNo)
	if err != nil {
		return nil, err
	}
	bp.pages[fid] = *pg

	return &bp.pages[fid], nil

}

// New a page
func (bp *BufferPool) NewPage(file DBFile, pageNo int, tid TransactionID, perm RWPerm) (*Page, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	// try fetching lock from lock manager
	fetchLockOk := false
	key := file.pageKey(pageNo)
	for !fetchLockOk {
		fetchLockOk = bp.mgr.AcquireLock(tid, key, perm)
		if fetchLockOk {
			// ok
			break
		}
		// otherwise
		// block current thread
		bp.mu.Unlock()
		time.Sleep(100) // sleep for 100 ms
		bp.mu.Lock()
	}
	bp.Pin(key)
	// get page successfully
	currTidPageFetchedList := bp.tranFetchedPid[tid]
	*currTidPageFetchedList = append(*currTidPageFetchedList, FetchedPageType{pageNo, perm, file})

	fid, ok := bp.coord[file.pageKey(pageNo)]
	// not only pid , but also file is same
	if ok && (*bp.pages[fid].getFile()) == file {
		return &bp.pages[fid], nil
	}

	if bp.freeList.Len() > 0 {
		backElement := bp.freeList.Back()
		fid = backElement.Value.(int)
		bp.freeList.Remove(backElement)

		pg := newHeapPage(file.Descriptor(), pageNo, file.(*HeapFile))

		bp.pages[fid] = pg
		bp.changeCoord(file, pageNo, fid)
		return &bp.pages[fid], nil
	}

	// read
	fid, err := bp.replacer.evict()
	if err != nil {
		return nil, err
	}

	if bp.pages[fid].isDirty() {
		// flush to disk
		pgFile := *bp.pages[fid].getFile()
		err := pgFile.flushPage(&bp.pages[fid])
		if err != nil {
			return nil, err
		}
	}

	// must be first
	bp.changeCoord(file, pageNo, fid)

	pg := newHeapPage(file.Descriptor(), pageNo, file.(*HeapFile))
	if err != nil {
		return nil, err
	}
	bp.pages[fid] = pg

	return &bp.pages[fid], nil
}
