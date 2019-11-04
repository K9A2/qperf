package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"

	quic "github.com/stormlin/qperf/quic-go"
)

const addr = "localhost:4242"

const message = "fuck"

// We start a server echoing data on the first stream the client opens,
// then connect with a client, send the message, and wait for its receipt.
// 在客户端发起的首个流回显信息
func main() {
	go func() {
		// 用 goroutine 起线程
		log.Fatal(echoServer())
	}()

	// 在主线程起服务器
	err := clientMain()
	if err != nil {
		panic(err)
	}
}

// Start a server that echos all data on the first stream opened by the client
// 服务器主线程
func echoServer() error {
	// 添加端口监听, 利用生成的 TLS 数据建立连接
	listener, err := quic.ListenAddr(addr, generateTLSConfig(), nil)
	if err != nil {
		return err
	}

	// 接受外界请求, connection 级
	sess, err := listener.Accept(context.Background())
	if err != nil {
		return err
	}

	// 接受来到的 stream
	stream, err := sess.AcceptStream(context.Background())
	if err != nil {
		panic(err)
	}

	// Echo through the loggingWriter
	// 在输出客户端收到的内容
	//_, err = io.Copy(loggingWriter{stream}, stream)
	buf := make([]byte, len(message))
	_, err = io.ReadFull(stream, buf)
	if err != nil {
		return err
	}
	fmt.Printf("Server: Got '%s'\n", buf)
	_, err = stream.Write([]byte("damn"))
	fmt.Printf("Server response: '%s'\n", "damn")

	return err
}

// 客户端主线程
func clientMain() error {
	// 默认 TLS 加密文件
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"quic-echo-example"},
	}

	// 与服务器建立连接
	session, err := quic.DialAddr(addr, tlsConf, nil)
	if err != nil {
		return err
	}

	// 打开 Stream
	stream, err := session.OpenStreamSync(context.Background())
	if err != nil {
		return err
	}

	// 发送假数据 foobar
	_, err = stream.Write([]byte(message))
	if err != nil {
		return err
	}
	fmt.Printf("Client sent: '%s'\n", message)

	// 读取收到的回显数据
	//buf := make([]byte, 128)
	//_, err = io.ReadFull(stream, buf)
	//fmt.Println("debug")
	//if err != nil {
	//	fmt.Println("error")
	//	return err
	//}
	//fmt.Printf("Client: Got '%s'\n", buf)

	return nil
}

// A wrapper for io.Writer that also logs the message.
// 日志组件
type loggingWriter struct{ io.Writer }

// 写日志并回显
func (w loggingWriter) Write(b []byte) (int, error) {
	fmt.Printf("Server: Got '%s'\n", string(b))
	return w.Writer.Write(b)
}

// Setup a bare-bones TLS config for the server
// 生成模拟 TLS 证书
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
