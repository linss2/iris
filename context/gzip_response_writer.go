package context

import (
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/gzip"
)

// compressionPool is a wrapper of sync.Pool, to initialize a new compression writer pool
type compressionPool struct {
	sync.Pool
	// todo 问题：Level 有什么作用，是容量？还是频率？
	Level int
}

//  +------------------------------------------------------------+
//  |GZIP raw io.writer, our gzip response writer will use that. |
//  +------------------------------------------------------------+

// default writer pool with Compressor's level setted to -1
// Level 默认是-1
// 问题：这里gzipPool中的Sync.Pool 没有实现newFunc()怎么可以获得东西？？
// 解答：看 acquireGzipWriter() 就可以知道原理
var gzipPool = &compressionPool{Level: -1}

// acquireGzipWriter prepares a gzip writer and returns it.
//
// see releaseGzipWriter too.
func acquireGzipWriter(w io.Writer) *gzip.Writer {
	// 这里是原生的 sync.Pool 的.Get()
	// todo 看sync.Pool 的源码？？？
	v := gzipPool.Get()
	// 这里v==nil，然后再初始化
	if v == nil {
		// 这里通过 /github.com/klauspost/gzip 中的gzip.go 来实现
		gzipWriter, err := gzip.NewWriterLevel(w, gzipPool.Level)
		if err != nil {
			return nil
		}
		return gzipWriter
	}
	gzipWriter := v.(*gzip.Writer)
	gzipWriter.Reset(w)
	return gzipWriter
}

// releaseGzipWriter called when flush/close and put the gzip writer back to the pool.
//
// see acquireGzipWriter too.
// 关闭指定的gzip.Writer，然后把gzip.Writer放在pool中
func releaseGzipWriter(gzipWriter *gzip.Writer) {
	gzipWriter.Close()
	gzipPool.Put(gzipWriter)
}

// writeGzip writes a compressed form of p to the underlying io.Writer. The
// compressed bytes are not necessarily flushed until the Writer is closed.
func writeGzip(w io.Writer, b []byte) (int, error) {
	gzipWriter := acquireGzipWriter(w)
	// todo 这里是gzip 压缩返回数据的核心部分，需要学习？？？？
	n, err := gzipWriter.Write(b)
	if err != nil {
		releaseGzipWriter(gzipWriter)
		return -1, err
	}
	// 再Writer关闭结束后再Flush()
	err = gzipWriter.Flush()
	releaseGzipWriter(gzipWriter)
	return n, err
}

// 之前上面的用的是第三方的gzip，现在是iris 自己定义的gzip
var gzpool = sync.Pool{New: func() interface{} { return &GzipResponseWriter{} }}

// AcquireGzipResponseWriter returns a new *GzipResponseWriter from the pool.
// Releasing is done automatically when request and response is done.
func AcquireGzipResponseWriter() *GzipResponseWriter {
	w := gzpool.Get().(*GzipResponseWriter)
	return w
}

func releaseGzipResponseWriter(w *GzipResponseWriter) {
	gzpool.Put(w)
}

// GzipResponseWriter is an upgraded response writer which writes compressed data to the underline ResponseWriter.
//
// It's a separate response writer because iris gives you the ability to "fallback" and "roll-back" the gzip encoding if something
// went wrong with the response, and write http errors in plain form instead.
// 这里表示GzipResponseWriter结构体， 在iris ResponseWriter 又封装了一层，可以分开响应
type GzipResponseWriter struct {
	ResponseWriter
	chunks   []byte
	// 这个表示是否关闭
	disabled bool
}

var _ ResponseWriter = (*GzipResponseWriter)(nil)

// BeginGzipResponse accepts a ResponseWriter
// and prepares the new gzip response writer.
// It's being called per-handler, when caller decide
// to change the response writer type.
// 初始化新获取的GzipResponseWriter
func (w *GzipResponseWriter) BeginGzipResponse(underline ResponseWriter) {
	w.ResponseWriter = underline

	w.chunks = w.chunks[0:0]
	w.disabled = false
}

// EndResponse called right before the contents of this
// response writer are flushed to the client.
func (w *GzipResponseWriter) EndResponse() {
	// 这就是将GzipResponseWriter放回到Pool中
	releaseGzipResponseWriter(w)
	// 也把底层的 ResponseWriter 放回其Pool中
	// todo 底层的ResponseWriter 是原生 server.go 中的 ResponseWriter吗
	w.ResponseWriter.EndResponse()
}

