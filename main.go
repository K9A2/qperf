package main

import (
  "context"
  "crypto/rand"
  "crypto/rsa"
  "crypto/tls"
  "crypto/x509"
  "encoding/pem"
  "fmt"
  . "github.com/stormlin/qperf/result"
  "io"
  "math/big"
  "os"
  "sort"
  "sync"
  "time"

  "github.com/jessevdk/go-flags"
  . "github.com/stormlin/qperf/parameters"
  "github.com/stormlin/qperf/quic-go"
)

// 参数列表
var opts struct {
  // 运行于客户端模式
  Client bool `short:"c" long:"client" description:"run as client"`
  // 运行于服务器模式
  Server bool `short:"s" long:"server" description:"run as server"`
  // 配置文件路径
  Config string `short:"C" long:"config" description:"load configuration file"`
}

// 打印使用信息
func printUsage() {
  fmt.Println("fuck usage")
}

func main() {
  // 读取程序参数
  args := os.Args
  args, err := flags.ParseArgs(&opts, args)
  if err != nil {
    fmt.Println("parse arguments error")
    printUsage()
    os.Exit(1)
  }

  // 加载配置文件
  LoadConfigurationFile(opts.Config)
  config := GetConfig()

  if opts.Client && !opts.Server {
    var clientWaitGroup sync.WaitGroup
    var evaluationResult EvaluationResult
    for i := 0; i < config.IterationCount; i++ {
      connectionResult := ConnectionResult{ConnectionID: i}
      clientWaitGroup.Add(1)
      // 运行于客户端模式
      runAsClient(&connectionResult, &clientWaitGroup)
      // 阻塞至本次测试完成
      clientWaitGroup.Wait()
      evaluationResult.ClientResults = append(evaluationResult.ClientResults, connectionResult)
      // 提供 5 秒的等待时间
      fmt.Printf("client: time to next evaluation = %ds\n", TimeToNextEvaluation)
      time.Sleep(time.Duration(TimeToNextEvaluation) * time.Second)
    }
    // 完成所有测试
    fmt.Printf("client: %d eavluations done\n", len(evaluationResult.ClientResults))
    fileName := fmt.Sprintf("group-%d-stream-%d-limit-%d.json", config.GroupNumber, config.NumberOfStreams, config.StreamMultiplexLimit)
    WriteToJsonFile(fileName, evaluationResult)
  } else if !opts.Client && opts.Server {
    // 运行于服务器模式
    runAsServer()
  }
}

// 判断缓冲区中收到的是否是终止实验的指令。目前使用全 1 缓冲区作为终止实验的指令。
func isCloseMessage(buf []byte) bool {
  // 全 1 是关闭 stream 的消息，全 0 是无意义的测试数据
  return buf[0] == 1
}

// 从 stream 中接收数据
func receiveMessage(stream quic.Stream, waitGroup *sync.WaitGroup) {
  defer waitGroup.Done()

  config := GetConfig()

  // 建立接收缓冲区
  receiveBuf := make([]byte, config.BufferSize)
  streamID := stream.StreamID()

  // 不停接受数据直到接收到终止消息
  var receivedSize int
  for true {
    // 从缓冲区中读取数据
    size, err := io.ReadFull(stream, receiveBuf)
    receivedSize += size
    if err != nil && err.Error() != "NO_ERROR" {
      // 接收过程出错
      fmt.Printf("stream %d: error in reading message, received size: %d, error: %s\n", streamID, receivedSize, err.Error())
      break
    }
    if isCloseMessage(receiveBuf) {
      fmt.Printf("stream %d: close message received\n", streamID)
      break
    }
  }

  // 在 server 一侧关闭 stream
  err := stream.Close()
  if err != nil {
    // 关闭过程出错
    fmt.Printf("stream %d: error in closing this stream\n", streamID)
  }
  fmt.Printf("stream %d: closed\n", streamID)
}

// 服务器主线程
func runAsServer() {
  fmt.Println("run as server")

  config := GetConfig()

  // 添加端口监听, 利用生成的 TLS 数据建立连接
  serverAddr := config.ServerAddr // Mininet 环境中需指明要监听的地址
  serverPort := config.ServerPort // 要监听的端口号
  listener, err := quic.ListenAddr(serverAddr+":"+serverPort, generateTLSConfig(), nil)
  if err != nil {
    fmt.Printf("fuck error, err: %s\n", err.Error())
    return
  }
  fmt.Printf("server: listening at %s\n", serverAddr+":"+serverPort)

  // 循环执行服务器进程，直到被控制台终止
  for true {
    // stream 结束时向此发送消息
    var streamWaitGroup sync.WaitGroup

    // 接受外界请求, connection 级
    session, err := listener.Accept(context.Background())
    if err != nil {
      return
    }
    fmt.Printf("server: received connection from: %s\n", session.RemoteAddr().String())

    // 只接受指定数目的 stream
    for i := 0; i < config.NumberOfStreams; i++ {
      stream, _ := session.AcceptStream(context.Background())
      if stream != nil {
        streamWaitGroup.Add(1)
        fmt.Printf("server: stream %d accepted\n", stream.StreamID())
        // 转入另外的 go 程接受数据
        go receiveMessage(stream, &streamWaitGroup)
      }
    }

    // 等待所有的 stream 都终止之后在结束 connection
    streamWaitGroup.Wait()
    // 所有 stream 都已经结束
    err = session.Close()
    if err != nil {
      fmt.Printf("server: error in closing connection, err = %s\n", err.Error())
    }
    fmt.Println("server: this connection closed")
  }
}

