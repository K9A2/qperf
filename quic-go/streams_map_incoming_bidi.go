// This file was automatically generated by genny.
// Any changes will be lost if this file is regenerated.
// see https://github.com/cheekybits/genny

package quic

import (
	"context"
	"sync"

	"github.com/stormlin/qperf/quic-go/core/protocol"
	"github.com/stormlin/qperf/quic-go/core/wire"
)

type incomingBidiStreamsMap struct {
	mutex         sync.RWMutex
	newStreamChan chan struct{}

	streams map[protocol.StreamNum]streamI
	// When a stream is deleted before it was accepted, we can't delete it immediately.
	// We need to wait until the application accepts it, and delete it immediately then.
	streamsToDelete map[protocol.StreamNum]struct{} // used as a set

	nextStreamToAccept protocol.StreamNum // the next stream that will be returned by AcceptStream()
	nextStreamToOpen   protocol.StreamNum // the highest stream that the peer openend
	maxStream          protocol.StreamNum // the highest stream that the peer is allowed to open
	maxNumStreams      uint64             // maximum number of streams

	newStream        func(protocol.StreamNum) streamI
	queueMaxStreamID func(*wire.MaxStreamsFrame)
	// streamNumToID    func(protocol.StreamNum) protocol.StreamID // only used for generating errors

	closeErr error
}

func newIncomingBidiStreamsMap(
	newStream func(protocol.StreamNum) streamI,
	maxStreams uint64,
	queueControlFrame func(wire.Frame),
) *incomingBidiStreamsMap {
	return &incomingBidiStreamsMap{
		newStreamChan:      make(chan struct{}),
		streams:            make(map[protocol.StreamNum]streamI),
		streamsToDelete:    make(map[protocol.StreamNum]struct{}),
		maxStream:          protocol.StreamNum(maxStreams),
		maxNumStreams:      maxStreams,
		newStream:          newStream,
		nextStreamToOpen:   1,
		nextStreamToAccept: 1,
		queueMaxStreamID:   func(f *wire.MaxStreamsFrame) { queueControlFrame(f) },
	}
}

func (m *incomingBidiStreamsMap) AcceptStream(ctx context.Context) (streamI, error) {
	m.mutex.Lock()

	var num protocol.StreamNum
	var str streamI
	for {
		num = m.nextStreamToAccept
		if m.closeErr != nil {
			m.mutex.Unlock()
			return nil, m.closeErr
		}
		var ok bool
		str, ok = m.streams[num]
		if ok {
			break
		}
		m.mutex.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.newStreamChan:
		}
		m.mutex.Lock()
	}
	m.nextStreamToAccept++
	// If this stream was completed before being accepted, we can delete it now.
	if _, ok := m.streamsToDelete[num]; ok {
		delete(m.streamsToDelete, num)
		if err := m.deleteStream(num); err != nil {
			m.mutex.Unlock()
			return nil, err
		}
	}
	m.mutex.Unlock()
	return str, nil
}

func (m *incomingBidiStreamsMap) GetOrOpenStream(num protocol.StreamNum) (streamI, error) {
	m.mutex.RLock()
	if num > m.maxStream {
		m.mutex.RUnlock()
		return nil, streamError{
			message: "peer tried to open stream %d (current limit: %d)",
			nums:    []protocol.StreamNum{num, m.maxStream},
		}
	}
	// if the num is smaller than the highest we accepted
	// * this stream exists in the map, and we can return it, or
	// * this stream was already closed, then we can return the nil
	if num < m.nextStreamToOpen {
		var s streamI
		// If the stream was already queued for deletion, and is just waiting to be accepted, don't return it.
		if _, ok := m.streamsToDelete[num]; !ok {
			s = m.streams[num]
		}
		m.mutex.RUnlock()
		return s, nil
	}
	m.mutex.RUnlock()

	m.mutex.Lock()
	// no need to check the two error conditions from above again
	// * maxStream can only increase, so if the id was valid before, it definitely is valid now
	// * highestStream is only modified by this function
	for newNum := m.nextStreamToOpen; newNum <= num; newNum++ {
		m.streams[newNum] = m.newStream(newNum)
		select {
		case m.newStreamChan <- struct{}{}:
		default:
		}
	}
	m.nextStreamToOpen = num + 1
	s := m.streams[num]
	m.mutex.Unlock()
	return s, nil
}

func (m *incomingBidiStreamsMap) DeleteStream(num protocol.StreamNum) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return m.deleteStream(num)
}

func (m *incomingBidiStreamsMap) deleteStream(num protocol.StreamNum) error {
	if _, ok := m.streams[num]; !ok {
		return streamError{
			message: "Tried to delete unknown stream %d",
			nums:    []protocol.StreamNum{num},
		}
	}

	// Don't delete this stream yet, if it was not yet accepted.
	// Just save it to streamsToDelete map, to make sure it is deleted as soon as it gets accepted.
	if num >= m.nextStreamToAccept {
		if _, ok := m.streamsToDelete[num]; ok {
			return streamError{
				message: "Tried to delete stream %d multiple times",
				nums:    []protocol.StreamNum{num},
			}
		}
		m.streamsToDelete[num] = struct{}{}
		return nil
	}

	delete(m.streams, num)
	// queue a MAX_STREAM_ID frame, giving the peer the option to open a new stream
	if m.maxNumStreams > uint64(len(m.streams)) {
		numNewStreams := m.maxNumStreams - uint64(len(m.streams))
		m.maxStream = m.nextStreamToOpen + protocol.StreamNum(numNewStreams) - 1
		m.queueMaxStreamID(&wire.MaxStreamsFrame{
			Type:         protocol.StreamTypeBidi,
			MaxStreamNum: m.maxStream,
		})
	}
	return nil
}

func (m *incomingBidiStreamsMap) CloseWithError(err error) {
	m.mutex.Lock()
	m.closeErr = err
	for _, str := range m.streams {
		str.closeForShutdown(err)
	}
	m.mutex.Unlock()
	close(m.newStreamChan)
}
