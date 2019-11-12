package schedulers

import (
  "github.com/google/logger"
  . "github.com/stormlin/qperf/constants"
  . "github.com/stormlin/qperf/datastructures"
  "sync"
  "time"
)

// chrome dynamic fcfs scheduler，以非抢占方式实现 dynamic fcfs 调度算法
type DynamicFcfsScheduler struct {
  sync.Mutex

  FirstImageResourceId uint16

  PendingRequest *StreamControlBlock // 当前正在发送的请求，不可被更高优先级的请求抢占

  HighestPriorityQueue *SyncFifoQueue
  HighPriorityQueue    *SyncFifoQueue
  NormalPriorityQueue  *SyncFifoQueue
  LowPriorityQueue     *SyncFifoQueue
  LowestPriorityQueue  *SyncFifoQueue
}

func NewDynamicFcfsScheduler(firstImageResourceId uint16, nRequests int) *DynamicFcfsScheduler {
  return &DynamicFcfsScheduler{
    Mutex:                sync.Mutex{},
    FirstImageResourceId: firstImageResourceId,
    PendingRequest:       nil,
    HighestPriorityQueue: NewSyncFifoQueue(nRequests),
    HighPriorityQueue:    NewSyncFifoQueue(nRequests),
    NormalPriorityQueue:  NewSyncFifoQueue(nRequests),
    LowPriorityQueue:     NewSyncFifoQueue(nRequests),
    LowestPriorityQueue:  NewSyncFifoQueue(nRequests),
  }
}

// 通过判断是否还有在传输的请求来判断是否完成了所有回放
//func (scheduler *DynamicFcfsScheduler) Finished() bool {
//  defer scheduler.Unlock()
//  scheduler.Lock()
//  return scheduler.PendingRequest == nil
//}

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
func dependenciesMet(block *StreamControlBlock,
  slice *StreamControlBlockSlice) bool {
  for _, dep := range block.Dependencies {
    if slice.BlockSlice[dep].Status != FINISHED {
      return false
    }
  }
  return true
}

// 将所有就绪状态的请求加入合适队列
func (scheduler *DynamicFcfsScheduler) enqueueAllAvailableRequests(
  blockSlice *StreamControlBlockSlice) {
  enqueuedRequests := 0
  logger.Info("enqueueAllAvailableRequests: before lock")
  //blockSlice.Mutex.Lock()
  logger.Info("enqueueAllAvailableRequests: after lock")

  for _, block := range blockSlice.BlockSlice {
    // 只检查未就绪状态的 request
    if block.Status == UNAVAILABLE &&
      dependenciesMet(block, blockSlice) {
      // 让就绪的 block 入队，等候调度发送
      block.Status = ENQUEUED
      block.EnqueuedAt = time.Now().Unix()
      scheduler.enqueue(block)
      enqueuedRequests += 1
    }
  }
  //blockSlice.Mutex.Unlock()
  logger.Infof("<%d> request enqueued", enqueuedRequests)
}

// 给出下一个应该发起的请求，结果为 nil 时当前并无应当发送的请求
func (scheduler *DynamicFcfsScheduler) PopNextRequest(
  slice *StreamControlBlockSlice) *StreamControlBlock {
  defer scheduler.Mutex.Unlock()
  logger.Info("PopNextRequest: before lock")
  scheduler.Mutex.Lock()
  logger.Info("PopNextRequest: after lock")

  // 当前请求尚未结束前不会开启下一请求
  if scheduler.PendingRequest != nil {
    logger.Info("PopNextRequest: PendingRequest not nil")
    return nil
  }

  logger.Info("PopNextRequest: PendingRequest nil")

  // 按照优先级发送队列中的请求
  var requestToSent *StreamControlBlock
  logger.Info("before selection")
  if requestToSent = scheduler.HighestPriorityQueue.Get(); requestToSent != nil {
    logger.Info("selection from highest queue")
  } else if requestToSent = scheduler.HighPriorityQueue.Get(); requestToSent != nil {
    logger.Info("selection from high queue")
  } else if requestToSent = scheduler.NormalPriorityQueue.Get(); requestToSent != nil {
    logger.Info("selection from normal queue")
  } else if requestToSent = scheduler.LowPriorityQueue.Get(); requestToSent != nil {
    logger.Info("selection from low queue")
  } else if requestToSent = scheduler.LowestPriorityQueue.Get(); requestToSent != nil {
    logger.Info("selection from lowest queue")
  }
  logger.Info("after selection")

  // 所有队列中均没有可发送的请求
  if requestToSent == nil {
    logger.Info("PopNextRequest: requestToSent nil")
    return nil
  }
  logger.Infof("next request id <%d>", requestToSent.ResourceId)
  // 有可供发送的请求
  scheduler.PendingRequest = requestToSent
  return requestToSent
}

// 有 stream 完成时被调用，把完成的 stream 的标志位改为完成状态，并登记完成时间
func (scheduler *DynamicFcfsScheduler) OnStreamFinished(
  finishedBlock *StreamControlBlock, blockSlice *StreamControlBlockSlice) {
  logger.Info("OnStreamFinished: before lock")
  //blockSlice.Mutex.Lock()
  scheduler.Mutex.Lock()
  blockSlice.Mutex.Lock()
  logger.Info("OnStreamFinished: after lock")
  scheduler.PendingRequest = nil
  // 调整该请求状态为已完成状态
  finishedBlock.Status = FINISHED
  finishedBlock.FinishedAt = time.Now().Unix()
  //blockSlice.Mutex.Unlock()
  // 让就绪请求入队
  logger.Info("before enqueueAllAvailableRequests")
  scheduler.enqueueAllAvailableRequests(blockSlice)
  scheduler.Mutex.Unlock()
  blockSlice.Mutex.Unlock()
}
