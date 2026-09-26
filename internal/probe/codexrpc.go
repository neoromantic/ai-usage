package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// errExited is app-server ending its output before it answered, as a Codex
// too old to have it does.
var errExited = errors.New("app-server exited without answering")

// errUnsupported is app-server answering that it does not know a request, as
// a Codex older than that request does.
var errUnsupported = errors.New("request not supported")

type unsupported struct{ error }

func (unsupported) Is(target error) bool { return target == errUnsupported }

// codexRPC speaks newline-delimited JSON-RPC to codex app-server. One
// goroutine reads the server for the whole conversation and hands lines to
// whichever call is waiting, so a call that gave up cannot swallow a line
// meant for the next one.
type codexRPC struct {
	in    io.Writer
	next  int // the id of the last call
	lines chan []byte
	done  chan struct{} // closed once the server's output ends; err says why
	err   error
	quit  chan struct{}
	once  sync.Once
}

func newCodexRPC(in io.Writer, out io.Reader) *codexRPC {
	c := &codexRPC{
		in:    in,
		lines: make(chan []byte),
		done:  make(chan struct{}),
		quit:  make(chan struct{}),
	}
	go c.read(bufio.NewReaderSize(out, 64<<10))
	return c
}

func (c *codexRPC) read(r *bufio.Reader) {
	defer close(c.done)
	defer rescue(&c.err)
	for {
		line, err := r.ReadBytes('\n')
		// The last message may end at EOF without a newline.
		if len(bytes.TrimSpace(line)) > 0 {
			select {
			case c.lines <- line:
			case <-c.quit:
				c.err = errors.New("closed")
				return
			}
		}
		if err != nil {
			c.err = err
			return
		}
	}
}

// close releases the reader if it is waiting to hand over a line. A reader
// blocked on the pipe ends when the pipe closes.
func (c *codexRPC) close() {
	c.once.Do(func() { close(c.quit) })
}

func (c *codexRPC) notify(method string) error {
	return c.send(map[string]any{"method": method})
}

func (c *codexRPC) send(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

type codexReply struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call sends a request and waits for its response. Notifications and requests
// from the server carry a method and are skipped, as are late answers to
// calls that already gave up.
func (c *codexRPC) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.next++
	id := c.next
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.send(msg); err != nil {
		return nil, fmt.Errorf("codex %s: %s", method, shortErr(err))
	}
	for {
		select {
		case <-ctx.Done():
		case <-c.done:
			// The context also kills the process, so its end is a timeout.
			if ctx.Err() != nil {
				break
			}
			if errors.Is(c.err, io.EOF) {
				return nil, fmt.Errorf("codex %s: %w", method, errExited)
			}
			return nil, fmt.Errorf("codex %s: %s", method, shortErr(c.err))
		case line := <-c.lines:
			var r codexReply
			if json.Unmarshal(line, &r) != nil || r.ID == nil || *r.ID != id || r.Method != "" {
				continue
			}
			if r.Error != nil {
				err := fmt.Errorf("codex %s: %s", method, truncate(r.Error.Message, maxErrLine))
				// Method not found, or a request type serde does not know.
				if r.Error.Code == -32601 || strings.Contains(r.Error.Message, "unknown variant") {
					return nil, unsupported{err}
				}
				return nil, err
			}
			return r.Result, nil
		}
		return nil, fmt.Errorf("codex %s: no answer in time", method)
	}
}
