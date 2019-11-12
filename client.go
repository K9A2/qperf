package common

import (
  "context"
  "crypto/tls"
  "github.com/google/logger"
  "github.com/stormlin/qperf/datastructures"
  "github.com/stormlin/qperf/quic-go"
  "io"
  //"sync"
  "time"
)

//var clientWaitGroup sync.WaitGroup

// 作为客户端运行
func RunAsClient() {
  logger.Info("Run as client")

  // 生成默认 TLS 加密文件
  tlsConf := &tls.Config{
    InsecureSkipVerify: true,
    NextProtos:         []string{"quic-echo-example"},
  }
  logger.Info("Client TLS qConfig generated")

  programConfig := GetProgramConfig()

  // 与服务器建立连接, 建立 connection 级连接
  serverAddr, serverPort := programConfig.Address, programConfig.Port
  connection, err :=
    quic.DialAddr(serverAddr+":"+serverPort, tlsConf, nil)
  if err != nil {
    logger.Errorf("client: error in connecting server, err: %s\n",
      err.Error())
    return
  }
  logger.Info("client: connected to server")

  // 记录实验开始时间
  start := time.Now()
  logger.Info("client: evaluation started at: %s\n",
    start.Format(time.UnixDate))

  scheduler := programConfig.Scheduler
  // 调整 root request 的状态为就绪状态
  rootRequestBlock := datastructures.GetStreamControlBlockByUrl(programConfig.RootRequestUrl)
  rootRequestBlock.EnqueuedAt = time.Now().Unix()
  scheduler.PendingRequest = rootRequestBlock
  newStream, _ := connection.OpenStreamSync(context.Background())
  // 发起 root stream 作为初始状态
  go sendRequest(&newStream, rootRequestBlock)

  for !scheduler.Finished() {
    nextRequest := scheduler.PopNextRequest(programConfig.ControlBlockSlice)
    if nextRequest != nil {
      newStream, _ := connection.OpenStreamSync(context.Background())
      nextRequest.StartedAt = time.Now().Unix()
      // 在新起的 stream 上发送请求
      go sendRequest(&newStream, nextRequest)
    }
  }
}

// client 发送请求
func sendRequest(stream *quic.Stream, block *datastructures.StreamControlBlock) {
  s := *stream
  defer s.Close()

  // 登记开始时间
  block.StartedAt = time.Now().Unix()
  // 请求指定的资源
  requestBody := make([]byte, 1, 1)
  // 填充 requestBody
  Memset(&requestBody, uint8(block.ResourceId))

  // 发送请求
  _, err := s.Write(requestBody)
  if err != nil {
    logger.Error(
      "Error in sending request for stream: <%d>, requestId: <%d>, "+
        "error: \"%s\"", s.StreamID(), block.ResourceId, err.Error())
    return
  }

  // 创建大小为 1KB 的接收缓冲区，每次实际接收到的字节数由 size 决定
  receiveBuf := make([]byte, 1*KB)
  // 已经接收到的字节数
  receivedSize := 0
  receivedBytes := make([]byte, 0, block.ResponseSize)
  for uint32(receivedSize) < block.ResponseSize {
    size, err := io.ReadFull(s, receiveBuf)
    if err != nil {
      logger.Error("Error in receiving response from stream: <%d>, "+
        "request id: <%d>, error: \"%s\"",
        s.StreamID(), block.ResourceId, err.Error())
      return
    }
    // 处理成功接收到的数据
    receivedBytes = append(receivedBytes, receiveBuf[:size]...)
    receivedSize += size
  }

  // 检查收到的 response body 是否有错
  if !ValidateResponse(&receivedBytes, block.ResponseSize, block.ResourceId) {
    logger.Error("Validation failed for stream: <%d>, request id: <%d>",
      s.StreamID(), block.ResourceId)
    return
  }

  // stream 已完成
  programConfig := GetProgramConfig()
  scheduler := programConfig.Scheduler
  controlBlockSlice := programConfig.ControlBlockSlice
  scheduler.OnStreamFinished(block, controlBlockSlice)
}
