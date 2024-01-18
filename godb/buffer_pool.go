package godb

import "fmt"

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

// 目前 bug是在于，每次都是新建一个page，而不是从缓存中取出来， 明天改
type BufferPool struct {
	// pageid to page
	pages map[int]Page
}

// Create a new BufferPool with the specified number of pages
func NewBufferPool(numPages int) *BufferPool {
	var bp = BufferPool{pages: make(map[int]Page)}
	return &bp
}

// Testing method -- iterate through all pages in the buffer pool
// and flush them using [DBFile.flushPage]. Does not need to be thread/transaction safe
func (bp *BufferPool) FlushAllPages() {
	// TODO: some code goes here
}

// Abort the transaction, releasing locks. Because GoDB is FORCE/NO STEAL, none
// of the pages tid has dirtired will be on disk so it is sufficient to just
// release locks to abort. You do not need to implement this for lab 1.
func (bp *BufferPool) AbortTransaction(tid TransactionID) {
	// TODO: some code goes here
}

// Commit the transaction, releasing locks. Because GoDB is FORCE/NO STEAL, none
// of the pages tid has dirtied will be on disk, so prior to releasing locks you
// should iterate through pages and write them to disk.  In GoDB lab3 we assume
// that the system will not crash while doing this, allowing us to avoid using a
// WAL. You do not need to implement this for lab 1.
func (bp *BufferPool) CommitTransaction(tid TransactionID) {
	// TODO: some code goes here
}

func (bp *BufferPool) BeginTransaction(tid TransactionID) error {
	// TODO: some code goes here
	return nil
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
	// for lab1 temporarily ignore tid and perm and read the page from disk
	if bp.pages[pageNo] == nil {
		pg, err := file.readPage(pageNo)
		if err != nil {
			return nil, err
		}
		bp.pages[pageNo] = *pg
		return pg, nil
	}
	var p Page = bp.pages[pageNo]
	return &p, nil
}

// New a page
func (bp *BufferPool) NewPage(file DBFile, pageNo int, tid TransactionID, perm RWPerm) (*Page, error) {
	heapPage := newHeapPage(file.Descriptor(), pageNo, file.(*HeapFile))
	if heapPage == nil {
		return nil, GoDBError{TupleNotFoundError, fmt.Sprintf("page %d not found", pageNo)}
	}
	var p Page = heapPage
	bp.pages[pageNo] = p
	return &p, nil
}
