package datastructures

const (
  // 服务器集群的最小端口号
  MinServerPort = 8080
)

type ServerControlBlock struct {
  Domain       string
  Port         string
  RequestCount int
}

func NewServerControlBlock(domain string, port string) *ServerControlBlock {
  return &ServerControlBlock{
    Domain:       domain,
    Port:         port,
    RequestCount: 0,
  }
}
