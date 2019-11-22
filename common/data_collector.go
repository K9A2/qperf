package common

import (
  "github.com/google/logger"
  . "github.com/stormlin/qperf/constants"
  . "github.com/stormlin/qperf/datastructures"
  "sync"
)

// 单独某一类资源的时间统计数据结构体
type streamStatistics struct {
  RequestCount     int64 // 请求数目
  ResponseSize     int64 // 响应大小
  EnqueuedAt       int64 // 入队时间
  QueuedFor        int64 // 排队时间
  StartAt          int64 // 开始时间
  FinishedAt       int64 // 结束时间
  TransmissionTime int64 // 传输时间
}

// 全部资源的时间统计数据结构体
type timingReport struct {
  DocumentReport   *streamStatistics
  StylesheetReport *streamStatistics
  XhrReport        *streamStatistics
  ScriptReport     *streamStatistics
  ImageReport      *streamStatistics
  OtherReport      *streamStatistics
}

// 用于在 stream 结束之后统计相关数据的工具类
type dataCollector struct {
  mutex           *sync.Mutex
  finishedStreams *StreamControlBlockSlice
}

// 构造方法
func DataCollector(capacity int) *dataCollector {
  return &dataCollector{
    mutex:           &sync.Mutex{},
    finishedStreams: GetNewControlBlockSlice(capacity),
  }
}

// 向数据收集器中添加已经完成的 stream
func (collector *dataCollector) OnConnectionFinish(
  slice *StreamControlBlockSlice) {
  collector.finishedStreams = slice
}

// 计算指定类别资源的时间统计数据
func (collector *dataCollector) calculateTimingStatistics(
  resourceType string) *streamStatistics {
  defer collector.finishedStreams.Mutex.Unlock()
  collector.finishedStreams.Mutex.Lock()
  var resourceCount int64 // 该类资源数目
  var responseSize int64
  var enqueuedAt int64
  var queuedFor int64
  var startedAt int64
  var finishedAt int64
  var transmissionTime int64
  // 逐个统计
  for _, block := range collector.finishedStreams.BlockSlice {
    if block.ResourceType == resourceType {
      resourceCount += 1
      responseSize += int64(block.ResponseSize)
      enqueuedAt += block.EnqueuedAt
      queuedFor += block.QueuedFor
      startedAt += block.StartedAt
      finishedAt += block.FinishedAt
      transmissionTime += block.TransmissionTime
    }
  }
  // 计算平均值后返回
  return &streamStatistics{
    RequestCount:     resourceCount,
    ResponseSize:     int64(float64(responseSize) / float64(resourceCount)),
    EnqueuedAt:       int64(float64(enqueuedAt) / float64(resourceCount)),
    QueuedFor:        int64(float64(queuedFor) / float64(resourceCount)),
    StartAt:          int64(float64(startedAt) / float64(resourceCount)),
    FinishedAt:       int64(float64(finishedAt) / float64(resourceCount)),
    TransmissionTime: int64(float64(transmissionTime) / float64(resourceCount)),
  }
}

// 获取本次实验的性能数据统计报告，内容为每一类资源的时间统计数据
func (collector *dataCollector) GetTimingReport() *timingReport {
  return &timingReport{
    DocumentReport:   collector.calculateTimingStatistics(DOCUMENT),
    StylesheetReport: collector.calculateTimingStatistics(STYLESHEET),
    XhrReport:        collector.calculateTimingStatistics(XHR),
    ScriptReport:     collector.calculateTimingStatistics(SCRIPT),
    ImageReport:      collector.calculateTimingStatistics(IMAGE),
    OtherReport:      collector.calculateTimingStatistics(OTHER),
  }
}

// 打印测试结果
func PrintTimingReport(timingStatistics *streamStatistics, resourceType string) {
  logger.Infof("resource type: <%s>", resourceType)
  logger.Infof("  RequestCount:     <%d>", timingStatistics.RequestCount)
  logger.Infof("  avg ResponseSize: <%.3f>KB", float64(timingStatistics.ResponseSize)/1000.0)
  logger.Infof("  avg QueuedFor:        <%d>ms", timingStatistics.QueuedFor/1e6)
  logger.Infof("  avg TransmissionTime: <%d>ms", timingStatistics.TransmissionTime/1e6)
}

func (collector dataCollector) PrintReport() {
  report := collector.GetTimingReport()
  PrintTimingReport(report.DocumentReport, DOCUMENT)
  PrintTimingReport(report.StylesheetReport, STYLESHEET)
  PrintTimingReport(report.XhrReport, XHR)
  PrintTimingReport(report.ScriptReport, SCRIPT)
  PrintTimingReport(report.ImageReport, IMAGE)
  PrintTimingReport(report.OtherReport, OTHER)
}
