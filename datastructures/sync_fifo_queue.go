package datastructures

import (
  "github.com/google/logger"
  "sync"
)

type SyncFifoQueue struct {
  mutex *sync.Mutex
  queue []*StreamControlBlock
}

func NewSyncFifoQueue(capacity int) *SyncFifoQueue {
  return &SyncFifoQueue{
    mutex: &sync.Mutex{},
    queue: make([]*StreamControlBlock, 0, capacity),
  }
}

func (sfq *SyncFifoQueue) Size() int {
  return len(sfq.queue)
}

func (sfq *SyncFifoQueue) Empty() bool {
  result := sfq.Size() <= 0
  logger.Infof("empty: result <%d>", result)
  return result
  //if sfq.Size() <= 0 {
  //  return true
  //}
  //return false
}

func (sfq *SyncFifoQueue) Put(block *StreamControlBlock) {
  defer sfq.mutex.Unlock()

  sfq.mutex.Lock()
  sfq.queue = append(sfq.queue, block)
}

func (sfq *SyncFifoQueue) Get() *StreamControlBlock {
  defer sfq.mutex.Unlock()
  sfq.mutex.Lock()

  if sfq.Empty() {
    return nil
  }
  object := sfq.queue[0]
  sfq.queue = sfq.queue[1:]
  return object
}
