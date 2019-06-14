package context

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/kataras/iris/core/errors"
)

// ResponseWriter interface is used by the context to serve an HTTP handler to
// construct an HTTP response.
//
// Note: Only this ResponseWriter is an interface in order to be able
// for developers to change the response writer of the Context via `context.ResetResponseWriter`.
// The rest of the response writers implementations (ResponseRecorder & GzipResponseWriter) are coupled to the internal
// ResponseWriter implementation(*responseWriter).
//
// A ResponseWriter may not be used after the Handler
// has returned.
type ResponseWriter interface {
	// todo 以下接口为啥要这样命名？？？
	http.ResponseWriter
	http.Flusher
	http.Hijacker
	http.CloseNotifier
	http.Pusher

	// Naive returns the simple, underline and original http.ResponseWriter
	// that backends this response writer.
	Naive() http.ResponseWriter

	// BeginResponse receives an http.ResponseWriter
	// and initialize or reset the response writer's field's values.
	BeginResponse(http.ResponseWriter)

	// EndResponse is the last function which is called right before the server sent the final response.
	//
	// Here is the place which we can make the last checks or do a cleanup.
	EndResponse()

	// Writef formats according to a format specifier and writes to the response.
	//
	// Returns the number of bytes written and any write error encountered.
	Writef(format string, a ...interface{}) (n int, err error)

	// WriteString writes a simple string to the response.
	//
	// Returns the number of bytes written and any write error encountered.
	WriteString(s string) (n int, err error)

	// StatusCode returns the status code header value.
	StatusCode() int

	// Written should returns the total length of bytes that were being written to the client.
	// In addition iris provides some variables to help low-level actions:
	// NoWritten, means that nothing were written yet and the response writer is still live.
	// StatusCodeWritten, means that status code were written but no other bytes are written to the client, response writer may closed.
	// > 0 means that the reply was written and it's the total number of bytes were written.
	Written() int

	// SetWritten sets manually a value for written, it can be
	// NoWritten(-1) or StatusCodeWritten(0), > 0 means body length which is useless here.
	SetWritten(int)

	// SetBeforeFlush registers the unique callback which called exactly before the response is flushed to the client.
	SetBeforeFlush(cb func())
	// GetBeforeFlush returns (not execute) the before flush callback, or nil if not setted by SetBeforeFlush.
	GetBeforeFlush() func()
	// FlushResponse should be called only once before EndResponse.
	// it tries to send the status code if not sent already
	// and calls the  before flush callback, if any.
	//
	// FlushResponse can be called before EndResponse, but it should
	// be the last call of this response writer.
	// 只有在EndResponse被调用，且应该是ResponseWriter最后一个调用，即最后将未发送的状态码发送出去，并调用beforeFlush回调
	FlushResponse()

	// clone returns a clone of this response writer
	// it copies the header, status code, headers and the beforeFlush finally  returns a new ResponseRecorder.
	Clone() ResponseWriter

	// WiteTo writes a response writer (temp: status code, headers and body) to another response writer
	WriteTo(ResponseWriter)

	// Flusher indicates if `Flush` is supported by the client.
	//
	// The default HTTP/1.x and HTTP/2 ResponseWriter implementations
	// support Flusher, but ResponseWriter wrappers may not. Handlers
	// should always test for this ability at runtime.
	//
	// Note that even for ResponseWriters that support Flush,
	// if the client is connected through an HTTP proxy,
	// the buffered data may not reach the client until the response
	// completes.
	Flusher() (http.Flusher, bool)

	// CloseNotifier indicates if the protocol supports the underline connection closure notification.
	// CloseNotifier 返回参数 表示了是否协议支持链接关闭提醒
	CloseNotifier() (http.CloseNotifier, bool)
}

//  +------------------------------------------------------------+
//  | Response Writer Implementation                             |
//  +------------------------------------------------------------+

// todo 从这里看感觉更合理，之前那个pool struct 寻找后来进行对比？？？？
var rpool = sync.Pool{New: func() interface{} { return &responseWriter{} }}

