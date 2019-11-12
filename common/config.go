package common

import (
  "encoding/json"
  "github.com/google/logger"
  "github.com/jessevdk/go-flags"
  . "github.com/stormlin/qperf/constants"
  . "github.com/stormlin/qperf/datastructures"
  . "github.com/stormlin/qperf/schedulers"
  "io/ioutil"
  "os"
)

// 打印程序用法
func PrintUsage() {
  helpMessage :=
    "Use this utility tool to replay http request with quic. This tool will " +
      "not consider the semantics of http. It only considers the data size, " +
      "priority, and delay of http request. Performance report mainly " +
      "includes the sequence of request replay, and the timings (enqueued_at, " +
      "queue_for, start_at, finished_at, and transmission_time).\n" +
      "We hope it useful to design a better transport-layer mechanism for " +
      "http3.\n" +
      "Usage:\n" +
      "  at client side: ./main -c -q qconfig.json -w wconfig.json" +
      "  at server side: ./main -s -q qconfig.json -w wconfig.json"
  logger.Info(helpMessage)
}

type QConfig struct {
  // 实验重复次数
  IterationCount int `json:"iterationCount"`
  // 每个 packet 中最多可以复用的 stream 数目
  StreamMultiplexLimit int `json:"streamMultiplexLimit"`
  // 实验组数
  GroupNumber int `json:"groupNumber"`
}

type WConfig struct {
  RootRequestUrl  string          `json:"root_request_url"`
  FilteredEntries []FilteredEntry `json:"filtered_entries"`
}

type FilteredEntry struct {
  ResourceId    int     `json:"resource_id"`
  RequestUrl    string  `json:"request_url"`
  ResponseSize  int     `json:"response_size"`
  RemainingSize int     `json:"remaining_size"`
  ResourceType  string  `json:"resource_type"`
  TTFB          float32 `json:"ttfb"`
  Dependencies  []int   `json:"dependencies"`
  Hostname      string  `json:"hostname"`
  Method        string  `json:"method"`
  IsStarted     bool    `json:"is_started"`
  ServerDelay   float32 `json:"server_delay"`

  // 时间统计信息，并不会在数据中读取，而是依靠在运行时获取
  EnqueuedAt       int64
  StartedAt        int64
  FinishedAt       int64
  QueuedFor        int64
  TransmissionTime int64
}

// 命令行选项
type Options struct {
  // 运行于客户端模式
  Client bool `short:"c" long:"client" description:"run as client"`
  // 运行于服务器模式
  Server bool `short:"s" long:"server" description:"run as server"`
  // QPerf 的配置文件路径
  QConfig string `short:"q" long:"qconfig" description:"load qconfig configuration file"`
  // 回放请求的配置文件
  WConfig string `short:"w" long:"wconfig" description:"load wconfig configuration file"`
  // 冗长输出
  Verbose bool `short:"v" long:"verbose" description:"enable verbose output"`
  // 服务器的 IP 地址与端口号
  Address string `short:"a" long:"address" description:"address for server"`
  Port    string `short:"p" long:"port" description:"port for server"`
}

// 全部可调的程序配置项
type ProgramConfig struct {
  // 公共配置项
  Verbose        bool
  Address        string
  Port           string
  IterationCount int
  RootRequestUrl string

  // 控制块，保存各 request 的信息
  ControlBlockSlice *StreamControlBlockSlice
  // 默认使用 chrome dynamic fcfs 调度器
  Scheduler *DynamicFcfsScheduler
  // 用来发送 stream 完成信息
  SignalChan chan *StreamControlBlock

  // 关于 quic-go 的配置项
  StreamMultiplexLimit int // 每个 QUIC Packet 包能复用的最大 stream 数目

  // client 模式下的配置项
  GroupNumber int // 当前实验是第几组，用于输出 JSON 测试文件
}

// 全局共享的配置文件
var programConfig ProgramConfig

