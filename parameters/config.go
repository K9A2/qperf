package parameters

import (
  "encoding/json"
  "fmt"
  "io/ioutil"
  "os"
)

type TestConfig struct {
  // 实验重复次数
  IterationCount int `json:"iterationCount"`
  // 服务器监听地址
  ServerAddr string `json:"serverAddr"`
  // 服务器监听端口号
  ServerPort string `json:"serverPort"`
  // 总发送数据量
  DataSize int `json:"dataSize"`
  // 缓冲区大小
  BufferSize int `json:"bufferSize"`
  // stream 数目
  NumberOfStreams int `json:"numberOfStreams"`
  // 每个 packet 中最多可以复用的 stream 数目
  StreamMultiplexLimit int `json:"streamMultiplexLimit"`
  // 实验组数
  GroupNumber int `json:"groupNumber"`
}

var config TestConfig

func LoadConfigurationFile(filePath string) {
  // 打开 json 文件
  jsonFile, err := os.Open(filePath)
  // 错误处理
  defer jsonFile.Close()
  if err != nil {
    fmt.Printf("Error in loading configuration file, err: %s\n", err.Error())
    return
  }
  fmt.Printf("TestConfig file loaded, file path = %s\n", filePath)
  // 以字节流形式读取 json 文件
  byteValue, _ := ioutil.ReadAll(jsonFile)
  // 按照给定模型解析 json 对象字节流
  err = json.Unmarshal(byteValue, &config)
  if err != nil {
    fmt.Printf("Error in parsing json configuration file, err: %s\n", err.Error())
    return
  }

  // 调整一些变量的单位
  config.DataSize *= MB   // 调整单位为 MB
  config.BufferSize *= MB // 调整单位为 MB

  fmt.Println("JSON TestConfig File Loaded!")
  fmt.Printf("param <IterationCount> = %d\n", config.IterationCount)
  fmt.Printf("param <ServerAddr> = %s\n", config.ServerAddr)
  fmt.Printf("param <ServerPort> = %s\n", config.ServerPort)
  fmt.Printf("param <DataSize> = %d\n", config.DataSize)
  fmt.Printf("param <BufferSize> = %d\n", config.BufferSize)
  fmt.Printf("param <NumberOfStreams> = %d\n", config.NumberOfStreams)
  fmt.Printf("param <StreamMultiplexLimit> = %d\n", config.StreamMultiplexLimit)
  fmt.Printf("param <GroupNumber> = %d\n", config.GroupNumber)
}

// 让全局都可以共享到这个配置文件
func GetConfig() *TestConfig {
  return &config
}
