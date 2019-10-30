package quic

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stormlin/qperf/quic-go/core/ackhandler"

	"github.com/stormlin/qperf/quic-go/core/flowcontrol"
	"github.com/stormlin/qperf/quic-go/core/protocol"
	"github.com/stormlin/qperf/quic-go/core/utils"
	"github.com/stormlin/qperf/quic-go/core/wire"
)

type sendStreamI interface {
	SendStream
	handleStopSendingFrame(*wire.StopSendingFrame)
	hasData() bool
	popStreamFrame(maxBytes protocol.ByteCount) (*ackhandler.Frame, bool)
	closeForShutdown(error)
	handleMaxStreamDataFrame(*wire.MaxStreamDataFrame)
}

type sendStream struct {
	mutex sync.Mutex

	numOutstandingFrames int64
	retransmissionQueue  []*wire.StreamFrame

	ctx       context.Context
	ctxCancel context.CancelFunc

	streamID protocol.StreamID
	sender   streamSender

	writeOffset protocol.ByteCount

	cancelWriteErr      error
	closeForShutdownErr error

	closedForShutdown bool // set when CloseForShutdown() is called
	finishedWriting   bool // set once Close() is called
	canceledWrite     bool // set when CancelWrite() is called, or a STOP_SENDING frame is received
	finSent           bool // set when a STREAM_FRAME with FIN bit has been sent
	completed         bool // set when this stream has been reported to the streamSender as completed

	dataForWriting []byte // 实质上是发送缓冲区

	writeChan chan struct{}
	deadline  time.Time

	flowController flowcontrol.StreamFlowController

	version protocol.VersionNumber
}

var _ SendStream = &sendStream{}
var _ sendStreamI = &sendStream{}

func newSendStream(
	streamID protocol.StreamID,
	sender streamSender,
	flowController flowcontrol.StreamFlowController,
	version protocol.VersionNumber,
) *sendStream {
	s := &sendStream{
		streamID:       streamID,
		sender:         sender,
		flowController: flowController,
		writeChan:      make(chan struct{}, 1),
		version:        version,
	}
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())
	return s
}

func (s *sendStream) StreamID() protocol.StreamID {
	return s.streamID // same for receiveStream and sendStream
}

func (s *sendStream) Write(p []byte) (int, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// 此 stream 已经被关闭
	if s.finishedWriting {
		return 0, fmt.Errorf("write on closed stream %d", s.streamID)
	}
	if s.canceledWrite {
		return 0, s.cancelWriteErr
	}
	if s.closeForShutdownErr != nil {
		return 0, s.closeForShutdownErr
	}
	if !s.deadline.IsZero() && !time.Now().Before(s.deadline) {
		return 0, errDeadline
	}
	// 没有数据可供写入，直接返回 0 写入长度，并不会返回错误
	if len(p) == 0 {
		return 0, nil
	}

	// 初始状态下，需要写入的数据块等于拿到的缓存块
	s.dataForWriting = p

	// 一些临时变量
	var (
		deadlineTimer *utils.Timer
		// 已经发出的数据量
		bytesWritten   int
		notifiedSender bool
	)

	// 循环写入缓冲区的数据知道写完为止
	for {
		// 已经发出去的数据量，初始值是 0
		bytesWritten = len(p) - len(s.dataForWriting)

		/* 根据本次操作是否有 deadline 来进行操作 */
		deadline := s.deadline
		if !deadline.IsZero() {
			// 存在发送 deadline
			if !time.Now().Before(deadline) {
				// 如果当前时间还在 deadline 之前，则 write 操作不会发送任何数据
				// 即把发送数据的时间窗口让给其他需要被发送的数据
				s.dataForWriting = nil // 清空发送缓冲
				return bytesWritten, errDeadline
			}

			// 没有 deadlineTimer 就创建一个
			if deadlineTimer == nil {
				deadlineTimer = utils.NewTimer()
			}
			deadlineTimer.Reset(deadline)
		}

		// 已经写完所有数据等需要退出此循环的情况
		if s.dataForWriting == nil || s.canceledWrite || s.closedForShutdown {
			break
		}

		s.mutex.Unlock()
		if !notifiedSender {
			s.sender.onHasStreamData(s.streamID) // must be called without holding the mutex
			notifiedSender = true
		}
		if deadline.IsZero() {
			<-s.writeChan
		} else {
			select {
			case <-s.writeChan:
			case <-deadlineTimer.Chan():
				deadlineTimer.SetRead()
			}
		}
		s.mutex.Lock()
	}

	if s.closeForShutdownErr != nil {
		return bytesWritten, s.closeForShutdownErr
	} else if s.cancelWriteErr != nil {
		return bytesWritten, s.cancelWriteErr
	}
	return bytesWritten, nil
}

