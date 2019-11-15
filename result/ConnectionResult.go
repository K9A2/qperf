package result

type ConnectionResult struct {
  // 第几次测试
  ConnectionID int
  // 属于本次连接的各 stream 的测试数据
  StreamResultSlice []StreamResult
}
