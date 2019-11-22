package schedulers

import (
  "github.com/google/logger"
  . "github.com/stormlin/qperf/constants"
  . "github.com/stormlin/qperf/datastructures"
  "sync"
  "time"
)

// chrome dynamic fcfs scheduler，以非抢占方式实现 dynamic fcfs 调度算法
// scheduler 组件只负责向用户返回下一个它认为应当发送的请求，由用户拿到该请求的 control
// block 之后自行发起请求
type DynamicFcfsScheduler struct {
  sync.Mutex

  FirstImageResourceId uint16

  // 指向全局数据的链接
  ControlBlockSlice *StreamControlBlockSlice
  //
  domainRequestMap *map[string]*DomainQueueBlock
}

func NewDynamicFcfsScheduler(firstImageResourceId uint16, nRequests int,
  controlBlockSlice *StreamControlBlockSlice) *DynamicFcfsScheduler {
  return &DynamicFcfsScheduler{
    Mutex:                sync.Mutex{},
    FirstImageResourceId: firstImageResourceId,
    ControlBlockSlice:    controlBlockSlice,
    HighestPriorityQueue: NewSyncFifoQueue(nRequests),
    HighPriorityQueue:    NewSyncFifoQueue(nRequests),
    NormalPriorityQueue:  NewSyncFifoQueue(nRequests),
    LowPriorityQueue:     NewSyncFifoQueue(nRequests),
    LowestPriorityQueue:  NewSyncFifoQueue(nRequests),
  }
}

// 确认该 script 是否为第一张图片之后的 JS 文件
func scriptAfterFirstImage(resourceId uint16, firstImageResourceId uint16) bool {
  return resourceId < firstImageResourceId
}

// 根据请求类型将其加入合适的队列
func (scheduler *DynamicFcfsScheduler) enqueue(block *StreamControlBlock) {
  switch block.ResourceType {
  case DOCUMENT:
    scheduler.HighestPriorityQueue.Put(block)
    break
  case STYLESHEET:
    scheduler.HighestPriorityQueue.Put(block)
    break
  case XHR:
    scheduler.HighPriorityQueue.Put(block)
    break
  case SCRIPT:
    if scriptAfterFirstImage(
      block.ResourceId, scheduler.FirstImageResourceId) {
      scheduler.NormalPriorityQueue.Put(block)
      break
    }
    scheduler.HighPriorityQueue.Put(block)
    break
  case IMAGE:
    scheduler.LowPriorityQueue.Put(block)
    break
  case OTHER:
    scheduler.LowPriorityQueue.Put(block)
    break
  default:
    logger.Warningf("unrecognized resource type: <%s>, enqueued at "+
      "lowest priority queue", block.ResourceType)
    scheduler.LowestPriorityQueue.Put(block)
    break
  }
}

// 检查该请求的依赖项是否都已被满足
func dependenciesMet(block *StreamControlBlock, slice *[]*StreamControlBlock) bool {
  for _, dep := range block.Dependencies {
    if (*slice)[dep].Status != FINISHED {
      return false
    }
  }
  return true
}

// scheduler 给出所有已就位的请求，由 client 自行发起请求
func (scheduler *DynamicFcfsScheduler) PopAvailableRequests(
  slice *StreamControlBlockSlice) *StreamControlBlock {
  defer scheduler.Mutex.Unlock()
  scheduler.Mutex.Lock()

  // 按照优先级发送队列中的请求
  requestToSent := scheduler.HighestPriorityQueue.Get()
  if requestToSent != nil {
    goto selected
  }
  requestToSent = scheduler.HighPriorityQueue.Get()
  if requestToSent != nil {
    goto selected
  }
  requestToSent = scheduler.NormalPriorityQueue.Get()
  if requestToSent != nil {
    goto selected
  }
  requestToSent = scheduler.LowPriorityQueue.Get()
  if requestToSent != nil {
    goto selected
  }
  requestToSent = scheduler.LowestPriorityQueue.Get()
  if requestToSent != nil {
    goto selected
  }
  // 没有可供发送的请求
  return nil

selected:
  nextRequest := scheduler.PopAvailableRequests(config.ControlBlockSlice)
  // 有可供发送的请求
  return requestToSent
}

// 有 stream 完成时被调用，把完成的 stream 的标志位改为完成状态，并登记完成时间
func (scheduler *DynamicFcfsScheduler) OnStreamFinished(
  finishedBlock *StreamControlBlock) {
  // 让就绪请求入队
  blockSlice := scheduler.ControlBlockSlice.BlockSlice

  for _, block := range blockSlice {
    // 只检查未就绪状态的 request
    if block.Status == UNAVAILABLE &&
      dependenciesMet(block, &blockSlice) {
      // 让就绪的 block 入队，等候调度发送
      block.Status = ENQUEUED
      block.EnqueuedAt = time.Now().UnixNano()
      scheduler.enqueue(block)
    }
  }
}
