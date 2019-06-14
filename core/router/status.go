package router

import (
	"net/http" // just for status codes
	"sync"

	"github.com/kataras/iris/context"
)

func statusCodeSuccessful(statusCode int) bool {
	// 判断statusCode是否 <200 或者 >=400
	return !context.StatusCodeNotSuccessful(statusCode)
}

// ErrorCodeHandler is the entry
// of the list of all http error code handlers.
// 这个Entry包含了所有的 http 错误码的处理
type ErrorCodeHandler struct {
	StatusCode int
	Handlers   context.Handlers
	mu         sync.Mutex
}

// Fire executes the specific an error http error status.
// it's being wrapped to make sure that the handler
// will render(给予) correctly.
func (ch *ErrorCodeHandler) Fire(ctx context.Context) {
	// if we can reset the body
	// 目的是将响应体数据清空
	// todo 这个 IsRecording() 还不了解 ？？
	if w, ok := ctx.IsRecording(); ok {
		if statusCodeSuccessful(w.StatusCode()) { // if not an error status code
			w.WriteHeader(ch.StatusCode) // then set it manually here, otherwise it should be setted via ctx.StatusCode(...)
		}
		// reset if previous content and it's recorder, keep the status code.
		w.ClearHeaders()
		w.ResetBody()
	} else if w, ok := ctx.ResponseWriter().(*context.GzipResponseWriter); ok {
		// reset and disable the gzip in order to be an expected form of http error result
		// ResponseBody()则是重置了了GzipResponseWriter的chunks
		w.ResetBody()
		// Disable()则将GzipResponseWriter的disabled
		w.Disable()
	} else {
		// if we can't reset the body and the body has been filled
		// which means that the status code already sent,
		// then do not fire this custom error code.
		// 这里表示如果不能重置body，且body还有数据，说明状态码数据已经返回，如果<=0那本来就不用管了
		// todo 调用这个的时候，默认的应该已经Reset了，或者是说默认是IsRecording()那个流程？？
		if ctx.ResponseWriter().Written() > 0 { // != -1, rel: context/context.go#EndRequest
			return
		}
	}

	// ctx.StopExecution() // not uncomment this, is here to remember why to.
	// note for me: I don't stopping the execution of the other handlers
	// because may the user want to add a fallback error code
	// i.e
	// users := app.Party("/users")
	// users.Done(func(ctx context.Context){ if ctx.StatusCode() == 400 { /*  custom error code for /users */ }})
	// 这里不调用 .StopExecution() ，是因为有些用户可能想有一个错误回调
	// use .HandlerIndex
	// that sets the current handler index to zero
	// in order to:
	// ignore previous runs that may changed the handler index,
	// via ctx.Next or ctx.StopExecution, if any.
	//
	// use .Do
	// that overrides the existing handlers and sets and runs these error handlers.
	// in order to:
	// ignore the route's after-handlers, if any.
	// 这里将 handleIndex=0 ，且重新将此时的Context的handlers进行设置
	ctx.HandlerIndex(0)
	ctx.Do(ch.Handlers)
}

// 修改ErrorCodeHandler的 handler链
func (ch *ErrorCodeHandler) updateHandlers(handlers context.Handlers) {
	ch.mu.Lock()
	ch.Handlers = handlers
	ch.mu.Unlock()
}

// ErrorCodeHandlers contains the http error code handlers.
// User of this struct can register, get
// a status code handler based on a status code or
// fire based on a receiver context.
// 包含所有的不同HTTP状态码的 ErrorCodeHandler
type ErrorCodeHandlers struct {
	handlers []*ErrorCodeHandler
}

// 默认的状态码有404、405、500
func defaultErrorCodeHandlers() *ErrorCodeHandlers {
	chs := new(ErrorCodeHandlers)
	// register some common error handlers.
	// Note that they can be registered on-fly but
	// we don't want to reduce the performance even
	// on the first failed request.
	for _, statusCode := range []int{
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusInternalServerError} {
		chs.Register(statusCode, statusText(statusCode))
	}

	return chs
}

//给对应的 context 返回值写状态码文本
func statusText(statusCode int) context.Handler {
	return func(ctx context.Context) {
		ctx.WriteString(http.StatusText(statusCode))
	}
}

// Get returns an http error handler based on the "statusCode".
// If not found it returns nil.
// 遍历各个状态码的集合的ErrorCodeHandlers 寻找对应的状态码的ErrorCodeHandler
func (s *ErrorCodeHandlers) Get(statusCode int) *ErrorCodeHandler {
	for i, n := 0, len(s.handlers); i < n; i++ {
		if h := s.handlers[i]; h.StatusCode == statusCode {
			return h
		}
	}
	return nil
}

// Register registers an error http status code
// based on the "statusCode" < 200 || >= 400 (`context.StatusCodeNotSuccessful`).
// The handler is being wrapepd by a generic
// handler which will try to reset
// the body if recorder was enabled
// and/or disable the gzip if gzip response recorder
// was active.
// 注册指定状态码以及其handlers
func (s *ErrorCodeHandlers) Register(statusCode int, handlers ...context.Handler) *ErrorCodeHandler {
	if statusCodeSuccessful(statusCode) {
		return nil
	}

	h := s.Get(statusCode)
	// 没有则新增
	if h == nil {
		// create new and add it
		ch := &ErrorCodeHandler{
			StatusCode: statusCode,
			Handlers:   handlers,
		}

		s.handlers = append(s.handlers, ch)

		return ch
	}
	// 有则更新
	// otherwise update the handlers
	h.updateHandlers(handlers)
	return h
}

// Fire executes an error http status code handler
// based on the context's status code.
//
// If a handler is not already registered,
// then it creates & registers a new trivial handler on the-fly.
// 通过集合来弄状态码
func (s *ErrorCodeHandlers) Fire(ctx context.Context) {
	//获取context 中 ResponseWriter 中的 StatusCode 值
	statusCode := ctx.GetStatusCode()
	if statusCodeSuccessful(statusCode) {
		return
	}
	ch := s.Get(statusCode)
	if ch == nil {
		ch = s.Register(statusCode, statusText(statusCode))
	}
	ch.Fire(ctx)
}
