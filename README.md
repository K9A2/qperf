# QPerf

QPerf 是一个通过模拟发送 Web 请求的方式来测试 HTTP/3 传输层效率的工具。此工具不关心 HTTP/3 的
具体语义，只通过回放 HAR 日志文件中的请求来测试 QUIC 层的传输性能。

## 开发环境

- go version go1.13.3 linux/amd64
- python 2.7/3.6

## 使用方式

client 和 server 双方可以使用同一个配置文件。配置文件中记载了以下两部分内容：

- 从 HAR 中提取的关键部分，以便 QPerf 能够按照真实访问顺序回放请求
- QPerf 的关键参数，以便调整 QPerf 和自带的 quic-go 的某些关键参数

client 一侧的使用方式：./main -c -C config.json  
server 一侧的使用方式：./main -s -C config.json

## QPerf 的工作流程

client 和 server 都需要通过加载配置文件获取记录的回放顺序以及依赖项。server 一侧还需要获取
server_delay，以便能够在经过指定的延迟之后才回复该请求，使其能够模拟真实服务器生成动态页面所需要
的延迟。

- client 发送 root_request 到 server。
- server 经过指定的 server_delay 之后回复 root_request，client 一侧记录收到回复的时间。
  如果启用了 server_push，则 server 会在收到 root_request 之后立刻按照配置的回访顺序传输
  数据。如果 client 请求的资源文件在 server_push 清单中，则 server 会按照配置的清单传输文件，
  覆盖 client 发送的 priority 信息。如果请求的文件不在清单中，则在该资源就绪之后立刻开始传输。
- client 按照 HAR 文件中的顺序发送请求。每有一个 request 被满足就会想 server 发送该请求。
- client 在测试结束之后会把相关的统计数据保存为 JSON 文件，以供后续分析。
  
