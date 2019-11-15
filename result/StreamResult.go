package result

import (
  "fmt"
  "time"
)

type StreamResult struct {
  StreamID int64
  // stream 的开始时间
  Start time.Time
  // stream 的终止时间
  End time.Time
  // 总测试时间
  Total float64
}

func (r StreamResult) String() string {
  return fmt.Sprintf("streamID = %d, starts at = %s, ends at = %s\n, last for %.3f seconds\n",
    r.StreamID, r.Start.Format(time.UnixDate), r.End.Format(time.UnixDate), r.End.Sub(r.Start).Seconds())
}
