package datastructures

import (
  "sync"
)

// stream 状态常量
type StreamStatus uint8

const (
  UNAVAILABLE StreamStatus = 1 // 依赖项未满足，无法发起此请求
  AVAILABLE   StreamStatus = 2 // 依赖项已满足，可随时发起此请求
  ENQUEUED    StreamStatus = 3 // 已经放入待发送队列中，等待调度器发出请求
  PENDING     StreamStatus = 4 // 请求已发起，正在接受对端传来的数据
  FINISHED    StreamStatus = 5 // 请求已完成
)

// 用来集中存储 stream 在运行期间的各种状态量
type StreamControlBlock struct {
  Status StreamStatus // stream 当前的状态，见常量定义

  ResourceId    uint16  // 资源 ID
  RequestUrl    string  // 请求地址
  ResponseSize  uint32  // 响应大小，单位为 B
  RemainingSize uint32  // 剩余未发送部分大小，单位为 B，初始值与 ResponseSize 等同
  ResourceType  string  // 资源类型
  TTFB          float32 // time to first byte
  Dependencies  []int   // 依赖项的 ResourceId
  Hostname      string  // 资源所在主机名
  Method        string  // 请求方式
  IsStarted     bool    // 是否已经开始
  ServerDelay   float32 // 此资源就绪所耗时间

  ResponseBody []byte // 数据缓冲区

  // 一些时间统计数据
  EnqueuedAt       int64 // 入队时间
  QueuedFor        int64 // 排队时间
  StartedAt        int64 // 开始时间
  FinishedAt       int64 // 结束时间
  TransmissionTime int64 // 传输数据耗时
}

type StreamControlBlockSlice struct {
  Mutex      *sync.Mutex
  BlockSlice []*StreamControlBlock
}

func GetNewControlBlockSlice(capacity int) *StreamControlBlockSlice {
  return &StreamControlBlockSlice{
    Mutex:      &sync.Mutex{},
    BlockSlice: make([]*StreamControlBlock, 0, capacity),
  }
}

// 是否已经回放所有请求
func ReplayFinished(slice *StreamControlBlockSlice) bool {
  defer slice.Mutex.Unlock()
  slice.Mutex.Lock()
  for _, block := range slice.BlockSlice {
    if block.Status != FINISHED {
      return false
    }
  }
  return true
}

// 以 request url 为 key 查找 control block
func GetStreamControlBlockByUrl(
  slice *StreamControlBlockSlice, url string) *StreamControlBlock {
  defer slice.Mutex.Unlock()
  slice.Mutex.Lock()
  for _, stream := range slice.BlockSlice {
    if stream.RequestUrl == url {
      return stream
    }
  }
  return nil
}

// 以 resource id 为 key 查找 control block
func GetStreamControlBlockByResourceId(
  slice *StreamControlBlockSlice, id uint16) *StreamControlBlock {
  defer slice.Mutex.Unlock()
  slice.Mutex.Lock()
  for _, block := range slice.BlockSlice {
    if block.ResourceId == id {
      return block
    }
  }
  return nil
}