// AcquireResponseWriter returns a new *ResponseWriter from the pool.
// Releasing is done automatically when request and response is done.
// 从sync.Pool中获取一个新的responseWriter struct(123行)的结构
// todo sync.Pool 的源码学习
func AcquireResponseWriter() ResponseWriter {
	return rpool.Get().(*responseWriter)
}

func releaseResponseWriter(w ResponseWriter) {
	rpool.Put(w)
}

// ResponseWriter is the basic response writer,
// it writes directly to the underline http.ResponseWriter
type responseWriter struct {
	// 原生的http.ResponseWriter，这里是接口
	// todo 这里的实现类在哪里呢？？？？这直接原生回调赋值，预测是server.go中的response.go
	// todo 阅读 server.go 的源码
	http.ResponseWriter

	// http状态码
	// todo 这里说的这个状态码将会被cache service使用?这是什么意思
	statusCode int // the saved status code which will be used from the cache service
	// statusCodeSent bool // reply header has been (logically) written | no needed any more as we have a variable to catch total len of written bytes

	// 返回的字节流的字节数
	written int // the total size of bytes were written

	// yes only one callback, we need simplicity here because on FireStatusCode the beforeFlush events should NOT be cleared
	// but the response is cleared.
	// Sometimes is useful to keep the event,
	// so we keep one func only and let the user decide when he/she wants to override it with an empty func before the FireStatusCode (context's behavior)
	beforeFlush func()
}

var _ ResponseWriter = (*responseWriter)(nil)

const (
	defaultStatusCode = http.StatusOK
	// NoWritten !=-1 => when nothing written before
	NoWritten = -1
	// StatusCodeWritten != 0 =>  when only status code written
	StatusCodeWritten = 0
)

// Naive returns the simple, underline and original http.ResponseWriter
// that backends this response writer.
func (w *responseWriter) Naive() http.ResponseWriter {
	return w.ResponseWriter
}

// BeginResponse receives an http.ResponseWriter
// and initialize or reset the response writer's field's values.
// 这里接受的参数是原生的http.ResponseWriter，然后初始化了responseWriter
func (w *responseWriter) BeginResponse(underline http.ResponseWriter) {
	w.beforeFlush = nil
	w.written = NoWritten
	w.statusCode = defaultStatusCode
	w.ResponseWriter = underline
}

// EndResponse is the last function which is called right before the server sent the final response.
//
// Here is the place which we can make the last checks or do a cleanup.
// EndResponse是在 服务端 响应的最后一个函数
func (w *responseWriter) EndResponse() {
	//将ResponseWriter返回到rpool中
	releaseResponseWriter(w)
}

// SetWritten sets manually a value for written, it can be
// NoWritten(-1) or StatusCodeWritten(0), > 0 means body length which is useless here.
// 这里的设置已经写的状态只有为NoWritten(-1)或StatusCodeWritten(0)
func (w *responseWriter) SetWritten(n int) {
	if n >= NoWritten && n <= StatusCodeWritten {
		w.written = n
	}
}

// Written should returns the total length of bytes that were being written to the client.
// In addition iris provides some variables to help low-level actions:
// NoWritten, means that nothing were written yet and the response writer is still live.
// StatusCodeWritten, means that status code were written but no other bytes are written to the client, response writer may closed.
// > 0 means that the reply was written and it's the total number of bytes were written.
func (w *responseWriter) Written() int {
	return w.written
}

// WriteHeader sends an HTTP response header with status code.
// If WriteHeader is not called explicitly, the first call to Write
// will trigger an implicit WriteHeader(http.StatusOK).
// Thus explicit calls to WriteHeader are mainly used to
// send error codes.
//这个在context/context.go中的StatusCode使用
func (w *responseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *responseWriter) tryWriteHeader() {
	if w.written == NoWritten { // before write, once.
		w.written = StatusCodeWritten
		w.ResponseWriter.WriteHeader(w.statusCode)// 这里调用的是go原生
	}
}

