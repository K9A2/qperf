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
  "net/url"
  "os"
  "sync"
  "time"
)

func main() {
  logger.Init("main", false, true, os.Stdout)

  // 加载程序参数
  options, err := ParseOptions()
  if err != nil {
    logger.Errorf("Error in parsing program arguments, error: \"%s\"",
      err.Error())
    PrintUsage()
    os.Exit(-1)
  }
  config := BuildProgramConfig(options)
  logger.Infof("param <IterationCount> = %d\n", config.IterationCount)
  logger.Infof("param <ServerAddr> = %s\n", config.Address)
  logger.Infof("param <ServerPort> = %s\n", config.Port)
  logger.Infof("param <StreamMultiplexLimit> = %d\n", config.StreamMultiplexLimit)
  logger.Infof("param <GroupNumber> = %d\n", config.GroupNumber)
  escapedUrl, _ := url.QueryUnescape(config.RootRequestUrl)
  logger.Infof("param <RootRequestUrl> = %s\n", escapedUrl)
  logger.Infof("Number of requests = <%d>\n", len(config.ControlBlockSlice.BlockSlice))

  // 在冗长模式下输出配置文件中包含的连接信息
  if options.Verbose {
    for index, block := range config.ControlBlockSlice.BlockSlice {
      escapedUrl, _ = url.QueryUnescape(block.RequestUrl)
      logger.Infof("request <%d>, url = <?%s>\n", index+1, block.RequestUrl)
      logger.Infof("  resource_id = <%d>, response_size = <%3.2f>KB, "+
        "resource_type = <%s>, dependencies = <%v>",
        block.ResponseBody[0],
        float32(block.ResponseSize)/1000.0,
        block.ResourceType,
        block.Dependencies,
      )
    }
  }

  //运行于客户端模式或者服务器模式
  if options.Client && !options.Server {
    runAsClient()
  } else if !options.Client && options.Server {
    runAsServer()
  }
}

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

// 作为客户端运行
func runAsClient() {
  logger.Info("Run as client")

  // 生成默认 TLS 加密文件
  tlsConf := &tls.Config{
    InsecureSkipVerify: true,
    NextProtos:         []string{"quic-echo-example"},
  }
  logger.Info("Client TLS config generated")

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
  logger.Infof("client: evaluation started at: %s\n",
    start.Format(time.UnixDate))

  scheduler := programConfig.Scheduler
  // 调整 root request 的状态为就绪状态
  rootRequestBlock := GetStreamControlBlockByUrl(
    programConfig.ControlBlockSlice, programConfig.RootRequestUrl)
  rootRequestBlock.EnqueuedAt = time.Now().Unix()
  scheduler.PendingRequest = rootRequestBlock
  newStream, _ := connection.OpenStreamSync(context.Background())
  // 发起 root request
  go sendRequest(&newStream, rootRequestBlock)
  logger.Info("root request sent")

  logger.Infof("before loop")
  for !ReplayFinished(programConfig.ControlBlockSlice) {
    select {
    case finished := <-programConfig.SignalChan:
      {
        logger.Infof("request id <%d> finished and passed by signal chan",
          finished.ResourceId)
        nextRequest := scheduler.PopNextRequest(programConfig.ControlBlockSlice)
        if nextRequest == nil {
          // 全部请求回放完毕
          logger.Info("all finished")
          break
        }

        logger.Infof("next resource id <%d>", nextRequest.ResourceId)
        newStream, _ := connection.OpenStreamSync(context.Background())
        nextRequest.StartedAt = time.Now().Unix()
        // 在新起的 stream 上发送请求
        go sendRequest(&newStream, nextRequest)
      }
    }
  }
  logger.Infof("after loop")

  //for !ReplayFinished(programConfig.ControlBlockSlice) {
  //  if nextRequest != nil {
  //  }
  //}
}

// client 发送请求
func sendRequest(stream *quic.Stream, block *StreamControlBlock) {
  s := *stream

  logger.Infof("sending request for resource id: <%d>", block.ResourceId)

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

  logger.Infof("request for resource id: <%d> sent", block.ResourceId)

  // 创建大小为 1KB 的接收缓冲区，每次实际接收到的字节数由 size 决定
  receiveBuf := make([]byte, 1*KB)
  // 已经接收到的字节数
  receivedSize := 0
  receivedBytes := make([]byte, 0, block.ResponseSize)
  for uint32(receivedSize) < block.ResponseSize {
    size, err := io.ReadAtLeast(s, receiveBuf, 1)
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

  logger.Infof("request for resource id: <%d> received", block.ResourceId)

  // 检查收到的 response body 是否有错
  if !ValidateResponse(&receivedBytes, block.ResponseSize,
    block.ResourceId) {
    logger.Error("Validation failed for stream: <%d>, request id: <%d>",
      s.StreamID(), block.ResourceId)
    return
  }

  logger.Infof("request for resource id: <%d> validated", block.ResourceId)

  // stream 已完成，触发 stream 状态更新事件
  programConfig := GetProgramConfig()
  programConfig.Scheduler.
    OnStreamFinished(block, programConfig.ControlBlockSlice)
  // 向 signal chan 发送信号，以启动下一请求
  programConfig.SignalChan <- block

  logger.Infof("request for resource id: <%d> finished", block.ResourceId)
}
