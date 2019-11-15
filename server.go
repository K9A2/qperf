package main

import (
  "context"
  "github.com/google/logger"
  . "github.com/stormlin/qperf/common"
  . "github.com/stormlin/qperf/constants"
  . "github.com/stormlin/qperf/datastructures"
  "github.com/stormlin/qperf/quic-go"
  "io"
  "sync"
)

//var serverWaitGroup sync.WaitGroup

// 服务器主线程
func runAsServer() {
  logger.Info("run as server")

  programConfig := GetProgramConfig()

  // 初始化响应体
  for _, block := range programConfig.ControlBlockSlice.BlockSlice {
    MakeResponseBody(block)
  }

  // 添加端口监听, 利用生成的 TLS 数据建立连接
  serverAddr, serverPort := programConfig.Address, programConfig.Port
  listener, err := quic.ListenAddr(
    serverAddr+":"+serverPort, GenerateTLSConfig(), nil)
  defer listener.Close()
  if err != nil {
    logger.Errorf("Error in create listener: <%s>, err: %s\n",
      serverAddr+":"+serverPort, err.Error())
    return
  }
  logger.Infof("listening at <%s>\n", serverAddr+":"+serverPort)

  // 循环执行服务器进程，直到被控制台终止
  for true {
    // stream 结束时向此发送消息
    var streamWaitGroup sync.WaitGroup

    // 接受外界请求, connection 级
    session, err := listener.Accept(context.Background())
    if err != nil {
      session.Close()
      logger.Errorf("Error in establish connection with client: <%s> "+
        "err: \"%s\"", session.RemoteAddr(), err.Error())
      return
    }
    logger.Infof("server: received connection from: %s\n",
      session.RemoteAddr().String())

    // 只接受指定数目的 stream
    acceptedStreams := 0
    for acceptedStreams < len(programConfig.ControlBlockSlice.BlockSlice) {
      stream, err := session.AcceptStream(context.Background())
      if err != nil {
        logger.Errorf("Error in accepting stream from: <%s>, "+
          "err: \"%s\"", session.RemoteAddr(), err.Error())
        return
      }
      if stream == nil {
        logger.Error("get nil stream")
        return
      }
      streamWaitGroup.Add(1)
      logger.Infof("server: stream %d accepted\n", stream.StreamID())
      acceptedStreams += 1
      // 转入另外的 go 程接受数据
      go handleRequest(&stream, &streamWaitGroup)
    }

    // 等待所有的 stream 都终止之后在结束 connection
    streamWaitGroup.Wait()
    // 所有 stream 都已经结束
    //err = session.Close()
    //if err != nil {
    //  logger.Errorf("server: error in closing connection, err = %s\n",
    //    err.Error())
    //  return
    //}
    logger.Infof("server: %d stream finished\n", acceptedStreams)
  }
}

// 负责处理请求
func handleRequest(stream *quic.Stream, streamWaitGroup *sync.WaitGroup) {
  s := *stream
  defer streamWaitGroup.Done()

  programConfig := GetProgramConfig()

  // 获取请求的资源 id
  requestBody := make([]byte, 1*B)
  _, err := io.ReadFull(s, requestBody)
  if err != nil {
    logger.Errorf("Error in receiving request, stream id: <%d>, "+
      "err: \"%s\"", s.StreamID(), err.Error())
    return
  }
  resourceId := uint16(requestBody[0])
  controlBlock := GetStreamControlBlockByResourceId(
    programConfig.ControlBlockSlice, resourceId)
  logger.Infof("Client requesting resource id: <%d>, request url: "+
    "\"%s\", resource type: <%s>", controlBlock.ResourceId,
    controlBlock.RequestUrl, controlBlock.ResourceType)

  size, err := s.Write(controlBlock.ResponseBody)
  logger.Infof("Response body size <%d>B for resource id <%d>, "+
    "transmitted size: <%d>B", len(controlBlock.ResponseBody),
    controlBlock.ResourceId, size)
  if err != nil {
    logger.Errorf("Error in sending response, stream id: <%d>, request"+
      " id: <%d>, sent size: <%d>, error: \"%s\"", s.StreamID(), resourceId,
      size, err.Error())
    return
  }
  if uint32(size) != controlBlock.ResponseSize {
    logger.Errorf("Error in sending response, stream id: <%d>, request"+
      " id: <%d>, sent size: <%d>", s.StreamID(), controlBlock.ResourceId, size)
    return
  }
}

