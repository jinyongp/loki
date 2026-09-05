// Package cdp implements Chromium's private NUL-delimited debugging pipe.
package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const MaxMessage = 32 * 1024 * 1024

type Event struct {
	Method    string
	Params    json.RawMessage
	SessionID string
}
type protocolError struct {
	Code    int
	Message string
}

func (e *protocolError) Error() string { return fmt.Sprintf("CDP %d: %s", e.Code, e.Message) }

type response struct {
	ID        int64
	Result    json.RawMessage
	Error     *protocolError
	Method    string
	Params    json.RawMessage
	SessionID string `json:"sessionId"`
}
type writeJob struct {
	ctx  context.Context
	data []byte
}
type Client struct {
	reader  io.ReadCloser
	writer  io.WriteCloser
	mu      sync.Mutex
	next    int64
	pending map[int64]chan response
	closed  chan struct{}
	queue   chan writeJob
	onEvent func(Event)
	once    sync.Once
	workers sync.WaitGroup
	err     error
}

// onEvent must return promptly and must not call back into the same client.
func New(reader io.ReadCloser, writer io.WriteCloser, onEvent func(Event)) *Client {
	c := &Client{reader: reader, writer: writer, pending: map[int64]chan response{}, closed: make(chan struct{}), queue: make(chan writeJob, 64), onEvent: onEvent}
	c.workers.Add(2)
	go c.read()
	go c.write()
	return c
}
func (c *Client) shutdown(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.closed)
		c.reader.Close()
		c.writer.Close()
	})
}
func (c *Client) Close() { c.shutdown(io.EOF); c.workers.Wait() }
func (c *Client) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return io.EOF
}
func (c *Client) read() {
	defer c.workers.Done()
	reader := bufio.NewReaderSize(c.reader, 65536)
	for {
		data := []byte{}
		for {
			part, err := reader.ReadSlice(0)
			if len(data)+len(part) > MaxMessage {
				c.shutdown(errors.New("CDP response exceeds limit"))
				return
			}
			data = append(data, part...)
			if err == nil {
				break
			}
			if !errors.Is(err, bufio.ErrBufferFull) {
				c.shutdown(err)
				return
			}
		}
		var message response
		if len(data) < 2 || json.Unmarshal(data[:len(data)-1], &message) != nil {
			c.shutdown(errors.New("invalid CDP response"))
			return
		}
		if message.ID != 0 {
			c.mu.Lock()
			pending := c.pending[message.ID]
			delete(c.pending, message.ID)
			c.mu.Unlock()
			if pending != nil {
				pending <- message
			}
		} else if message.Method != "" && c.onEvent != nil {
			c.onEvent(Event{message.Method, message.Params, message.SessionID})
		}
	}
}
func (c *Client) write() {
	defer c.workers.Done()
	for {
		select {
		case <-c.closed:
			return
		case job := <-c.queue:
			if job.ctx.Err() != nil {
				continue
			}
			data := job.data
			for len(data) > 0 {
				n, err := c.writer.Write(data)
				if err != nil {
					c.shutdown(err)
					return
				}
				if n == 0 {
					c.shutdown(io.ErrShortWrite)
					return
				}
				data = data[n:]
			}
		}
	}
}
func (c *Client) Call(ctx context.Context, session, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if len(c.pending) >= 64 {
		c.mu.Unlock()
		return errors.New("too many pending CDP requests")
	}
	c.next++
	id := c.next
	reply := make(chan response, 1)
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	request := struct {
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
		Session string `json:"sessionId,omitempty"`
	}{id, method, params, session}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(data) > 1024*1024 {
		return errors.New("CDP request exceeds limit")
	}
	data = append(data, 0)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return c.failure()
	case c.queue <- writeJob{ctx, data}:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return c.failure()
	case response := <-reply:
		if response.Error != nil {
			return response.Error
		}
		if out == nil {
			return nil
		}
		if len(response.Result) == 0 {
			return errors.New("CDP result missing")
		}
		return json.Unmarshal(response.Result, out)
	}
}
