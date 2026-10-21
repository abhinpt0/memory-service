package routetx

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"

	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/gin-gonic/gin"
)

func MemoryRead(c *gin.Context, store registrystore.MemoryStore, fn func(context.Context) error) error {
	return store.InReadTx(c.Request.Context(), func(ctx context.Context) error {
		return withContext(c, ctx, fn)
	})
}

func MemoryWrite(c *gin.Context, store registrystore.MemoryStore, fn func(context.Context) error) error {
	return bufferedWrite(c, func(run func(context.Context) error) error {
		return store.InWriteTx(c.Request.Context(), run)
	}, fn)
}

func EpisodicRead(c *gin.Context, store registryepisodic.EpisodicStore, fn func(context.Context) error) error {
	return store.InReadTx(c.Request.Context(), func(ctx context.Context) error {
		return withContext(c, ctx, fn)
	})
}

func EpisodicWrite(c *gin.Context, store registryepisodic.EpisodicStore, fn func(context.Context) error) error {
	return bufferedWrite(c, func(run func(context.Context) error) error {
		return store.InWriteTx(c.Request.Context(), run)
	}, fn)
}

func bufferedWrite(c *gin.Context, inTx func(func(context.Context) error) error, fn func(context.Context) error) error {
	original := c.Writer
	buffered := &transactionResponseWriter{}
	c.Writer = buffered
	defer func() {
		c.Writer = original
	}()

	err := inTx(func(ctx context.Context) error {
		// MongoDB can retry the transaction callback. Discard output from every
		// aborted attempt and retain only the response produced by the attempt
		// whose transaction commits.
		buffered.reset(original)
		return withContext(c, ctx, fn)
	})
	if err != nil {
		return err
	}
	c.Writer = original
	buffered.commit()
	return nil
}

type transactionResponseWriter struct {
	underlying gin.ResponseWriter
	header     http.Header
	body       bytes.Buffer
	status     int
	size       int
}

var _ gin.ResponseWriter = (*transactionResponseWriter)(nil)

func (w *transactionResponseWriter) reset(underlying gin.ResponseWriter) {
	w.underlying = underlying
	w.header = underlying.Header().Clone()
	w.body.Reset()
	w.status = http.StatusOK
	w.size = -1
}

func (w *transactionResponseWriter) Header() http.Header {
	return w.header
}

func (w *transactionResponseWriter) WriteHeader(code int) {
	if code > 0 && !w.Written() {
		w.status = code
	}
}

func (w *transactionResponseWriter) WriteHeaderNow() {
	if !w.Written() {
		w.size = 0
	}
}

func (w *transactionResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	n, err := w.body.Write(data)
	w.size += n
	return n, err
}

func (w *transactionResponseWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *transactionResponseWriter) Status() int {
	return w.status
}

func (w *transactionResponseWriter) Size() int {
	return w.size
}

func (w *transactionResponseWriter) Written() bool {
	return w.size >= 0
}

func (w *transactionResponseWriter) Flush() {
	w.WriteHeaderNow()
}

func (w *transactionResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, fmt.Errorf("cannot hijack an HTTP response inside a write transaction")
}

func (w *transactionResponseWriter) CloseNotify() <-chan bool {
	return w.underlying.CloseNotify()
}

func (w *transactionResponseWriter) Pusher() http.Pusher {
	return nil
}

func (w *transactionResponseWriter) commit() {
	destination := w.underlying.Header()
	clear(destination)
	for key, values := range w.header {
		destination[key] = append([]string(nil), values...)
	}
	if w.Written() || w.status != http.StatusOK {
		w.underlying.WriteHeader(w.status)
	}
	if w.body.Len() == 0 {
		return
	}
	_, _ = w.underlying.Write(w.body.Bytes())
}

func withContext(c *gin.Context, ctx context.Context, fn func(context.Context) error) error {
	original := c.Request
	c.Request = c.Request.WithContext(ctx)
	defer func() {
		c.Request = original
	}()
	return fn(ctx)
}
