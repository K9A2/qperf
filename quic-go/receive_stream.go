package quic

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/stormlin/qperf/quic-go/core/flowcontrol"
	"github.com/stormlin/qperf/quic-go/core/protocol"
	"github.com/stormlin/qperf/quic-go/core/utils"
	"github.com/stormlin/qperf/quic-go/core/wire"
)

type receiveStreamI interface {
	ReceiveStream

	handleStreamFrame(*wire.StreamFrame) error
	handleResetStreamFrame(*wire.ResetStreamFrame) error
	closeForShutdown(error)
	getWindowUpdate() protocol.ByteCount
}

type receiveStream struct {
	mutex sync.Mutex

	streamID protocol.StreamID

	sender streamSender

	frameQueue  *frameSorter
	readOffset  protocol.ByteCount
	finalOffset protocol.ByteCount

	currentFrame       []byte
	currentFrameDone   func()
	currentFrameIsLast bool // is the currentFrame the last frame on this stream
	readPosInFrame     int

	closeForShutdownErr error
	cancelReadErr       error
	resetRemotelyErr    StreamError

	closedForShutdown bool // set when CloseForShutdown() is called
	finRead           bool // set once we read a frame with a FinBit
	canceledRead      bool // set when CancelRead() is called
	resetRemotely     bool // set when HandleResetStreamFrame() is called

	readChan chan struct{}
	deadline time.Time

	flowController flowcontrol.StreamFlowController
	version        protocol.VersionNumber
}

var _ ReceiveStream = &receiveStream{}
var _ receiveStreamI = &receiveStream{}

func newReceiveStream(
	streamID protocol.StreamID,
	sender streamSender,
	flowController flowcontrol.StreamFlowController,
	version protocol.VersionNumber,
) *receiveStream {
	return &receiveStream{
		streamID:       streamID,
		sender:         sender,
		flowController: flowController,
		frameQueue:     newFrameSorter(),
		readChan:       make(chan struct{}, 1),
		finalOffset:    protocol.MaxByteCount,
		version:        version,
	}
}

func (s *receiveStream) StreamID() protocol.StreamID {
	return s.streamID
}

// Read implements io.Reader. It is not thread safe!
// io.Reader 的实现函数
func (s *receiveStream) Read(p []byte) (int, error) {
	s.mutex.Lock()
	// 从 stream 中读取数据
	completed, n, err := s.readImpl(p)
	s.mutex.Unlock()

	if completed {
		s.streamCompleted()
	}
	return n, err
}

// 实际读取数据的函数
func (s *receiveStream) readImpl(p []byte) (bool /*stream completed */, int, error) {
	if s.finRead {
		return false, 0, io.EOF
	}
	if s.canceledRead {
		return false, 0, s.cancelReadErr
	}
	if s.resetRemotely {
		return false, 0, s.resetRemotelyErr
	}
	if s.closedForShutdown {
		return false, 0, s.closeForShutdownErr
	}

	// 本次读出的字节数
	bytesRead := 0
	// 缓冲区还有空位，可以继续向其中填入数据
	for bytesRead < len(p) {
		if s.currentFrame == nil || s.readPosInFrame >= len(s.currentFrame) {
			s.dequeueNextFrame()
		}
		if s.currentFrame == nil && bytesRead > 0 {
			return false, bytesRead, s.closeForShutdownErr
		}

		var deadlineTimer *utils.Timer
		/* 循环到读到数据或者最后一帧然后退出 */
		for {
			// Stop waiting on errors
			if s.closedForShutdown {
				return false, bytesRead, s.closeForShutdownErr
			}
			if s.canceledRead {
				return false, bytesRead, s.cancelReadErr
			}
			if s.resetRemotely {
				return false, bytesRead, s.resetRemotelyErr
			}

			deadline := s.deadline
			if !deadline.IsZero() {
				if !time.Now().Before(deadline) {
					return false, bytesRead, errDeadline
				}
				if deadlineTimer == nil {
					deadlineTimer = utils.NewTimer()
				}
				deadlineTimer.Reset(deadline)
			}

			if s.currentFrame != nil || s.currentFrameIsLast {
				break
			}

			s.mutex.Unlock()
			if deadline.IsZero() {
				// deadline 到期，让读 go 程立刻过来取数据
				<-s.readChan
			} else {
				select {
				case <-s.readChan: // 通知读 go 程来收数据
				case <-deadlineTimer.Chan():
					deadlineTimer.SetRead()
				}
			}
			s.mutex.Lock()
			if s.currentFrame == nil {
				s.dequeueNextFrame()
			}
		}

		if bytesRead > len(p) {
			return false, bytesRead, fmt.Errorf("BUG: bytesRead (%d) > len(p) (%d) in stream.Read", bytesRead, len(p))
		}
		if s.readPosInFrame > len(s.currentFrame) {
			return false, bytesRead, fmt.Errorf("BUG: readPosInFrame (%d) > frame.DataLen (%d) in stream.Read", s.readPosInFrame, len(s.currentFrame))
		}

		s.mutex.Unlock()

		// 把帧数据复制到缓冲区
		m := copy(p[bytesRead:], s.currentFrame[s.readPosInFrame:])
		s.readPosInFrame += m
		bytesRead += m
		s.readOffset += protocol.ByteCount(m)

		s.mutex.Lock()
		// when a RESET_STREAM was received, the was already informed about the final byteOffset for this stream
		if !s.resetRemotely {
			s.flowController.AddBytesRead(protocol.ByteCount(m))
		}

		if s.readPosInFrame >= len(s.currentFrame) && s.currentFrameIsLast {
			s.finRead = true
			return true, bytesRead, io.EOF
		}
	}
	return false, bytesRead, nil
}