// Write writes to the client
// If WriteHeader has not yet been called, Write calls
// WriteHeader(http.StatusOK) before writing the data. If the Header
// does not contain a Content-Type line, Write adds a Content-Type set
// to the result of passing the initial 512 bytes of written data to
// DetectContentType.
//
// Depending on the HTTP protocol version and the client, calling
// Write or WriteHeader may prevent future reads on the
// Request.Body. For HTTP/1.x requests, handlers should read any
// needed request body data before writing the response. Once the
// headers have been flushed (due to either an explicit Flusher.Flush
// call or writing enough data to trigger a flush), the request body
// may be unavailable. For HTTP/2 requests, the Go HTTP server permits
// handlers to continue to read the request body while concurrently
// writing the response. However, such behavior may not be supported
// by all HTTP/2 clients. Handlers should read before writing if
// possible to maximize compatibility.
// 当使用responseWrite调用Write()给客户端的时候
func (w *responseWriter) Write(contents []byte) (int, error) {
	// 如果written为noWrite(-1)的话，则通过原生的responseWriter的writeHead来填写状态值，并将written变为0
	w.tryWriteHeader()
	n, err := w.ResponseWriter.Write(contents)
	w.written += n
	return n, err
}

// Writef formats according to a format specifier and writes to the response.
//
// Returns the number of bytes written and any write error encountered.
func (w *responseWriter) Writef(format string, a ...interface{}) (n int, err error) {
	return fmt.Fprintf(w, format, a...)
}

// WriteString writes a simple string to the response.
//
// Returns the number of bytes written and any write error encountered.
func (w *responseWriter) WriteString(s string) (int, error) {
	w.tryWriteHeader()
	n, err := io.WriteString(w.ResponseWriter, s)
	w.written += n
	return n, err
}

// StatusCode returns the status code header value
func (w *responseWriter) StatusCode() int {
	return w.statusCode
}

func (w *responseWriter) GetBeforeFlush() func() {
	return w.beforeFlush
}

// SetBeforeFlush registers the unique callback which called exactly before the response is flushed to the client
func (w *responseWriter) SetBeforeFlush(cb func()) {
	w.beforeFlush = cb
}

func (w *responseWriter) FlushResponse() {
	// responseWriter.FlushResponse()之前调用 beforeFlush 回调
	if w.beforeFlush != nil {
		w.beforeFlush()
	}

	w.tryWriteHeader()
}

// Clone returns a clone of this response writer
// it copies the header, status code, headers and the beforeFlush finally  returns a new ResponseRecorder.
func (w *responseWriter) Clone() ResponseWriter {
	wc := &responseWriter{}
	wc.ResponseWriter = w.ResponseWriter
	wc.statusCode = w.statusCode
	wc.beforeFlush = w.beforeFlush
	wc.written = w.written
	return wc
}

// WriteTo writes a response writer (temp: status code, headers and body) to another response writer.
func (w *responseWriter) WriteTo(to ResponseWriter) {
	// set the status code, failure status code are first class
	if w.statusCode >= 400 {
		to.WriteHeader(w.statusCode)
	}

	// append the headers
	if w.Header() != nil {
		for k, values := range w.Header() {
			for _, v := range values {
				if to.Header().Get(v) == "" {
					to.Header().Add(k, v)
				}
			}
		}

	}
	// the body is not copied, this writer doesn't support recording
}

// Hijack lets the caller take over the connection.
// After a call to Hijack(), the HTTP server library
// will not do anything else with the connection.
//
// It becomes the caller's responsibility to manage
// and close the connection.
//
// The returned net.Conn may have read or write deadlines
// already set, depending on the configuration of the
// Server. It is the caller's responsibility to set
// or clear those deadlines as needed.
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, isHijacker := w.ResponseWriter.(http.Hijacker); isHijacker {
		w.written = StatusCodeWritten
		return h.Hijack()
	}

	return nil, nil, errors.New("hijack is not supported by this ResponseWriter")
}