// popStreamFrame returns the next STREAM frame that is supposed to be sent on this stream
// maxBytes is the maximum length this frame (including frame header) will have.
func (s *sendStream) popStreamFrame(maxBytes protocol.ByteCount) (*ackhandler.Frame, bool /* has more data to send */) {
	s.mutex.Lock()
	f, hasMoreData := s.popNewOrRetransmittedStreamFrame(maxBytes)
	if f != nil {
		s.numOutstandingFrames++
	}
	s.mutex.Unlock()

	if f == nil {
		return nil, hasMoreData
	}
	return &ackhandler.Frame{Frame: f, OnLost: s.queueRetransmission, OnAcked: s.frameAcked}, hasMoreData
}

func (s *sendStream) popNewOrRetransmittedStreamFrame(maxBytes protocol.ByteCount) (*wire.StreamFrame, bool /* has more data to send */) {
	/* 处理需要重传的数据帧 */
	if len(s.retransmissionQueue) > 0 {
		f, hasMoreRetransmissions := s.maybeGetRetransmission(maxBytes)
		if f != nil || hasMoreRetransmissions {
			if f == nil {
				return nil, true
			}
			// We always claim that we have more data to send.
			// This might be incorrect, in which case there'll be a spurious call to popStreamFrame in the future.
			return f, true
		}
	}

	/* 创建一个新的 STREAM_FRAME */
	f := wire.GetStreamFrame()
	f.FinBit = false
	f.StreamID = s.streamID
	f.Offset = s.writeOffset
	f.DataLenPresent = true
	f.Data = f.Data[:0]

	hasMoreData := s.popNewStreamFrame(f, maxBytes)

	if len(f.Data) == 0 && !f.FinBit {
		f.PutBack()
		return nil, hasMoreData
	}
	return f, hasMoreData
}

func (s *sendStream) popNewStreamFrame(f *wire.StreamFrame, maxBytes protocol.ByteCount) bool {
	if s.canceledWrite || s.closeForShutdownErr != nil {
		return false
	}

	maxDataLen := f.MaxDataLen(maxBytes, s.version)
	if maxDataLen == 0 { // a STREAM frame must have at least one byte of data
		// STREAM_FRAME 必须携带应用层数据
		return s.dataForWriting != nil
	}
	s.getDataForWriting(f, maxDataLen)
	if len(f.Data) == 0 && !f.FinBit {
		// this can happen if:
		// - popStreamFrame is called but there's no data for writing
		// - there's data for writing, but the stream is stream-level flow control blocked
		// - there's data for writing, but the stream is connection-level flow control blocked
		// 也就是没有数据，或者被流量控制阻塞了
		if s.dataForWriting == nil {
			return false
		}
		if isBlocked, offset := s.flowController.IsNewlyBlocked(); isBlocked {
			s.sender.queueControlFrame(&wire.StreamDataBlockedFrame{
				StreamID:  s.streamID,
				DataLimit: offset,
			})
			return false
		}
		return true
	}
	if f.FinBit {
		s.finSent = true
	}
	return s.dataForWriting != nil
}

func (s *sendStream) maybeGetRetransmission(maxBytes protocol.ByteCount) (*wire.StreamFrame, bool /* has more retransmissions */) {
	f := s.retransmissionQueue[0]
	newFrame, needsSplit := f.MaybeSplitOffFrame(maxBytes, s.version)
	if needsSplit {
		return newFrame, true
	}
	s.retransmissionQueue = s.retransmissionQueue[1:]
	return f, len(s.retransmissionQueue) > 0
}

func (s *sendStream) hasData() bool {
	s.mutex.Lock()
	hasData := len(s.dataForWriting) > 0
	s.mutex.Unlock()
	return hasData
}

// 准备本次发送操作所能够发送的数据
func (s *sendStream) getDataForWriting(f *wire.StreamFrame, maxBytes protocol.ByteCount) {
	if s.dataForWriting == nil {
		// 并无数据可供发送
		f.FinBit = s.finishedWriting && !s.finSent
		return
	}

	// 计算当前可供发送的最大字节数
	maxBytes = utils.MinByteCount(maxBytes, s.flowController.SendWindowSize())
	if maxBytes == 0 {
		// 发送窗口并没有足够的空间来容纳待发送的数据
		return
	}

	if protocol.ByteCount(len(s.dataForWriting)) > maxBytes {
		// 需要发送的数据大于可供发送的最大字节数，则需要分成多次发送
		f.Data = f.Data[:maxBytes]
		// 把能够发出去的数据复制到帧的数据部分
		copy(f.Data, s.dataForWriting)
		// 余下的数据待下次发送
		s.dataForWriting = s.dataForWriting[maxBytes:]
	} else {
		// 一次性发送全部数据
		f.Data = f.Data[:len(s.dataForWriting)]
		copy(f.Data, s.dataForWriting)
		s.dataForWriting = nil
		s.signalWrite()
	}
	s.writeOffset += f.DataLen()
	s.flowController.AddBytesSent(f.DataLen())
	f.FinBit = s.finishedWriting && s.dataForWriting == nil && !s.finSent
}

