package common

import (
  "github.com/stormlin/qperf/config"
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
  sync.Mutex
  BlockSlice []*StreamControlBlock
}

var controlBlockSlice StreamControlBlockSlice

// 按照请求日志文件的要求初始化空白控制块
func BuildStreamControlBlockSlice(filteredEntries *[]config.FilteredEntry,
  runAsServer bool) *StreamControlBlockSlice {
  for _, entry := range *filteredEntries {
    block := StreamControlBlock{
      ResourceId:    uint16(entry.ResourceId),
      RequestUrl:    entry.RequestUrl,
      ResponseSize:  uint32(entry.ResponseSize),
      RemainingSize: uint32(entry.RemainingSize),
      ResourceType:  entry.ResourceType,
      TTFB:          entry.TTFB,
      Dependencies:  entry.Dependencies,
      Hostname:      entry.Hostname,
      Method:        entry.Method,
      IsStarted:     entry.IsStarted,
      ServerDelay:   entry.ServerDelay,
    }
    if runAsServer {
      block.ResponseBody = make([]byte, 0, entry.ResponseSize)
      // 只在 server 一侧初始化 response body 以节省内存空间
      Memset(&block.ResponseBody, uint8(block.ResourceId))
    }
    // 把此 block 添加到 block slice 中，以便供其他模块使用
    controlBlockSlice.BlockSlice = append(controlBlockSlice.BlockSlice, &block)
  }
  return &controlBlockSlice
}

// 以 request url 为 key 查找 control block
func GetStreamControlBlockByUrl(url string) *StreamControlBlock {
  defer controlBlockSlice.Unlock()
  controlBlockSlice.Lock()
  for _, stream := range controlBlockSlice.BlockSlice {
    if stream.RequestUrl == url {
      return stream
    }
  }
  return nil
}

// 以 resource id 为 key 查找 control block
func GetStreamControlBlockByResourceId(id uint16) *StreamControlBlock {
  defer controlBlockSlice.Unlock()
  controlBlockSlice.Lock()
  for _, block := range controlBlockSlice.BlockSlice {
    if block.ResourceId == id {
      return block
    }
  }
  return nil
}
//
//// 活跃 stream 队列
//type StreamControlBlockQueue struct {
//  queue []*StreamControlBlock
//}
//
//// 可供发送的请求队列
//var requestsToSent StreamControlBlockQueue
//
//func (asq *StreamControlBlockQueue) Put(block *StreamControlBlock) {
//  asq.queue = append(asq.queue, block)
//}
//
//func (asq *StreamControlBlockQueue) Get() *StreamControlBlock {
//  if len(asq.queue) <= 0 {
//    return nil
//  }
//  item := asq.queue[0]
//  asq.queue = asq.queue[1:]
//  return item
//}