// Flusher indicates if `Flush` is supported by the client.
//
// The default HTTP/1.x and HTTP/2 ResponseWriter implementations
// support Flusher, but ResponseWriter wrappers may not. Handlers
// should always test for this ability at runtime.
//
// Note that even for ResponseWriters that support Flush,
// if the client is connected through an HTTP proxy,
// the buffered data may not reach the client until the response
// completes.
// todo 这个方法不理解，而且w.ResponseWriter是原生的，还判断(http.Flusher)？？？下面说默认的已经实现了，是原生的吗？？
// Flusher() 表示是否客户端支持Flush
// 默认的HTTP/1.x 和 HTTP/2 ResponseWriter实现，但是封装了后可能没有
// 虽然responseWriter支持Flush ，但是如果客户端是通过http代理，则缓存数据只有到响应结束才能到达客户端
func (w *responseWriter) Flusher() (http.Flusher, bool) {
	flusher, canFlush := w.ResponseWriter.(http.Flusher)
	return flusher, canFlush
}

// Flush sends any buffered data to the client.
// 将全部缓存的数据发送给客户端
// todo 缓存的数据什么时候保存的？？
func (w *responseWriter) Flush() {
	if flusher, ok := w.Flusher(); ok {
		flusher.Flush()
	}
}

// ErrPushNotSupported is returned by the Push method to
// indicate that HTTP/2 Push support is not available.
var ErrPushNotSupported = errors.New("push feature is not supported by this ResponseWriter")

// Push initiates an HTTP/2 server push. This constructs a synthetic
// request using the given target and options, serializes that request
// into a PUSH_PROMISE frame, then dispatches that request using the
// server's request handler. If opts is nil, default options are used.
//
// The target must either be an absolute path (like "/path") or an absolute
// URL that contains a valid host and the same scheme as the parent request.
// If the target is a path, it will inherit the scheme and host of the
// parent request.
//
// The HTTP/2 spec disallows recursive pushes and cross-authority pushes.
// Push may or may not detect these invalid pushes; however, invalid
// pushes will be detected and canceled by conforming clients.
//
// Handlers that wish to push URL X should call Push before sending any
// data that may trigger a request for URL X. This avoids a race where the
// client issues requests for X before receiving the PUSH_PROMISE for X.
//
// Push returns ErrPushNotSupported if the client has disabled push or if push
// is not supported on the underlying connection.
func (w *responseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, isPusher := w.ResponseWriter.(http.Pusher); isPusher {
		err := pusher.Push(target, opts)
		if err != nil && err.Error() == http.ErrNotSupported.ErrorString {
			return ErrPushNotSupported
		}
		return err
	}
	return ErrPushNotSupported
}

// CloseNotifier indicates if the protocol supports the underline connection closure notification.
func (w *responseWriter) CloseNotifier() (http.CloseNotifier, bool) {
	// todo 这里判断原生的ResponseWriter是否支持http.CloseNotifier，要学习原生的机制？？
	notifier, supportsCloseNotify := w.ResponseWriter.(http.CloseNotifier)
	return notifier, supportsCloseNotify
}

// CloseNotify returns a channel that receives at most a
// single value (true) when the client connection has gone
// away.
//
// CloseNotify may wait to notify until Request.Body has been
// fully read.
//
// After the Handler has returned, there is no guarantee
// that the channel receives a value.
//
// If the protocol is HTTP/1.1 and CloseNotify is called while
// processing an idempotent request (such a GET) while
// HTTP/1.1 pipelining is in use, the arrival of a subsequent
// pipelined request may cause a value to be sent on the
// returned channel. In practice HTTP/1.1 pipelining is not
// enabled in browsers and not seen often in the wild. If this
// is a problem, use HTTP/2 or only use CloseNotify on methods
// such as POST.
// CloseNofity() 返回一个可以接收到客户端断开连接的信息的通道，且至少在Request.Body读取完成后才会调用，
// 在handler已经返回的情况下，没法保证channel有值
// 只有在HTTP/1.1 且CloseNotify被调用的且是Get请求，且HTTP/1.1 pipelining 被使用，随后的pipelined 请求可以发送一些
// 值到channel中，如果HTTP/1.1 pipelining在浏览器没有开启，可以使用HTTP/2
// todo HTTP/1.1 pipelining是什么？？？
func (w *responseWriter) CloseNotify() <-chan bool {
	if notifier, ok := w.CloseNotifier(); ok {
		return notifier.CloseNotify()
	}

	ch := make(chan bool, 1)
	return ch
}
