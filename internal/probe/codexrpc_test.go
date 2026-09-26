package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// pipeServer is an in-process app-server: it answers what answer returns
// for each request, and says nothing for an empty answer.
func pipeServer(t *testing.T, answer func(id int, method string) string) (*codexRPC, func()) {
	t.Helper()
	cr, cw := io.Pipe()
	sr, sw := io.Pipe()
	go func() {
		br := bufio.NewReader(cr)
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var m struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(line, &m)
			if out := answer(m.ID, m.Method); out != "" {
				if _, err := io.WriteString(sw, out); err != nil {
					return
				}
			}
		}
	}()
	rpc := newCodexRPC(cw, sr)
	return rpc, func() {
		rpc.close()
		_ = cw.Close()
		_ = sw.Close()
		_ = cr.Close()
	}
}

// A call that gave up must not leave a reader behind that takes the next
// call's answer.
func TestCodexRPCAbandonedCall(t *testing.T) {
	rpc, stop := pipeServer(t, func(id int, method string) string {
		if id == 2 {
			return `{"id":1,"result":{"late":true}}` + "\n" + `{"id":2,"result":{"ok":true}}` + "\n"
		}
		return ""
	})
	defer stop()
	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()
	if _, err := rpc.call(ctx1, "first", nil); err == nil {
		t.Fatal("first call was answered")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	res, err := rpc.call(ctx2, "second", nil)
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("second call: %s, %v", res, err)
	}
	stop()
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

// A long error answer is cut short, and not inside a character.
func TestCodexRPCErrorAnswer(t *testing.T) {
	long := "x" + strings.Repeat("é", 100) // two-byte characters from an odd offset
	rpc, stop := pipeServer(t, func(id int, method string) string {
		return fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":%q}}`+"\n", id, long)
	})
	defer stop()
	_, err := rpc.call(context.Background(), "m", nil)
	if msg := errText(err); !strings.Contains(msg, "éééé") || strings.Contains(msg, long) || !utf8.ValidString(msg) {
		t.Errorf("error = %q", msg)
	}
}

func TestCodexRPCLastLineWithoutNewline(t *testing.T) {
	cr, cw := io.Pipe()
	sr, sw := io.Pipe()
	go func() {
		_, _ = bufio.NewReader(cr).ReadBytes('\n')
		_, _ = io.WriteString(sw, `{"id":1,"result":{"ok":true}}`)
		_ = sw.Close()
		_ = cr.Close()
	}()
	rpc := newCodexRPC(cw, sr)
	defer rpc.close()
	res, err := rpc.call(context.Background(), "m", nil)
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("call: %s, %v", res, err)
	}
	if _, err := rpc.call(context.Background(), "next", nil); err == nil {
		t.Error("call after the server went away succeeded")
	}
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

// close releases a reader that holds a line nobody asked for.
func TestCodexRPCCloseReleasesReader(t *testing.T) {
	sr, sw := io.Pipe()
	rpc := newCodexRPC(io.Discard, sr)
	go func() { _, _ = io.WriteString(sw, `{"method":"unasked"}`+"\n") }()
	time.Sleep(20 * time.Millisecond)
	rpc.close()
	rpc.close()
	waitNoGoroutine(t, "probe.(*codexRPC).read")
	_ = sw.Close()
}

// buggyReader panics as a bug in the reading goroutine would.
type buggyReader struct{}

func (buggyReader) Read([]byte) (int, error) { panic("reader bug") }

// A bug in the goroutine that reads app-server fails the call, in one line,
// rather than ending the process.
func TestCodexRPCReaderPanicIsTheCallError(t *testing.T) {
	rpc := newCodexRPC(io.Discard, buggyReader{})
	defer rpc.close()
	_, err := rpc.call(context.Background(), "initialize", nil)
	if msg := errText(err); !strings.Contains(msg, "reader bug") || strings.Contains(msg, "\n") {
		t.Errorf("error = %q", msg)
	}
	waitNoGoroutine(t, "probe.(*codexRPC).read")
}