// 在 client 一侧发送数据
func sendMessage(stream quic.Stream, waitGroup *sync.WaitGroup, streamResultSlice *[]StreamResult) {
  defer waitGroup.Done()

  streamID := stream.StreamID()
  // 用于保存本 stream 的测试结果
  streamResult := StreamResult{}
  streamResult.StreamID = int64(streamID)

  config := GetConfig()

  // 创建循环使用的测试数据缓冲区
  message := make([]byte, config.BufferSize)
  for i := 0; i < config.BufferSize; i++ {
    // 使用全 0 来初始化发送缓冲，以和全 1 的终止消息相区别
    message[i] = 0
  }
  fmt.Printf("stream %d: buffer created\n", streamID)

  // 数据流的实际起始时间
  streamResult.Start = time.Now()

  // 循环发送同一数据块指导发完全部数据量
  var sentSize int // 已发送的数据量
  // 只需发送本 stream 所对应的数据区长度
  for i := 0; i < (config.DataSize/config.NumberOfStreams)/config.BufferSize; i++ {
    size, err := stream.Write(message)
    sentSize += size
    if err != nil {
      // 发送出错
      fmt.Printf("stream %d: error in sending messages, %d bytes sent, err: %s\n", streamID, sentSize, err.Error())
      return
    }
  }
  fmt.Printf("stream %d: message sent\n", streamID)

  // 数据流的实际终止时间
  streamResult.End = time.Now()
  // 数据流的实际持续时间
  streamResult.Total = streamResult.End.Sub(streamResult.Start).Seconds()
  // 把本 stream 的结果添加到全局结果中
  *streamResultSlice = append(*streamResultSlice, streamResult)

  // 发送结束信息
  for i := 0; i < config.BufferSize; i++ {
    // 全部置 1 以表示结束消息
    message[i] = 1
  }
  _, err := stream.Write(message)
  if err != nil && err.Error() != "NO_ERROR" {
    fmt.Printf("stream %d: error in sending close stream message, err: %s\n", streamID, err.Error())
    return
  }
  fmt.Printf("stream %d: stream close message sent\n", stream.StreamID())

  // 关闭此 stream
  err = stream.Close()
  if err != nil {
    fmt.Printf("stream %d: error in closeing this stream, err: %s\n", streamID, err.Error())
    return
  }
  fmt.Printf("stream %d: closed\n", streamID)
}

// 作为客户端运行
func runAsClient(connectionResult *ConnectionResult, clientWaitGroup *sync.WaitGroup) {
  fmt.Println("run as client")

  // 用于接受各 stream 的结束信号
  var streamWaitGroup sync.WaitGroup
  // 用于接收各 stream 的测试结果，当前 slice 为空，后续使用 append 来向其中添加数据
  var streamResultSlice []StreamResult

  // 生成默认 TLS 加密文件
  tlsConf := &tls.Config{
    InsecureSkipVerify: true,
    NextProtos:         []string{"quic-echo-example"},
  }
  fmt.Println("client: TLS config generated")

  config := GetConfig()

  // 与服务器建立连接, 建立 connection 级连接
  serverAddr := config.ServerAddr
  serverPort := config.ServerPort
  session, err := quic.DialAddr(serverAddr+":"+serverPort, tlsConf, nil)
  if err != nil {
    fmt.Printf("client: error in connecting server, err: %s\n", err.Error())
    return
  }
  fmt.Println("client: connected to server")

  start := time.Now()
  fmt.Printf("client: evaluation started at: %s\n", start.Format(time.UnixDate))

  // 按照设定起指定数量的 stream
  for i := 0; i < config.NumberOfStreams; i++ {
    stream, err := session.OpenStreamSync(context.Background())
    if err != nil {
      fmt.Printf("cleint: error in creating stream, err: %s\n", err.Error())
      return
    }
    fmt.Printf("client: stream %d created\n", stream.StreamID())
    streamWaitGroup.Add(1)
    // 转到另外的 go 程来发送数据
    go sendMessage(stream, &streamWaitGroup, &streamResultSlice)
  }

  // 处理连接的结束
  streamWaitGroup.Wait()
  err = session.Close()
  if err != nil {
    fmt.Println("client: error in closing connection")
    return
  }
  fmt.Println("client: connection closed")

  // 所有 stream 的最早开始时间
  minStartTime := streamResultSlice[0].Start
  // 所有 stream 的最晚结束时间
  maxEndTime := streamResultSlice[0].Start
  // 计算此链接发送数据部分所需要的时间
  for i := 1; i < len(streamResultSlice); i++ {
    r := streamResultSlice[i]
    if r.Start.Before(minStartTime) {
      minStartTime = r.Start
    }
    if r.End.After(maxEndTime) {
      maxEndTime = r.End
    }
  }

  // 对结果集进行排序
  sort.Slice(streamResultSlice, func(i, j int) bool {
    return streamResultSlice[i].StreamID < streamResultSlice[j].StreamID
  })

  // 输出所有结果
  for _, r := range streamResultSlice {
    fmt.Println(r.String())
  }

  // 把实验结果添加到全局结果集中
  connectionResult.StreamResultSlice = streamResultSlice
  // 打印测试时间
  fmt.Printf("client: evaluation takes %f seconds to finish\n", maxEndTime.Sub(minStartTime).Seconds())
  // 通知上层
  clientWaitGroup.Done()
}

// 生成 TLS 证书
func generateTLSConfig() *tls.Config {
  key, err := rsa.GenerateKey(rand.Reader, 1024)
  if err != nil {
    panic(err)
  }
  template := x509.Certificate{SerialNumber: big.NewInt(1)}
  certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
  if err != nil {
    panic(err)
  }
  keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
  certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

  tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
  if err != nil {
    panic(err)
  }
  return &tls.Config{
    Certificates: []tls.Certificate{tlsCert},
    NextProtos:   []string{"quic-echo-example"},
  }
}
