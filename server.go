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
  "time"
)

// 用于阻塞 server 主线程，让其不会在起完所有子服务器之后退出
var domainWaitGroup sync.WaitGroup

// 服务器模式主线程，主要作用是建立各 domain 对应的子服务器，以模拟多 domain 情景
func runAsServer() {
  logger.Info("run as server")
  config := GetProgramConfig()
  // 只在 server 一侧初始化响应体以节省内存空间
  for _, block := range config.ControlBlockSlice.BlockSlice {
    MakeResponseBody(block)
  }

  // 本机 IP 地址
  serverAddr := config.Address
  // 在新起的 go 程中监听各 domain 地址
  for _, block := range *config.ServerControlBlockMap {
    domainWaitGroup.Add(1)
    // 每一个子服务器实际监听的地址
    go spawnNewServer(block, serverAddr)
  }
  domainWaitGroup.Wait()
  logger.Infof("server: all domain server exited")
}

// 根据端口号创建不同的服务器
func spawnNewServer(block *ServerControlBlock, serverAddr string) {
  addr := serverAddr + ":" + block.Port
  // 不同的 domain 监听不同的 port
  listener, err := quic.ListenAddr(addr, GenerateTLSConfig(), nil)
  if err != nil {
    logger.Errorf("Error in create listener for domain: <%s>, server "+
      "addr: <%s>, err: %s\n", block.Domain, addr, err.Error())
    return
  }
  logger.Infof("domain <%s> listening at <%s>\n", block.Domain, addr)

  // 服务器处于不停接受外界请求的状态
  for true {
    // 接受外界请求, connection 级
    session, err := listener.Accept(context.Background())
    if err != nil {
      if session != nil {
        _ = session.Close()
        logger.Errorf("Error in establishing connection with client: <%s> "+
          "err: \"%s\"", session.RemoteAddr(), err.Error())
        return
      }
      logger.Errorf("<%s>: error in establishing connection, err <%s>",
        addr, err.Error())
      return
    }
    logger.Infof("server <%s> accepted connection from: %s\n",
      addr, session.RemoteAddr().String())
    go handleSession(session, block)
  }
}

// 处理每一条进来的 session
func handleSession(session quic.Session, block *ServerControlBlock) {
  acceptedRequestCount := 0
  for acceptedRequestCount < block.RequestCount {
    stream, err := session.AcceptStream(context.Background())
    if err != nil {
      if stream == nil {
        logger.Errorf("domain <%s>: get nil stream", block.Domain)
      }
      logger.Errorf("Error in accepting stream from: <%s>, "+
        "err: \"%s\"", session.RemoteAddr(), err.Error())
      return
    }
    logger.Infof("server <%s>: stream %d accepted\n",
      block.Domain, stream.StreamID())
    acceptedRequestCount += 1
    // 转入另外的 go 程接受数据
    go handleRequest(&stream)
  }

  logger.Infof("domain <%s> processed <%d> streams and finished\n",
    block.Domain, acceptedRequestCount)
}

// 负责处理请求
func handleRequest(stream *quic.Stream) {
  s := *stream
  config := GetProgramConfig()

  // 获取请求的资源 id
  requestBody := make([]byte, 1*B)
  _, err := io.ReadFull(s, requestBody)
  if err != nil {
    logger.Errorf("Error in receiving request, stream id: <%d>, "+
      "err: \"%s\"", s.StreamID(), err.Error())
    return
  }

  // 根据请求的资源 id 获取 control block 及其 response buffer
  resourceId := uint16(requestBody[0])
  targetControlBlock := GetStreamControlBlockByResourceId(
    config.ControlBlockSlice, resourceId)
  logger.Infof("Client requesting resource id: <%d>, request url: "+
    "\"%s\", resource type: <%s>", targetControlBlock.ResourceId,
    targetControlBlock.RequestUrl, targetControlBlock.ResourceType)

  // 模拟 server 生成 response body 所需要的时间
  serverDelay := targetControlBlock.ServerDelay
  time.Sleep(time.Duration(serverDelay) * time.Millisecond)

  // 开始返回 response body
  size, err := s.Write(targetControlBlock.ResponseBody)
  if err != nil {
    logger.Errorf("Error in sending response, stream id: <%d>, request"+
      " id: <%d>, sent size: <%d>, error: \"%s\"", s.StreamID(), resourceId,
      size, err.Error())
    return
  }
  if uint32(size) != targetControlBlock.ResponseSize {
    logger.Errorf("Error in sending response, stream id: <%d>, request"+
      " id: <%d>, sent size: <%d>", s.StreamID(), targetControlBlock.ResourceId, size)
    return
  }
  logger.Infof("Response body size <%d>B for resource id <%d>, "+
    "transmitted size: <%d>B, server delay: <%.3f>",
    len(targetControlBlock.ResponseBody), targetControlBlock.ResourceId, size, serverDelay)
}
