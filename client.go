package main

import (
  "context"
  "crypto/tls"
  "github.com/google/logger"
  . "github.com/stormlin/qperf/common"
  . "github.com/stormlin/qperf/constants"
  . "github.com/stormlin/qperf/datastructures"
  "github.com/stormlin/qperf/quic-go"
  "io"
  "time"
)

// 已经建立的连接与 domain 之间的映射关系
var sessionMap map[string]*quic.Session

// 作为客户端运行
func runAsClient() {
  logger.Info("Run as client")
  config := GetProgramConfig()
  sessionMap = make(map[string]*quic.Session)

  // 生成默认 TLS 加密文件
  tlsConfig := &tls.Config{
    InsecureSkipVerify: true,
    NextProtos:         []string{"quic-echo-example"},
  }
  logger.Info("Client TLS config generated")

  // 记录实验开始时间
  start := time.Now()
  logger.Infof("client: evaluation started at: %s\n",
    start.Format(time.UnixDate))

  // 调整 root request 的状态为就绪状态
  rootRequestBlock := GetStreamControlBlockByUrl(
    config.ControlBlockSlice, config.RootRequestUrl)
  rootRequestBlock.EnqueuedAt = time.Now().UnixNano()
  // 发起 root request
  go sendRequest(rootRequestBlock, tlsConfig)

  // 发送剩下的请求
  for !ReplayFinished(config.ControlBlockSlice) {
    select {
    case availableRequests := <-config.StreamAvailableChan:
      {
        // 发送所有就绪的请求
        for _, request := range *availableRequests {
          go sendRequest(request, tlsConfig)
        }
      }
    }
  }

  // 实验已结束
  end := time.Now()
  logger.Infof("evaluation finished, time: <%.3f>s",
    float64(end.Sub(start).Milliseconds())/1000.0)
  config.Collector.PrintReport()
}

// 与指定服务器建立链接
func establishConnection(
  addr string, port string, tlsConfig *tls.Config) (*quic.Session, error) {
  // 与服务器建立连接, 建立 connection 级连接
  session, err := quic.DialAddr(addr+":"+port, tlsConfig, nil)
  if err != nil {
    logger.Errorf("client: error in connecting server, err: %s\n",
      err.Error())
    return nil, err
  }
  logger.Info("client: connected to server")
  return &session, nil
}

// client 发送请求
func sendRequest(block *StreamControlBlock, tlsConfig *tls.Config) {
  config := GetProgramConfig()
  serverMap := *config.ServerControlBlockMap
  session := sessionMap[block.Domain]
  var err error
  if session == nil {
    // 还没有与此服务器建立连接
    session, err = establishConnection(
      config.Address, serverMap[block.Domain].Port, tlsConfig)
    if err != nil {
      logger.Info("error in sending request, err: can not connect to server")
      return
    }
    sessionMap[block.Domain] = session
  }

  if session == nil {
    logger.Infof("got nil session, block <%s>, domain <%s>, port <%s>",
      block.RequestUrl, block.Domain, serverMap[block.Domain].Port)
    return
  }
  // 在同一个 domain 的连接上起一个新的 stream 来承载此请求
  newStream, err := (*session).OpenStreamSync(context.Background())
  if err != nil {
    logger.Infof("error in open stream with domain <%s>, port <%s>, "+
      "error: <%s>", block.Domain, serverMap[block.Domain].Port, err.Error())
    return
  }
  streamId := newStream.StreamID()

  // 登记开始时间
  block.StartedAt = time.Now().UnixNano()
  // 填充 requestBody
  requestBody := make([]byte, 1, 1)
  Memset(&requestBody, uint8(block.ResourceId))

  // 发送请求
  _, err = newStream.Write(requestBody)
  if err != nil {
    logger.Error(
      "Error in sending request for stream: <%d>, requestId: <%d>, "+
        "error: \"%s\"", streamId, block.ResourceId, err.Error())
    return
  }

  // 创建大小为 1KB 的接收缓冲区，每次实际接收到的字节数由 size 决定
  receiveBuf := make([]byte, 1*KB)
  // 已经接收到的字节数
  receivedSize := 0
  receivedBytes := make([]byte, 0, block.ResponseSize)
  for uint32(receivedSize) < block.ResponseSize {
    size, err := io.ReadAtLeast(newStream, receiveBuf, 1)
    if err != nil {
      logger.Error("Error in receiving response from stream: <%d>, "+
        "request id: <%d>, error: \"%s\"", streamId, block.ResourceId,
        err.Error())
      return
    }
    // 处理成功接收到的数据
    receivedBytes = append(receivedBytes, receiveBuf[:size]...)
    receivedSize += size
  }

  // 接受完所有数据之后关闭此 stream
  err = newStream.Close()
  if err != nil {
    logger.Infof("error in closing stream, stream <%s>, domain <%s>, "+
      "port <%s>, error: <%s>", streamId, block.Domain,
      serverMap[block.Domain].Port, err.Error())
    return
  }

  // 检查收到的 response body 是否有错
  if !ValidateResponse(&receivedBytes, block.ResponseSize,
    block.ResourceId) {
    logger.Error("Validation failed for stream: <%d>, request id: <%d>",
      streamId, block.ResourceId)
    return
  }

  // 调整该请求状态为已完成状态
  block.Status = FINISHED
  // 计算结束时间和传输耗时
  block.FinishedAt = time.Now().UnixNano()
  block.QueuedFor = block.StartedAt - block.EnqueuedAt
  block.TransmissionTime = block.FinishedAt - block.StartedAt

  // 触发 scheduler 更新 stream 状态事件
  config.Scheduler.OnStreamFinished(block)

  logger.Infof("request for resource id: <%d> finished, type: <%s>",
    block.ResourceId, block.ResourceType)
}