// 解析命令行选项
func ParseOptions() (*Options, error) {
  var options Options
  _, err := flags.ParseArgs(&options, os.Args)
  if err != nil {
    return nil, err
  }
  return &options, nil
}

func readConfigFile(filePath string) (*[]byte, error) {
  // 打开文件
  jsonFile, err := os.Open(filePath)
  if err != nil {
    return nil, err
  }
  defer jsonFile.Close()

  // 以字节流形式读取 json 文件
  var byteSlice []byte
  if byteSlice, err = ioutil.ReadAll(jsonFile); err != nil {
    return nil, err
  }

  return &byteSlice, nil
}

func ParseQConfigFile(filePath string) *QConfig {
  // 按照给定模型解析 json 对象字节流
  byteSlice, err := readConfigFile(filePath)
  if err != nil {
    logger.Errorf(
      "Error in reading QConfig file, error: \"%s\"\n", err.Error())
    return nil
  }
  if byteSlice == nil {
    logger.Errorf("Error in reading QConfig file, error: \"%s\"\n",
      "received nil byte slice")
    return nil
  }

  qConfig := QConfig{}
  if err = json.Unmarshal(*byteSlice, &qConfig); err != nil {
    logger.Errorf("Error in parsing QConfig file, error: \"%s\"\n",
      err.Error())
    return nil
  }
  return &qConfig
}

func ParseWConfigFile(filePath string) *WConfig {
  // 按照给定模型解析 json 对象字节流
  byteSlice, err := readConfigFile(filePath)
  if err != nil {
    logger.Errorf(
      "Error in reading WConfig file, error: \"%s\"\n", err.Error())
    return nil
  }
  if byteSlice == nil {
    logger.Errorf("Error in reading WConfig file, error: \"%s\"\n",
      "received nil byte slice")
    return nil
  }

  wConfig := WConfig{}
  if err = json.Unmarshal(*byteSlice, &wConfig); err != nil {
    logger.Errorf("Error in parsing WConfig file, error: \"%s\"\n",
      err.Error())
    return nil
  }
  return &wConfig
}

type StreamFinishSignalChan struct {
}

// 在初始化阶段创建程序配置项对象
func BuildProgramConfig(options *Options) *ProgramConfig {
  qConfig := ParseQConfigFile(options.QConfig)
  if qConfig == nil {
    logger.Errorf("Error in loading QConfig file, err: \"%s\"\n")
    return nil
  }
  q := *qConfig

  wConfig := ParseWConfigFile(options.WConfig)
  if wConfig == nil {
    logger.Errorf("Error in loading WConfig file, err: \"%s\"\n")
    return nil
  }
  w := *wConfig

  programConfig.Verbose = options.Verbose
  programConfig.Address = options.Address
  programConfig.Port = options.Port
  programConfig.IterationCount = q.IterationCount
  programConfig.RootRequestUrl = w.RootRequestUrl
  programConfig.ControlBlockSlice =
    BuildStreamControlBlockSlice(&w.FilteredEntries)
  programConfig.StreamMultiplexLimit = q.StreamMultiplexLimit
  programConfig.GroupNumber = q.GroupNumber
  programConfig.Scheduler = NewDynamicFcfsScheduler(
    getFirstImageResourceId(programConfig.ControlBlockSlice),
    len(programConfig.ControlBlockSlice.BlockSlice))
  programConfig.Scheduler.FirstImageResourceId = getFirstImageResourceId(
    programConfig.ControlBlockSlice)
  programConfig.SignalChan = make(chan *StreamControlBlock)

  return &programConfig
}

// 在程序初始化之后提供获取配置对象的入口
func GetProgramConfig() *ProgramConfig {
  return &programConfig
}

// 获取第一张图片的 resource id，-1 表示此请求不含图片
func getFirstImageResourceId(slice *StreamControlBlockSlice) uint16 {
  for _, block := range slice.BlockSlice {
    if block.ResourceType == IMAGE {
      return block.ResourceId
    }
  }
  return 0
}
