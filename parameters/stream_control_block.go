package parameters

// stream 状态
type StreamStatus uint8

const (
  UNAVAILABLE StreamStatus = 1
  ENQUEUED    StreamStatus = 2
  FINISHED    StreamStatus = 3
)

// 用来集中存储 stream 在运行期间的各种状态量
type StreamControlBlock struct {
  Status uint8 // stream 当前的状态，见常量定义

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

  ByteBuffer []byte // 数据缓冲区

  // 一些时间统计数据
  EnqueuedAt       uint64 // 入队时间
  QueuedFor        uint64 // 排队时间
  StartedAt        uint64 // 开始时间
  FinishedAt       uint64 // 结束时间
  TransmissionTime uint64 // 传输数据耗时
}

var streamControlBlockSlice []*StreamControlBlock

func AddNewStreamControlBlock(block *StreamControlBlock) {
  streamControlBlockSlice = append(streamControlBlockSlice, block)
}

func GetStreamControlBlockSlice() *[]*StreamControlBlock {
  return &streamControlBlockSlice
}

func GetStreamControlBlockByUrl(url string) *StreamControlBlock {
  for _, stream := range streamControlBlockSlice {
    if stream.RequestUrl == url {
      return stream
    }
  }
  return nil
}

// 通过检查所有 stream 是否为已结束状态来确定整个连接是否已结束
func IsReplayFinished(streamStatus *[]StreamStatus) bool {
  for _, status := range *streamStatus {
    if status != FINISHED {
      return false
    }
  }
  return true
}

// 活跃 stream 队列
type ActiveStreamQueue struct {
  queue []*StreamControlBlock
}

var activeStreamQueue ActiveStreamQueue

func GetQueue() *ActiveStreamQueue {
  return &activeStreamQueue
}

func (asq *ActiveStreamQueue) Put(block *StreamControlBlock) {
  asq.queue = append(asq.queue, block)
}

func (asq *ActiveStreamQueue) Get() *StreamControlBlock {
  if len(asq.queue) <= 0 {
    return nil
  }
  item := asq.queue[0]
  asq.queue = asq.queue[1:]
  return item
}