func (s *receiveStream) dequeueNextFrame() {
	var offset protocol.ByteCount
	// We're done with the last frame. Release the buffer.
	if s.currentFrameDone != nil {
		s.currentFrameDone()
	}
	offset, s.currentFrame, s.currentFrameDone = s.frameQueue.Pop()
	s.currentFrameIsLast = offset+protocol.ByteCount(len(s.currentFrame)) >= s.finalOffset
	s.readPosInFrame = 0
}

func (s *receiveStream) CancelRead(errorCode protocol.ApplicationErrorCode) {
	s.mutex.Lock()
	completed := s.cancelReadImpl(errorCode)
	s.mutex.Unlock()

	if completed {
		s.streamCompleted()
	}
}

func (s *receiveStream) cancelReadImpl(errorCode protocol.ApplicationErrorCode) bool /* completed */ {
	if s.finRead || s.canceledRead || s.resetRemotely {
		return false
	}
	s.canceledRead = true
	s.cancelReadErr = fmt.Errorf("Read on stream %d canceled with error code %d", s.streamID, errorCode)
	s.signalRead()
	s.sender.queueControlFrame(&wire.StopSendingFrame{
		StreamID:  s.streamID,
		ErrorCode: errorCode,
	})
	// We're done with this stream if the final offset was already received.
	return s.finalOffset != protocol.MaxByteCount
}

func (s *receiveStream) handleStreamFrame(frame *wire.StreamFrame) error {
	s.mutex.Lock()
	completed, err := s.handleStreamFrameImpl(frame)
	s.mutex.Unlock()

	if completed {
		s.streamCompleted()
	}
	return err
}

func (s *receiveStream) handleStreamFrameImpl(frame *wire.StreamFrame) (bool /* completed */, error) {
	maxOffset := frame.Offset + frame.DataLen()
	if err := s.flowController.UpdateHighestReceived(maxOffset, frame.FinBit); err != nil {
		return false, err
	}
	if frame.FinBit {
		s.finalOffset = maxOffset
	}
	if s.canceledRead {
		return frame.FinBit, nil
	}
	if err := s.frameQueue.Push(frame.Data, frame.Offset, frame.PutBack); err != nil {
		return false, err
	}
	s.signalRead()
	return false, nil
}

func (s *receiveStream) handleResetStreamFrame(frame *wire.ResetStreamFrame) error {
	s.mutex.Lock()
	completed, err := s.handleResetStreamFrameImpl(frame)
	s.mutex.Unlock()

	if completed {
		s.streamCompleted()
	}
	return err
}

func (s *receiveStream) handleResetStreamFrameImpl(frame *wire.ResetStreamFrame) (bool /*completed */, error) {
	if s.closedForShutdown {
		return false, nil
	}
	if err := s.flowController.UpdateHighestReceived(frame.ByteOffset, true); err != nil {
		return false, err
	}
	s.finalOffset = frame.ByteOffset

	// ignore duplicate RESET_STREAM frames for this stream (after checking their final offset)
	if s.resetRemotely {
		return false, nil
	}
	s.resetRemotely = true
	s.resetRemotelyErr = streamCanceledError{
		errorCode: frame.ErrorCode,
		error:     fmt.Errorf("stream %d was reset with error code %d", s.streamID, frame.ErrorCode),
	}
	s.signalRead()
	return true, nil
}

func (s *receiveStream) CloseRemote(offset protocol.ByteCount) {
	s.handleStreamFrame(&wire.StreamFrame{FinBit: true, Offset: offset})
}

func (s *receiveStream) SetReadDeadline(t time.Time) error {
	s.mutex.Lock()
	s.deadline = t
	s.mutex.Unlock()
	s.signalRead()
	return nil
}

// CloseForShutdown closes a stream abruptly.
// It makes Read unblock (and return the error) immediately.
// The peer will NOT be informed about this: the stream is closed without sending a FIN or RESET.
func (s *receiveStream) closeForShutdown(err error) {
	s.mutex.Lock()
	s.closedForShutdown = true
	s.closeForShutdownErr = err
	s.mutex.Unlock()
	s.signalRead()
}

func (s *receiveStream) getWindowUpdate() protocol.ByteCount {
	return s.flowController.GetWindowUpdate()
}

func (s *receiveStream) streamCompleted() {
	s.mutex.Lock()
	finRead := s.finRead
	s.mutex.Unlock()

	if !finRead {
		s.flowController.Abandon()
	}
	s.sender.onStreamCompleted(s.streamID)
}

// signalRead performs a non-blocking send on the readChan
func (s *receiveStream) signalRead() {
	select {
	case s.readChan <- struct{}{}:
	default:
	}
}
