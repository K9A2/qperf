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

  scheduler := config.Scheduler
  // 调整 root request 的状态为就绪状态
  rootRequestBlock := GetStreamControlBlockByUrl(
    config.ControlBlockSlice, config.RootRequestUrl)
  rootRequestBlock.EnqueuedAt = time.Now().UnixNano()
  scheduler.PendingRequest = rootRequestBlock
  // 发起 root request
  go sendRequest(rootRequestBlock, tlsConfig)

  for !ReplayFinished(config.ControlBlockSlice) {
    select {
    case finished := <-config.SignalChan:
      {
        config.Collector.AddNewFinishedStream(finished)
        nextRequest := scheduler.PopNextRequest(config.ControlBlockSlice)
        if nextRequest == nil {
          // 全部请求回放完毕
          logger.Info("all finished")
          break
        }
        nextRequest.StartedAt = time.Now().UnixNano()
        // 在新起的 stream 上发送请求
        go sendRequest(nextRequest, tlsConfig)
      }
    }
  }

  end := time.Now()
  logger.Infof("evaluation finished, time: <%.3f>s",
    float64(end.Sub(start).Milliseconds())/1000.0)

  // 打印性能测试数据
  report := config.Collector.GetTimingReport()
  PrintTimingReport(report.DocumentReport, DOCUMENT)
  PrintTimingReport(report.StylesheetReport, STYLESHEET)
  PrintTimingReport(report.XhrReport, XHR)
  PrintTimingReport(report.ScriptReport, SCRIPT)
  PrintTimingReport(report.ImageReport, IMAGE)
  PrintTimingReport(report.OtherReport, OTHER)

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
    session, err = establishConnection(config.Address, serverMap[block.Domain].Port, tlsConfig)
    if err != nil {
      logger.Info("error in sending request, err: can not connect to server")
      return
    }
    sessionMap[block.Domain] = session
  }

  // 创建完成本次请求所需要的 stream
  if session == nil {
    logger.Infof("got nil session, block <%s>, domain <%s>, port <%s>",
      block.RequestUrl, block.Domain, serverMap[block.Domain].Port)
    return
  }
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

  // stream 已完成，触发 stream 状态更新事件
  config.Scheduler.OnStreamFinished(block, config.ControlBlockSlice)
  // 向 signal chan 发送信号，以启动下一请求
  config.SignalChan <- block

  logger.Infof("request for resource id: <%d> finished, type: <%s>",
    block.ResourceId, block.ResourceType)
}
