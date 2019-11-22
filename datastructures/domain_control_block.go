package datastructures

import "sync"

type DomainQueueBlock struct {
  Mutex *sync.Mutex

  HighestPriorityQueue *SyncFifoQueue
  HighPriorityQueue    *SyncFifoQueue
  NormalPriorityQueue  *SyncFifoQueue
  LowPriorityQueue     *SyncFifoQueue
  LowestPriorityQueue  *SyncFifoQueue
}

func NewDomainQueueBlock(nRequests int) *DomainQueueBlock {
  return &DomainQueueBlock{
    Mutex:                &sync.Mutex{},
    HighestPriorityQueue: NewSyncFifoQueue(nRequests),
    HighPriorityQueue:    NewSyncFifoQueue(nRequests),
    NormalPriorityQueue:  NewSyncFifoQueue(nRequests),
    LowPriorityQueue:     NewSyncFifoQueue(nRequests),
    LowestPriorityQueue:  NewSyncFifoQueue(nRequests),
  }
}
