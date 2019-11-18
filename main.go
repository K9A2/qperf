package main

import (
  "github.com/google/logger"
  . "github.com/stormlin/qperf/common"
  "net/url"
  "os"
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
