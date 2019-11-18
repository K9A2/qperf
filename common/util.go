package common

import (
  "crypto/rand"
  "crypto/rsa"
  "crypto/tls"
  "crypto/x509"
  "encoding/pem"
  . "github.com/stormlin/qperf/datastructures"
  "math/big"
  "strconv"
)

// 按照请求日志文件的要求初始化空白控制块
func BuildStreamControlBlockSlice(
  filteredEntries *[]FilteredEntry) *StreamControlBlockSlice {
  controlBlockSlice := GetNewControlBlockSlice(len(*filteredEntries))
  for _, entry := range *filteredEntries {
    block := StreamControlBlock{
      Status:        UNAVAILABLE,
      ResourceId:    uint16(entry.ResourceId),
      RequestUrl:    entry.RequestUrl,
      ResponseSize:  uint32(entry.ResponseSize),
      RemainingSize: uint32(entry.RemainingSize),
      ResourceType:  entry.ResourceType,
      TTFB:          entry.TTFB,
      Dependencies:  entry.Dependencies,
      Domain:        entry.Domain,
      Method:        entry.Method,
      IsStarted:     entry.IsStarted,
      ServerDelay:   entry.ServerDelay,
    }
    // 把此 block 添加到 block slice 中，以便供其他模块使用
    controlBlockSlice.BlockSlice = append(controlBlockSlice.BlockSlice, &block)
  }
  return controlBlockSlice
}

// 只在 server 一侧初始化 response body 以节省内存空间
func MakeResponseBody(block *StreamControlBlock) {
  block.ResponseBody = make([]byte, block.ResponseSize, block.ResponseSize)
  Memset(&block.ResponseBody, uint8(block.ResourceId))
}

// 把一块 byte 数组设为指定值
func Memset(array *[]byte, value byte) {
  a := *array
  if len(a) == 0 {
    return
  }
  a[0] = value
  for bp := 1; bp < len(a); bp *= 2 {
    copy(a[bp:], a[:bp])
  }
}

func ValidateResponse(response *[]byte, responseSize uint32, value uint16) bool {
  if len(*response) <= 0 {
    // 没有接收到任何数据
    return false
  }
  if uint32(len(*response)) != responseSize {
    // 接收到的数据块长度与预计长度不同
    return false
  }

  // 逐个字节检查是否出错
  valueInByte := uint8(value)
  for _, b := range *response {
    if b != valueInByte {
      return false
    }
  }
  return true
}

// 生成 TLS 证书
func GenerateTLSConfig() *tls.Config {
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

func BuildServerControlBlockSlice(
  entries *[]FilteredEntry) *map[string]*ServerControlBlock {
  // 用于在遍历过程中暂存各 server control block
  blockMap := make(map[string]*ServerControlBlock)
  // 下一个子服务器的端口号，以后每一个子服务器的端口号在此基础上加 1
  nextServerPort := MinServerPort
  // 遍历原始数据以按顺序找出所有 domain
  for _, e := range *entries {
    if _, exists := blockMap[e.Domain]; !exists {
      // 首次读到这个 block 相关的信息，需要为其创建 control block 并分配端口号
      newBlock := NewServerControlBlock(e.Domain, strconv.Itoa(nextServerPort))
      newBlock.RequestCount = 1
      blockMap[e.Domain] = newBlock
      nextServerPort += 1
    } else {
      blockMap[e.Domain].RequestCount += 1
    }
  }
  return &blockMap
}
