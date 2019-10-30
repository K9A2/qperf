package congestion

import "github.com/stormlin/qperf/quic-go/core/protocol"

type connectionStats struct {
	slowstartPacketsLost protocol.PacketNumber
	slowstartBytesLost   protocol.ByteCount
}
