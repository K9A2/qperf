package common

import (
  "github.com/google/logger"
  "github.com/stormlin/qperf/config"
  "github.com/stormlin/qperf/datastructures"
  "sync"
  "time"
)

// chrome dynamic fcfs scheduler，以非抢占方式实现 dynamic fcfs 调度算法
type DynamicFcfsScheduler struct {
  sync.Mutex

  PendingRequest *datastructures.StreamControlBlock // 当前正在发送的请求，不可被更高优先级的请求抢占

  HighestPriorityQueue datastructures.SyncFifoQueue
  HighPriorityQueue    datastructures.SyncFifoQueue
  NormalPriorityQueue  datastructures.SyncFifoQueue
  LowPriorityQueue     datastructures.SyncFifoQueue
  LowestPriorityQueue  datastructures.SyncFifoQueue
}

// 通过判断是否还有在传输的请求来判断是否完成了所有回放
func (scheduler DynamicFcfsScheduler) Finished() bool {
  defer scheduler.Unlock()
  scheduler.Lock()
  return scheduler.PendingRequest == nil
}

// 确认该 script 是否为第一张图片之后的 JS 文件
func scriptAfterFirstImage(resourceId uint16, firstImageResourceId uint16) bool {
  return resourceId < firstImageResourceId
}

// 根据请求类型将其加入合适的队列
func (scheduler DynamicFcfsScheduler) enqueue(block *datastructures.StreamControlBlock) {
  switch block.ResourceType {
  case config.DOCUMENT:
    scheduler.HighestPriorityQueue.Put(block)
    break
  case config.STYLESHEET:
    scheduler.HighestPriorityQueue.Put(block)
    break
  case config.XHR:
    scheduler.HighPriorityQueue.Put(block)
    break
  case config.SCRIPT:
    if scriptAfterFirstImage(
      block.ResourceId, config.GetProgramConfig().FirstImageResourceId) {
      scheduler.NormalPriorityQueue.Put(block)
      break
    }
    scheduler.HighPriorityQueue.Put(block)
    break
  case config.IMAGE:
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
func dependenciesMet(block *datastructures.StreamControlBlock, slice *datastructures.StreamControlBlockSlice) bool {
  for _, dep := range block.Dependencies {
    if slice.BlockSlice[dep].Status != datastructures.AVAILABLE {
      return false
    }
  }
  return true
}

// 将所有就绪状态的请求加入合适队列
func (scheduler DynamicFcfsScheduler) enqueueAllAvailableRequests(
  blockSlice *datastructures.StreamControlBlockSlice) {
  defer blockSlice.Unlock()

  blockSlice.Lock()
  for _, block := range blockSlice.BlockSlice {
    // 只检查未就绪状态的 request
    if block.Status == datastructures.UNAVAILABLE && dependenciesMet(block, blockSlice) {
      // 让就绪的 block 入队，等候调度发送
      block.Status = datastructures.ENQUEUED
      block.EnqueuedAt = time.Now().Unix()
      scheduler.enqueue(block)
    }
  }
}

// 给出下一个应该发起的请求，结果为 nil 时当前并无应当发送的请求
func (scheduler DynamicFcfsScheduler) PopNextRequest(
  slice *datastructures.StreamControlBlockSlice) *datastructures.StreamControlBlock {
  // 当前请求尚未结束前不会开启下一请求
  if scheduler.PendingRequest != nil {
    return nil
  }

  // 按照优先级发送队列中的请求
  var requestToSent *datastructures.StreamControlBlock
  if !scheduler.HighestPriorityQueue.Empty() {
    requestToSent = scheduler.HighestPriorityQueue.Get()
  } else if !scheduler.HighPriorityQueue.Empty() {
    requestToSent = scheduler.HighPriorityQueue.Get()
  } else if !scheduler.NormalPriorityQueue.Empty() {
    requestToSent = scheduler.NormalPriorityQueue.Get()
  } else if !scheduler.LowPriorityQueue.Empty() {
    requestToSent = scheduler.LowPriorityQueue.Get()
  } else if !scheduler.LowestPriorityQueue.Empty() {
    requestToSent = scheduler.LowestPriorityQueue.Get()
  }

  // 所有队列中均没有可发送的请求
  if requestToSent == nil {
    return nil
  }
  // 有可供发送的请求
  scheduler.PendingRequest = requestToSent
  return requestToSent
}

// 有 stream 完成时被调用，把完成的 stream 的标志位改为完成状态，并登记完成时间
func (scheduler DynamicFcfsScheduler) OnStreamFinished(
  finishedBlock *datastructures.StreamControlBlock, blockSlice *datastructures.StreamControlBlockSlice) {
  defer blockSlice.Unlock()

  blockSlice.Lock()
  scheduler.PendingRequest = nil
  // 调整该请求状态为已完成状态
  finishedBlock.Status = datastructures.FINISHED
  finishedBlock.FinishedAt = time.Now().Unix()
  // 让就绪请求入队
  scheduler.enqueueAllAvailableRequests(blockSlice)
}
