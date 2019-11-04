package quic

import "C"
import (
	"sync"

	. "github.com/stormlin/qperf/parameters"

	"github.com/stormlin/qperf/quic-go/core/ackhandler"
	"github.com/stormlin/qperf/quic-go/core/protocol"
	"github.com/stormlin/qperf/quic-go/core/utils"
	"github.com/stormlin/qperf/quic-go/core/wire"
)

type framer interface {
	QueueControlFrame(wire.Frame)
	AppendControlFrames([]ackhandler.Frame, protocol.ByteCount) ([]ackhandler.Frame, protocol.ByteCount)

	AddActiveStream(protocol.StreamID)
	AppendStreamFrames([]ackhandler.Frame, protocol.ByteCount) ([]ackhandler.Frame, protocol.ByteCount)
}

type framerI struct {
	mutex sync.Mutex

	streamGetter streamGetter
	version      protocol.VersionNumber

	// 当前需要发送数据的 stream
	activeStreams map[protocol.StreamID]struct{}
	streamQueue   []protocol.StreamID

	controlFrameMutex sync.Mutex
	controlFrames     []wire.Frame
}

var _ framer = &framerI{}

func newFramer(
	streamGetter streamGetter,
	v protocol.VersionNumber,
) framer {
	return &framerI{
		streamGetter:  streamGetter,
		activeStreams: make(map[protocol.StreamID]struct{}),
		version:       v,
	}
}

func (f *framerI) QueueControlFrame(frame wire.Frame) {
	f.controlFrameMutex.Lock()
	f.controlFrames = append(f.controlFrames, frame)
	f.controlFrameMutex.Unlock()
}

func (f *framerI) AppendControlFrames(frames []ackhandler.Frame, maxLen protocol.ByteCount) ([]ackhandler.Frame, protocol.ByteCount) {
	var length protocol.ByteCount
	f.controlFrameMutex.Lock()
	for len(f.controlFrames) > 0 {
		frame := f.controlFrames[len(f.controlFrames)-1]
		frameLen := frame.Length(f.version)
		if length+frameLen > maxLen {
			break
		}
		frames = append(frames, ackhandler.Frame{Frame: frame})
		length += frameLen
		f.controlFrames = f.controlFrames[:len(f.controlFrames)-1]
	}
	f.controlFrameMutex.Unlock()
	return frames, length
}

// 向 framer 中添加一条活跃的 stream，以便对其上的数据进行 frame 操作
func (f *framerI) AddActiveStream(id protocol.StreamID) {
	f.mutex.Lock()
	// ok = true 时，此 stream 已在活跃队列中
	if _, ok := f.activeStreams[id]; !ok {
		// 把此 stream 添加到活跃队列中
		f.streamQueue = append(f.streamQueue, id)
		f.activeStreams[id] = struct{}{}
	}
	f.mutex.Unlock()
}

// 把需要发送应用数据插入发送队列
func (f *framerI) AppendStreamFrames(frames []ackhandler.Frame, maxLen protocol.ByteCount) ([]ackhandler.Frame, protocol.ByteCount) {
	// 此 QUIC Packet 中所有 frame 的长度
	var length protocol.ByteCount
	// 指向最后一帧的指针
	var lastFrame *ackhandler.Frame

	// 获取全局共享的配置文件
	config := GetConfig()

	f.mutex.Lock()
	// pop STREAM frames, until less than MinStreamFrameSize bytes are left in the packet
	/* 默认尽可能用一帧来填满整个 packet，如果数据不足，则添加来自其他 stream 的数据以填满此 packet */
	numActiveStreams := len(f.streamQueue) // 活跃 stream 数目
	// 每 Packet 可复用的 stream 数目上限
	//streamMultiplexLimit := 5
	// 已复用的 stream 数目
	multiplexedStreams := 0
	for i := 0; i < numActiveStreams && multiplexedStreams < config.StreamMultiplexLimit; i++ {
		if protocol.MinStreamFrameSize+length > maxLen {
			// 无法放下更多帧
			break
		}

		// 选取发送队列中的第一个 stream
		id := f.streamQueue[0]
		// 从发送队列中移除第一个 stream
		f.streamQueue = f.streamQueue[1:]
		// This should never return an error. Better check it anyway.
		// The stream will only be in the streamQueue, if it enqueued itself there.
		str, err := f.streamGetter.GetOrOpenSendStream(id)
		// The stream can be nil if it completed after it said it had data.
		if str == nil || err != nil {
			delete(f.activeStreams, id)
			continue
		}

		// 还可以放入的字节数
		remainingLen := maxLen - length
		// 每轮每条 stream 最多可以放入的字节数
		var maxLengthPerRound int32
		if numActiveStreams > config.StreamMultiplexLimit {
			// 活跃 stream 数比复用 stream 数上限还大时，只能按照复用上限来计算字节数
			maxLengthPerRound = int32(float32(maxLen) / float32(config.StreamMultiplexLimit))
		} else {
			// 按照实际活跃 stream 数计算字节数
			maxLengthPerRound = int32(float32(maxLen) / float32(numActiveStreams))
		}
		if int32(remainingLen) > maxLengthPerRound {
			remainingLen = protocol.ByteCount(maxLengthPerRound)
		}
		// stream 复用数加 1
		multiplexedStreams += 1

		// For the last STREAM frame, we'll remove the DataLen field later.
		// Therefore, we can pretend to have more bytes available when popping
		// the STREAM frame (which will always have the DataLen set).
		remainingLen += utils.VarIntLen(uint64(remainingLen))
		frame, hasMoreData := str.popStreamFrame(remainingLen)
		if hasMoreData { // put the stream back in the queue (at the end)
			// 取出一帧以后仍有数据可供发送，则放回发送队列的最后一位，等待下一次轮训
			f.streamQueue = append(f.streamQueue, id)
		} else { // no more data to send. Stream is not active any more
			// 没有数据则从活跃队列中删除此 stream
			delete(f.activeStreams, id)
		}
		// The frame can be nil
		// * if the receiveStream was canceled after it said it had data
		// * the remaining size doesn't allow us to add another STREAM frame
		if frame == nil {
			continue
		}
		// 把获得的 frame 添加到发送队列
		frames = append(frames, *frame)
		// 添加此 frame 的实际长度
		length += frame.Length(f.version)
		// 调整最后一帧指针
		lastFrame = frame
	}

	f.mutex.Unlock()

	if lastFrame != nil {
		lastFrameLen := lastFrame.Length(f.version)
		// account for the smaller size of the last STREAM frame
		lastFrame.Frame.(*wire.StreamFrame).DataLenPresent = false
		length += lastFrame.Length(f.version) - lastFrameLen
	}

	return frames, length
}
