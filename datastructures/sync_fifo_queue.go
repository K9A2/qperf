package common

import (
  "sync"
)

type SyncFifoQueue struct {
  sync.Mutex
  queue []*StreamControlBlock
}

func (sfq SyncFifoQueue) Size() int {
  defer sfq.Unlock()

  sfq.Lock()
  return len(sfq.queue)
}

func (sfq SyncFifoQueue) Empty() bool {
  defer sfq.Unlock()

  sfq.Lock()
  if sfq.Size() <= 0 {
    return true
  }
  return false
}

func (sfq SyncFifoQueue) Put(block *StreamControlBlock) {
  defer sfq.Unlock()

  sfq.Lock()
  sfq.queue = append(sfq.queue, block)
}

func (sfq SyncFifoQueue) Get() *StreamControlBlock {
  defer sfq.Unlock()

  sfq.Lock()

  if sfq.Empty() {
    return nil
  }

  object := sfq.queue[0]
  sfq.queue = sfq.queue[1:]
  return object
}