func (s *sendStream) frameAcked(f wire.Frame) {
	f.(*wire.StreamFrame).PutBack()

	s.mutex.Lock()
	s.numOutstandingFrames--
	if s.numOutstandingFrames < 0 {
		panic("numOutStandingFrames negative")
	}
	newlyCompleted := s.isNewlyCompleted()
	s.mutex.Unlock()

	if newlyCompleted {
		s.sender.onStreamCompleted(s.streamID)
	}
}

func (s *sendStream) isNewlyCompleted() bool {
	completed := (s.finSent || s.canceledWrite) && s.numOutstandingFrames == 0 && len(s.retransmissionQueue) == 0
	if completed && !s.completed {
		s.completed = true
		return true
	}
	return false
}

// 把需要重传的包放入发送队列
func (s *sendStream) queueRetransmission(f wire.Frame) {
	sf := f.(*wire.StreamFrame)
	sf.DataLenPresent = true
	s.mutex.Lock()
	// 获得重传队列的锁之后向其中放入需要重传的帧
	s.retransmissionQueue = append(s.retransmissionQueue, sf)
	s.numOutstandingFrames--
	if s.numOutstandingFrames < 0 {
		panic("numOutStandingFrames negative")
	}
	s.mutex.Unlock()

	s.sender.onHasStreamData(s.streamID)
}

// 关闭此 send_stream
func (s *sendStream) Close() error {
	s.mutex.Lock()
	if s.canceledWrite {
		s.mutex.Unlock()
		return fmt.Errorf("Close called for canceled stream %d", s.streamID)
	}
	s.finishedWriting = true
	s.mutex.Unlock()

	s.sender.onHasStreamData(s.streamID) // need to send the FIN, must be called without holding the mutex
	s.ctxCancel()
	return nil
}

func (s *sendStream) CancelWrite(errorCode protocol.ApplicationErrorCode) {
	s.cancelWriteImpl(errorCode, fmt.Errorf("Write on stream %d canceled with error code %d", s.streamID, errorCode))

}

// must be called after locking the mutex
func (s *sendStream) cancelWriteImpl(errorCode protocol.ApplicationErrorCode, writeErr error) {
	s.mutex.Lock()
	if s.canceledWrite {
		s.mutex.Unlock()
		return
	}
	s.canceledWrite = true
	s.cancelWriteErr = writeErr
	newlyCompleted := s.isNewlyCompleted()
	s.mutex.Unlock()

	s.signalWrite()
	s.sender.queueControlFrame(&wire.ResetStreamFrame{
		StreamID:   s.streamID,
		ByteOffset: s.writeOffset,
		ErrorCode:  errorCode,
	})
	if newlyCompleted {
		s.sender.onStreamCompleted(s.streamID)
	}
	s.ctxCancel()
}

func (s *sendStream) handleMaxStreamDataFrame(frame *wire.MaxStreamDataFrame) {
	s.mutex.Lock()
	hasStreamData := s.dataForWriting != nil
	s.mutex.Unlock()

	s.flowController.UpdateSendWindow(frame.ByteOffset)
	if hasStreamData {
		s.sender.onHasStreamData(s.streamID)
	}
}

func (s *sendStream) handleStopSendingFrame(frame *wire.StopSendingFrame) {
	writeErr := streamCanceledError{
		errorCode: frame.ErrorCode,
		error:     fmt.Errorf("stream %d was reset with error code %d", s.streamID, frame.ErrorCode),
	}
	s.cancelWriteImpl(frame.ErrorCode, writeErr)
}

func (s *sendStream) Context() context.Context {
	return s.ctx
}

func (s *sendStream) SetWriteDeadline(t time.Time) error {
	s.mutex.Lock()
	s.deadline = t
	s.mutex.Unlock()
	s.signalWrite()
	return nil
}

// CloseForShutdown closes a stream abruptly.
// It makes Write unblock (and return the error) immediately.
// The peer will NOT be informed about this: the stream is closed without sending a FIN or RST.
func (s *sendStream) closeForShutdown(err error) {
	s.mutex.Lock()
	s.closedForShutdown = true
	s.closeForShutdownErr = err
	s.mutex.Unlock()
	s.signalWrite()
	s.ctxCancel()
}

// signalWrite performs a non-blocking send on the writeChan
func (s *sendStream) signalWrite() {
	select {
	case s.writeChan <- struct{}{}:
	default:
	}
}