// Write prepares the data write to the gzip writer and finally to its
// underline response writer, returns the uncompressed len(contents).
// 每次写入都放在 chunks 中，返回是未压缩的长度
func (w *GzipResponseWriter) Write(contents []byte) (int, error) {
	// save the contents to serve them (only gzip data here)
	w.chunks = append(w.chunks, contents...)
	return len(contents), nil
}

// Writef formats according to a format specifier and writes to the response.
//
// Returns the number of bytes written and any write error encountered.
func (w *GzipResponseWriter) Writef(format string, a ...interface{}) (n int, err error) {
	n, err = fmt.Fprintf(w, format, a...)
	if err == nil {
		// 如果没有 Content-Type ，则设置为text/plain
		if w.ResponseWriter.Header()[ContentTypeHeaderKey] == nil {
			w.ResponseWriter.Header().Set(ContentTypeHeaderKey, ContentTextHeaderValue)
		}
	}

	return
}

// WriteString prepares the string data write to the gzip writer and finally to its
// underline response writer, returns the uncompressed len(contents).
func (w *GzipResponseWriter) WriteString(s string) (n int, err error) {
	n, err = w.Write([]byte(s))
	if err == nil {
		if w.ResponseWriter.Header()[ContentTypeHeaderKey] == nil {
			w.ResponseWriter.Header().Set(ContentTypeHeaderKey, ContentTextHeaderValue)
		}

	}
	return
}

// WriteNow compresses and writes that data to the underline response writer,
// returns the compressed written len.
//
// Use `WriteNow` instead of `Write`
// when you need to know the compressed written size before
// the `FlushResponse`, note that you can't post any new headers
// after that, so that information is not closed to the handler anymore.
// 这里是压缩响应数据的地方，返回压缩后的数据长度
// 使用 FlushResponse() 之前我们要知道响应压缩后的大小，而且使用压缩后就不能再传输新的协议头字段
func (w *GzipResponseWriter) WriteNow(contents []byte) (int, error) {
	// 如果是关闭的 ，则直接用底层的 ResponseWriter 来响应数据
	if w.disabled {
		// type noOp struct{}
		//
		// func (n noOp) Write([]byte) (int, error) {
		// 	return 0, nil
		// }
		//
		// var noop = noOp{}
		// problem solved with w.gzipWriter.Reset(noop):
		//
		// the below Write called multiple times but not from here,
		// the gzip writer does something to the writer, even if we don't call the
		// w.gzipWriter.Write it does call the underline http.ResponseWriter
		// multiple times, and therefore it changes the content-length
		// the problem that results to the #723.
		//
		// Or a better idea, acquire and adapt the gzip writer on-time when is not disabled.
		// So that is not needed any more:
		// w.gzipWriter.Reset(noop)

		return w.ResponseWriter.Write(contents)
	}

	AddGzipHeaders(w.ResponseWriter)
	// if not `WriteNow` but "Content-Length" header
	// is exists, then delete it before `.Write`
	// Content-Length should not be there.
	// no, for now at least: w.ResponseWriter.Header().Del(contentLengthHeaderKey)
	// 如果还没 WriteNow() 而头字段 Content-Length 已存在，则在返回数据之前删除 Content-Length
	return writeGzip(w.ResponseWriter, contents)
}

// AddGzipHeaders just adds the headers "Vary" to "Accept-Encoding"
// and "Content-Encoding" to "gzip".
func AddGzipHeaders(w ResponseWriter) {
	// 添加vary = Accept-Encoding
	w.Header().Add(VaryHeaderKey, AcceptEncodingHeaderKey)
	// 添加 Content-Encoding = gzip
	w.Header().Add(ContentEncodingHeaderKey, GzipHeaderValue)
}

// FlushResponse validates the response headers in order to be compatible with the gzip written data
// and writes the data to the underline ResponseWriter.
// 把GzipResponseWriter所有的缓存的数据写入响应流，并完成底层ResponseWriter所需要的方法回调
func (w *GzipResponseWriter) FlushResponse() {
	w.WriteNow(w.chunks)
	w.ResponseWriter.FlushResponse()
}

// ResetBody resets the response body.
func (w *GzipResponseWriter) ResetBody() {
	w.chunks = w.chunks[0:0]
}

// Disable turns off the gzip compression for the next .Write's data,
// if called then the contents are being written in plain form.
func (w *GzipResponseWriter) Disable() {
	w.disabled = true
}
