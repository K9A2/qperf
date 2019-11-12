package constants

const (
  // 数据块大小定义
  B  = 1
  KB = B * 1e3
  MB = KB * 1e3
  GB = MB * 1e3

  // 两次测试之间的等待时间
  TimeToNextEvaluation = 5

  // 资源类型常量
  DOCUMENT   = "document"
  STYLESHEET = "stylesheet"
  SCRIPT     = "script"
  IMAGE      = "image"
  XHR        = "xhr"
  OTHER      = "other"
)
