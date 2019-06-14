package context

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kataras/iris/core/errors"
	"github.com/kataras/iris/core/memstore"

	"github.com/Shopify/goreferrer"
	"github.com/fatih/structs"
	"github.com/iris-contrib/blackfriday"
	formbinder "github.com/iris-contrib/formBinder"
	"github.com/json-iterator/go"
	"github.com/microcosm-cc/bluemonday"
	"gopkg.in/yaml.v2"
)

type (
	// BodyDecoder is an interface which any struct can implement in order to customize the decode action
	// from ReadJSON and ReadXML
	// BodyDecoder可以从ReadJSON和ReadXML中自定义解码行为
	//
	// Trivial example of this could be:
	// type User struct { Username string }
	//
	// func (u *User) Decode(data []byte) error {
	//	  return json.Unmarshal(data, u)
	// }
	//
	// the 'context.ReadJSON/ReadXML(&User{})' will call the User's
	// Decode option to decode the request body
	//
	// Note: This is totally optionally, the default decoders
	// for ReadJSON is the encoding/json and for ReadXML is the encoding/xml.
	//
	// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-custom-per-type/main.go
	// 看了例子表示，如果想自定义解析方式，可以对接受数据的struct，然后实现这个接口，iris.Context中UnmarshalBody中有使用
	BodyDecoder interface {
		Decode(data []byte) error
	}

	// Unmarshaler is the interface implemented by types that can unmarshal any raw data.
	// TIP INFO: Any pointer to a value which implements the BodyDecoder can be override the unmarshaler.
	// 注意：如果某些结构体也实现了这个接口（Unmarshaler）和BodyDecoder接口，则BodyCecoer接口则会被覆盖
	Unmarshaler interface {
		Unmarshal(data []byte, outPtr interface{}) error
	}

	// UnmarshalerFunc a shortcut for the Unmarshaler interface
	//
	// See 'Unmarshaler' and 'BodyDecoder' for more.
	//
	// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-custom-via-unmarshaler/main.go
	UnmarshalerFunc func(data []byte, outPtr interface{}) error
)

// Unmarshal parses the X-encoded data and stores the result in the value pointed to by v.
// Unmarshal uses the inverse of the encodings that Marshal uses, allocating maps,
// slices, and pointers as necessary.
func (u UnmarshalerFunc) Unmarshal(data []byte, v interface{}) error {
	return u(data, v)
}

// Context is the midle-man server's "object" for the clients.
//
// A New context is being acquired from a sync.Pool on each connection.
// The Context is the most important thing on the iris's http flow.
//
// Developers send responses to the client's request through a Context.
// Developers get request information from the client's request a Context.
//
// This context is an implementation of the context.Context sub-package.
// context.Context is very extensible and developers can override
// its methods if that is actually needed.
type Context interface {
	// BeginRequest is executing once for each request
	// it should prepare the (new or acquired from pool) context's fields for the new request.
	//
	// To follow the iris' flow, developer should:
	// 1. reset handlers to nil
	// handlers 的数据类型是Handlers，Handlers的数据类型是 []Handler ，Handler的数据类型是 func(Context)

	// 2. reset values to empty
	// values 的数据类型是 memstore.Store

	// 3. reset sessions to nil
	// todo 这里的sessions为nil？？？

	// 4. reset response writer to the http.ResponseWriter
	// 这里是从rpool中重新拿取了一个一个responseWriter，这里的ResponseWriter不是原生的，而是原生封装起来的
	// var rpool = sync.Pool{New: func() interface{} { return &responseWriter{} }}

	// 5. reset request to the *http.Request
	// 更新当前context的Request对象

	// and any other optional steps, depends on dev's application type.
	// 每一个请求都会执行一次，需要为新的请求准备context的字段
	BeginRequest(http.ResponseWriter, *http.Request)
	// EndRequest is executing once after a response to the request was sent and this context is useless or released.
	//
	// To follow the iris' flow, developer should:
	// 1. flush the response writer's result
	// 2. release the response writer
	// and any other optional steps, depends on dev's application type.
	//在iris代码中，的时候，通过cpool来进行release()调用中被调到
	EndRequest()

	// ResponseWriter returns an http.ResponseWriter compatible response writer, as expected.
	ResponseWriter() ResponseWriter
	// ResetResponseWriter should change or upgrade the Context's ResponseWriter.
	ResetResponseWriter(ResponseWriter)

	// Request returns the original *http.Request, as expected.
	Request() *http.Request

	// SetCurrentRouteName sets the route's name internally,
	// in order to be able to find the correct current "read-only" Route when
	// end-developer calls the `GetCurrentRoute()` function.
	// It's being initialized by the Router, if you change that name
	// manually nothing really happens except that you'll get other
	// route via `GetCurrentRoute()`.
	// Instead, to execute a different path
	// from this context you should use the `Exec` function
	// or change the handlers via `SetHandlers/AddHandler` functions.
	// 如果在这个context实现不一样的路由的方式，需要通过Exec方法或者是通过SetHandlers/AddHandler来改变路由
	SetCurrentRouteName(currentRouteName string)
	// GetCurrentRoute returns the current registered "read-only" route that
	// was being registered to this request's path.
	// 这个方法只有测试用例调用
	GetCurrentRoute() RouteReadOnly

	// Do calls the SetHandlers(handlers)
	// and executes the first handler,
	// handlers should not be empty.
	//
	// It's used by the router, developers may use that
	// to replace and execute handlers immediately.
	Do(Handlers)

	// AddHandler can add handler(s)
	// to the current request in serve-time,
	// these handlers are not persistenced to the router.
	//
	// Router is calling this function to add the route's handler.
	// If AddHandler called then the handlers will be inserted
	// to the end of the already-defined route's handler.
	//
	AddHandler(...Handler)
	// SetHandlers replaces all handlers with the new.
	SetHandlers(Handlers)
	// Handlers keeps tracking of the current handlers.
	Handlers() Handlers

	// HandlerIndex sets the current index of the
	// current context's handlers chain.
	// If -1 passed then it just returns the
	// current handler index without change the current index.
	//
	// Look Handlers(), Next() and StopExecution() too.
	// 就是设置当前context的Handler位置
	HandlerIndex(n int) (currentIndex int)
	// Proceed is an alternative way to check if a particular handler
	// has been executed and called the `ctx.Next` function inside it.
	// This is useful only when you run a handler inside
	// another handler. It justs checks for before index and the after index.
	// Proceed 是另类的检查指定的handler是否被执行，并调用ctx.Next方法,只有在你的handler中存有另一个handler
	// 才会有用，他只会检查前面的索引和后面的索引
	//
	// A usecase example is when you want to execute a middleware
	// inside controller's `BeginRequest` that calls the `ctx.Next` inside it.
	// The Controller looks the whole flow (BeginRequest, method handler, EndRequest)
	// as one handler, so `ctx.Next` will not be reflected to the method handler
	// if called from the `BeginRequest`.
	//
	// Although `BeginRequest` should NOT be used to call other handlers,
	// the `BeginRequest` has been introduced to be able to set
	// common data to all method handlers before their execution.
	// Controllers can accept middleware(s) from the MVC's Application's Router as normally.
	//
	// That said let's see an example of `ctx.Proceed`:
	//
	// var authMiddleware = basicauth.New(basicauth.Config{
	// 	Users: map[string]string{
	// 		"admin": "password",
	// 	},
	// })
	//
	// func (c *UsersController) BeginRequest(ctx iris.Context) {
	// 	if !ctx.Proceed(authMiddleware) {
	// 		ctx.StopExecution()
	// 	}
	// }
	// This Get() will be executed in the same handler as `BeginRequest`,
	// internally controller checks for `ctx.StopExecution`.
	// So it will not be fired if BeginRequest called the `StopExecution`.
	// func(c *UsersController) Get() []models.User {
	//	  return c.Service.GetAll()
	//}
	// Alternative way is `!ctx.IsStopped()` if middleware make use of the `ctx.StopExecution()` on failure.
	Proceed(Handler) bool
	// HandlerName returns the current handler's name, helpful for debugging.
	HandlerName() string
	// Next calls all the next handler from the handlers chain,
	// it should be used inside a middleware.
	//
	// Note: Custom context should override this method in order to be able to pass its own context.Context implementation.
	// 调用Handler链接下来的handler，且如果自定义context，要重写这个接口方法
	Next()
	// NextOr checks if chain has a next handler, if so then it executes it
	// otherwise it sets a new chain assigned to this Context based on the given handler(s)
	// and executes its first handler.
	//
	// Returns true if next handler exists and executed, otherwise false.
	//
	// Note that if no next handler found and handlers are missing then
	// it sends a Status Not Found (404) to the client and it stops the execution.
	// NextOr 会检验handler链是否还有下一个handler，如果有则调用，没有就设置新的handlers，并执行第一个handler，如果len(handlers)长度为0，则返回404，并停止当前的调用
	NextOr(handlers ...Handler) bool
	// NextOrNotFound checks if chain has a next handler, if so then it executes it
	// otherwise it sends a Status Not Found (404) to the client and stops the execution.
	//
	// Returns true if next handler exists and executed, otherwise false.
	// 这个内部就是直接NextOr()
	NextOrNotFound() bool
	// NextHandler returns (it doesn't execute) the next handler from the handlers chain.
	//
	// Use .Skip() to skip this handler if needed to execute the next of this returning handler.
	// 这就是handler链中的下一个
	NextHandler() Handler
	// Skip skips/ignores the next handler from the handlers chain,
	// it should be used inside a middleware.
	// 将currentHandlerIndex+1
	Skip()
	// StopExecution if called then the following .Next calls are ignored,
	// as a result the next handlers in the chain will not be fire.
	// 这个调用了后，则.Next()方法则会被无视，实际是将currentHandlerIndex设置为-1，会让handler链接下来的handler都不能被调用
	StopExecution()
	// IsStopped checks and returns true if the current position of the Context is 255,
	// means that the StopExecution() was called.
	// 则判断是否StopExecution()是否被调用
	IsStopped() bool
	// OnConnectionClose registers the "cb" function which will fire (on its own goroutine, no need to be registered goroutine by the end-dev)
	// when the underlying connection has gone away.
	// OnConnectionCLose 注册一个回调函数，这个回调函数会在链接断开的时候执行（而且自己生成一个协程）
	//
	// This mechanism can be used to cancel long operations on the server
	// if the client has disconnected before the response is ready.
	// 这个机制可以被用在取消长操作，比如在应答前客户端以及取消链接了
	//
	// It depends on the `http#CloseNotify`.
	// CloseNotify may wait to notify until Request.Body has been
	// fully read.
	// 这个取决于CloseNotify，CloseNotify等请求体被全部读取完后去notify
	// todo CloseNotify去notify什么？？
	//
	// After the main Handler has returned, there is no guarantee
	// that the channel receives a value.
	// 当mainHandler全部返回，通过也没法保证有接收到值
	//
	// Finally, it reports whether the protocol supports pipelines (HTTP/1.1 with pipelines disabled is not supported).
	// The "cb" will not fire for sure if the output value is false.
	//
	// Note that you can register only one callback for the entire request handler chain/per route.
	//
	// Look the `ResponseWriter#CloseNotifier` for more.
	OnConnectionClose(fnGoroutine func()) bool
	// OnClose registers the callback function "cb" to the underline connection closing event using the `Context#OnConnectionClose`
	// and also in the end of the request handler using the `ResponseWriter#SetBeforeFlush`.
	// Note that you can register only one callback for the entire request handler chain/per route.
	//
	// Look the `Context#OnConnectionClose` and `ResponseWriter#SetBeforeFlush` for more.
	// 这里就注册了一个回调函数，而且依次调用了ctx.OnConectionClose(cb)和ctx.writer.SetBeforeFlush()
	// 这个暂时只有_example文件夹中调用
	OnClose(cb func())

	//  +------------------------------------------------------------+
	//  | Current "user/request" storage                             |
	//  | and share information between the handlers - Values().     |
	//  | Save and get named path parameters - Params()              |
	//  +------------------------------------------------------------+

	// Params returns the current url's named parameters key-value storage.
	// Named path parameters are being saved here.
	// This storage, as the whole Context, is per-request lifetime.
	// Params 返回是当前的url中后面的参数保存在这里，本质是memstore.Store（k-v形式）
	Params() *RequestParams

	// Values returns the current "user" storage.
	// Named path parameters and any optional data can be saved here.
	// This storage, as the whole Context, is per-request lifetime.
	//
	// You can use this function to Set and Get local values
	// that can be used to share information between handlers and middleware.
	// 返回context中的 memstore.Store
	Values() *memstore.Store
	// Translate is the i18n (localization) middleware's function,
	// it calls the Get("translate") to return the translated value.
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/miscellaneous/i18n
	// 这个有关i18n，可以根据上面的例子配合学习
	Translate(format string, args ...interface{}) string

	//  +------------------------------------------------------------+
	//  | Path, Host, Subdomain, IP, Headers etc...                  |
	//  +------------------------------------------------------------+

	// Method returns the request.Method, the client's http method to the server.
	Method() string
	// Path returns the full request path,
	// escaped if EnablePathEscape config field is true.
	Path() string
	// RequestPath returns the full request path,
	// based on the 'escape'.
	// 这个根据传递的参数来觉得是否
	RequestPath(escape bool) string

	// Host returns the host part of the current url.
	Host() string
	// Subdomain returns the subdomain of this request, if any.
	// Note that this is a fast method which does not cover all cases.
	Subdomain() (subdomain string)
	// IsWWW returns true if the current subdomain (if any) is www.
	IsWWW() bool
	// RemoteAddr tries to parse and return the real client's request IP.
	//
	// Based on allowed headers names that can be modified from Configuration.RemoteAddrHeaders.
	//
	// If parse based on these headers fail then it will return the Request's `RemoteAddr` field
	// which is filled by the server before the HTTP handler.
	//
	// Look `Configuration.RemoteAddrHeaders`,
	//      `Configuration.WithRemoteAddrHeader(...)`,
	//      `Configuration.WithoutRemoteAddrHeader(...)` for more.
	// 这个具体还是看context的实现方式
	RemoteAddr() string
	// GetHeader returns the request header's value based on its name.
	GetHeader(name string) string
	// IsAjax returns true if this request is an 'ajax request'( XMLHttpRequest)
	//
	// There is no a 100% way of knowing that a request was made via Ajax.
	// You should never trust data coming from the client, they can be easily overcome by spoofing.
	//
	// Note that "X-Requested-With" Header can be modified by any client(because of "X-"),
	// so don't rely on IsAjax for really serious stuff,
	// try to find another way of detecting the type(i.e, content type),
	// there are many blogs that describe these problems and provide different kind of solutions,
	// it's always depending on the application you're building,
	// this is the reason why this `IsAjax`` is simple enough for general purpose use.
	//
	// Read more at: https://developer.mozilla.org/en-US/docs/AJAX
	// and https://xhr.spec.whatwg.org/
	// 注意:这个方法不是百分百准去，因为可以ip欺骗，"X-Requested-With" Header可以被客户端修改，实现类就是判断
	// GetHeader("X-Requested-With")=="XMLHttpRequest"
	IsAjax() bool
	// IsMobile checks if client is using a mobile device(phone or tablet) to communicate with this server.
	// If the return value is true that means that the http client using a mobile
	// device to communicate with the server, otherwise false.
	//
	// Keep note that this checks the "User-Agent" request header.
	// 这个是通过User-Agent 的请求头来判断
	IsMobile() bool
	// GetReferrer extracts and returns the information from the "Referer" header as specified
	// in https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Referrer-Policy
	// or by the URL query parameter "referer".
	// 提取请求头"Referrer"来实现
	// todo 问题:不知道Referrer如何使用？？？
	GetReferrer() Referrer
	//  +------------------------------------------------------------+
	//  | Headers helpers                                            |
	//  +------------------------------------------------------------+

	// Header adds a header to the response writer.
	Header(name string, value string)

	// ContentType sets the response writer's header key "Content-Type" to the 'cType'.
	// 将 cType 写到响应的Writer中的 Content-Type 请求头中
	ContentType(cType string)
	// GetContentType returns the response writer's header value of "Content-Type"
	// which may, setted before with the 'ContentType'.
	// 这个是返回响应值汇总的 Content-Type 请求头
	GetContentType() string
	// GetContentType returns the request's header value of "Content-Type".
	// 这是返回Request中的 Content-Type
	GetContentTypeRequested() string

	// GetContentLength returns the request's header value of "Content-Length".
	// Returns 0 if header was unable to be found or its value was not a valid number.
	// 返回Request中的 Content-Length
	GetContentLength() int64

	// StatusCode sets the status code header to the response.
	// Look .`GetStatusCode` too.
	// 这是针对 iris 中 Context 中的 ResponseWriter 中的 Code
	StatusCode(statusCode int)
	// GetStatusCode returns the current status code of the response.
	// Look `StatusCode` too.
	// 返回上面StatusCode()社会的值
	GetStatusCode() int

	// Redirect sends a redirect response to the client
	// to a specific url or relative path.
	// accepts 2 parameters string and an optional int
	// first parameter is the url to redirect
	// second parameter is the http status should send,
	// default is 302 (StatusFound),
	// you can set it to 301 (Permant redirect)
	// or 303 (StatusSeeOther) if POST method,
	// or StatusTemporaryRedirect(307) if that's nessecery.
	// 表示重定向的方式，前一个表达了重定向的地址，后一个表达状态码，虽然后面是变长参数，但是实现中只是用了第一个，
	// 内在调用的是原生 server.go 中 http.Redirect
	Redirect(urlToRedirect string, statusHeader ...int)

	//  +------------------------------------------------------------+
	//  | Various Request and Post Data                              |
	//  +------------------------------------------------------------+

	// URLParam returns true if the url parameter exists, otherwise false.
	// 判断url中的参数，通过 ctx.request.URL.Query() ，这里的 ctx.Request 是原生的 http.Request
	URLParamExists(name string) bool
	// URLParamDefault returns the get parameter from a request,
	// if not found then "def" is returned.
	// 查询url参数中指定的值，如果没有则 def 参数的值
	URLParamDefault(name string, def string) string
	// URLParam returns the get parameter from a request, if any.
	// 本质就是 URLParamDefault(name, "")
	URLParam(name string) string
	// URLParamTrim returns the url query parameter with trailing white spaces removed from a request.
	// 就是在URLParam基础上多了处理空格
	URLParamTrim(name string) string
	// URLParamTrim returns the escaped url query parameter from a request.
	URLParamEscape(name string) string
	// URLParamInt returns the url query parameter as int value from a request,
	// returns -1 and an error if parse failed.
	// 根据 name 参数返回对应的值是 int 类型
	URLParamInt(name string) (int, error)
	// URLParamIntDefault returns the url query parameter as int value from a request,
	// if not found or parse failed then "def" is returned.
	URLParamIntDefault(name string, def int) int
	// URLParamInt32Default returns the url query parameter as int32 value from a request,
	// if not found or parse failed then "def" is returned.
	URLParamInt32Default(name string, def int32) int32
	// URLParamInt64 returns the url query parameter as int64 value from a request,
	// returns -1 and an error if parse failed.
	URLParamInt64(name string) (int64, error)
	// URLParamInt64Default returns the url query parameter as int64 value from a request,
	// if not found or parse failed then "def" is returned.
	URLParamInt64Default(name string, def int64) int64
	// URLParamFloat64 returns the url query parameter as float64 value from a request,
	// returns -1 and an error if parse failed.
	URLParamFloat64(name string) (float64, error)
	// URLParamFloat64Default returns the url query parameter as float64 value from a request,
	// if not found or parse failed then "def" is returned.
	URLParamFloat64Default(name string, def float64) float64
	// URLParamBool returns the url query parameter as boolean value from a request,
	// returns an error if parse failed or not found.
	URLParamBool(name string) (bool, error)
	// URLParams returns a map of GET query parameters separated by comma if more than one
	// it returns an empty map if nothing found.
	// 就是将 url.go 中 Values (type Values map[string][]string）转为对应的格式
	URLParams() map[string]string

	// FormValueDefault returns a single parsed form value by its "name",
	// including both the URL field's query parameters and the POST or PUT form data.
	//
	// Returns the "def" if not found.
	// 通过ctx.form() 得到的 (form map[string][]string, found bool），然后拿取对应map的第一个string，如果没有则返回def
	FormValueDefault(name string, def string) string
	// FormValue returns a single parsed form value by its "name",
	// including both the URL field's query parameters and the POST or PUT form data.
	// 即 FormValueDefault(name, "")
	FormValue(name string) string
	// FormValues returns the parsed form data, including both the URL
	// field's query parameters and the POST or PUT form data.
	//
	// The default form's memory maximum size is 32MB, it can be changed by the
	// `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
	//
	// NOTE: A check for nil is necessary.
	// 这个是直接返回了ctx.form 可能会有nil，所以一定要判断是否为nil，即len()!=0
	FormValues() map[string][]string

	// PostValueDefault returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name".
	//
	// If not found then "def" is returned instead.
	// 这个内部是通过ctx.form()后，然后在通过request.PostForm()过
	PostValueDefault(name string, def string) string
	// PostValue returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name"
	PostValue(name string) string
	// PostValueTrim returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name",  without trailing spaces.
	PostValueTrim(name string) string
	// PostValueInt returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name", as int.
	//
	// If not found returns -1 and a non-nil error.
	PostValueInt(name string) (int, error)
	// PostValueIntDefault returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name", as int.
	//
	// If not found returns or parse errors the "def".
	PostValueIntDefault(name string, def int) int
	// PostValueInt64 returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name", as float64.
	//
	// If not found returns -1 and a no-nil error.
	PostValueInt64(name string) (int64, error)
	// PostValueInt64Default returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name", as int64.
	//
	// If not found or parse errors returns the "def".
	PostValueInt64Default(name string, def int64) int64
	// PostValueInt64Default returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name", as float64.
	//
	// If not found returns -1 and a non-nil error.
	PostValueFloat64(name string) (float64, error)
	// PostValueInt64Default returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name", as float64.
	//
	// If not found or parse errors returns the "def".
	PostValueFloat64Default(name string, def float64) float64
	// PostValueInt64Default returns the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name", as bool.
	//
	// If not found or value is false, then it returns false, otherwise true.
	PostValueBool(name string) (bool, error)
	// PostValues returns all the parsed form data from POST, PATCH,
	// or PUT body parameters based on a "name" as a string slice.
	//
	// The default form's memory maximum size is 32MB, it can be changed by the
	// `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
	PostValues(name string) []string
	// FormFile returns the first uploaded file that received from the client.
	//
	// The default form's memory maximum size is 32MB, it can be changed by the
	//  `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/http_request/upload-file
	// 根据 key 获取第一个客户端上传的文件,通过原生的 http.Request{}.FormFile()
	FormFile(key string) (multipart.File, *multipart.FileHeader, error)
	// UploadFormFiles uploads any received file(s) from the client
	// to the system physical location "destDirectory".
	// 这是将客户端上传的图片 保存到磁盘中
	//
	// The second optional argument "before" gives caller the chance to
	// modify the *miltipart.FileHeader before saving to the disk,
	// it can be used to change a file's name based on the current request,
	// all FileHeader's options can be changed. You can ignore it if
	// you don't need to use this capability before saving a file to the disk.
	// 参数 before 是用来将文件上传到指定磁盘时候，可以让其多一步操作
	//
	// Note that it doesn't check if request body streamed.
	// 如果是请求流，则不用检查
	// todo 问题：这里的请求流是什么意思？？检查什么呢？？
	//
	// Returns the copied length as int64 and
	// a not nil error if at least one new file
	// can't be created due to the operating system's permissions or
	// http.ErrMissingFile if no file received.
	//
	// If you want to receive & accept files and manage them manually you can use the `context#FormFile`
	// instead and create a copy function that suits your needs, the below is for generic usage.
	// 如果想手动处理文件流，则可以用上面的 FormFile() ，UploadFormFiles是通用的处理方式
	//
	// The default form's memory maximum size is 32MB, it can be changed by the
	//  `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
	//
	// See `FormFile` to a more controlled to receive a file.
	//
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/http_request/upload-files
	UploadFormFiles(destDirectory string, before ...func(Context, *multipart.FileHeader)) (n int64, err error)

	//  +------------------------------------------------------------+
	//  | Custom HTTP Errors                                         |
	//  +------------------------------------------------------------+

	// NotFound emits an error 404 to the client, using the specific custom error error handler.
	// Note that you may need to call ctx.StopExecution() if you don't want the next handlers
	// to be executed. Next handlers are being executed on iris because you can alt the
	// error code and change it to a more specific one, i.e
	// users := app.Party("/users")
	// users.Done(func(ctx context.Context){ if ctx.StatusCode() == 400 { /*  custom error code for /users */ }})
	// 即 StatusCode(404)，即通过原生的 responseWriter 的 WriteHeader()
	NotFound()

	//  +------------------------------------------------------------+
	//  | Body Readers                                               |
	//  +------------------------------------------------------------+

	// SetMaxRequestBodySize sets a limit to the request body size
	// should be called before reading the request body from the client.
	// 限制请求体的大小，在读取来自客户端请求体数据之前调用
	// 其本质是设置Request.Body的参数，其中Body是 io.ReadCloser
	// todo 原生 io.ReadCloser，以及 Request.Body 源码阅读？？
	// 通过原生 request.go 中 maxBytesReader 来限制请求体的大小
	SetMaxRequestBodySize(limitOverBytes int64)

	// UnmarshalBody reads the request's body and binds it to a value or pointer of any type.
	// Examples of usage: context.ReadJSON, context.ReadXML.
	//
	// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-custom-via-unmarshaler/main.go
	//
	// UnmarshalBody does not check about gzipped data.
	// Do not rely on compressed data incoming to your server. The main reason is: https://en.wikipedia.org/wiki/Zip_bomb
	// However you are still free to read the `ctx.Request().Body io.Reader` manually.
	// 可以看例子来，即自定义Unmarshaler的格式
	UnmarshalBody(outPtr interface{}, unmarshaler Unmarshaler) error
	// ReadJSON reads JSON from request's body and binds it to a pointer of a value of any json-valid type.
	//
	// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-json/main.go
	// 内部实现直接使用了json.Unmarshaler，如果有优化则jsonitor.Unmashaler
	// 本质都是通过UnmarshalBody的方法，不过第二参数有修改
	ReadJSON(jsonObjectPtr interface{}) error
	// ReadXML reads XML from request's body and binds it to a pointer of a value of any xml-valid type.
	//
	// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-xml/main.go
	ReadXML(xmlObjectPtr interface{}) error
	// ReadForm binds the formObject  with the form data
	// it supports any kind of type, including custom structs.
	// It will return nothing if request data are empty.
	//
	// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-form/main.go
	// 这是将form格式转化为对象
	// todo 本质是通过formbinder.Decode()来实现，阅读formbinder.Decode()
	ReadForm(formObjectPtr interface{}) error

	//  +------------------------------------------------------------+
	//  | Body (raw) Writers                                         |
	//  +------------------------------------------------------------+

	// Write writes the data to the connection as part of an HTTP reply.
	//
	// If WriteHeader has not yet been called, Write calls
	// WriteHeader(http.StatusOK) before writing the data. If the Header
	// does not contain a Content-Type line, Write adds a Content-Type set
	// to the result of passing the initial 512 bytes of written data to
	// DetectContentType.
	// 如果在这之前，WriteHeader没有被调用，则会调用WriteHeader(http.StatusOK)，
	// 如果Header没有 Content-Type ，则会设置去通过返回的数据最初的512字节数来判断
	// todo 512字节数判断的规则？？？？
	// 在其Write的实现部分会调用一次tryWriterHeader
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
	// 不同的客户端HTTP协议，Write()执行后会有不同的效果
	// HTTP/1.x：服务端Write调用，其请求体则会过期
	// HTTP/2  ：服务端Write可以和读取请求体并发执行，不过有些行为不会支持
	// 实现是用原生的responseWriter.Write()来实现
	Write(body []byte) (int, error)
	// Writef formats according to a format specifier and writes to the response.
	//
	// Returns the number of bytes written and any write error encountered.
	// 本质还是Write()，这个实现方式去看看
	Writef(format string, args ...interface{}) (int, error)
	// WriteString writes a simple string to the response.
	//
	// Returns the number of bytes written and any write error encountered.
	// 意思也很明显，这个实现方式要去看看
	WriteString(body string) (int, error)

	// SetLastModified sets the "Last-Modified" based on the "modtime" input.
	// If "modtime" is zero then it does nothing.
	//
	// It's mostly internally on core/router and context packages.
	//
	// Note that modtime.UTC() is being used instead of just modtime, so
	// you don't have to know the internals in order to make that works.
	// 这就是设置响应的头文件 "Last-Modified"
	SetLastModified(modtime time.Time)
	// CheckIfModifiedSince checks if the response is modified since the "modtime".
	// Note that it has nothing to do with server-side caching.
	// It does those checks by checking if the "If-Modified-Since" request header
	// sent by client or a previous server response header
	// (e.g with WriteWithExpiration or StaticEmbedded or Favicon etc.)
	// is a valid one and it's before the "modtime".
	//
	// A check for !modtime && err == nil is necessary to make sure that
	// it's not modified since, because it may return false but without even
	// had the chance to check the client-side (request) header due to some errors,
	// like the HTTP Method is not "GET" or "HEAD" or if the "modtime" is zero
	// or if parsing time from the header failed.
	//
	// It's mostly used internally, e.g. `context#WriteWithExpiration`.
	//
	// Note that modtime.UTC() is being used instead of just modtime, so
	// you don't have to know the internals in order to make that works.
	// 判断客户端请求的时间与服务端的时间在UTC格式下，客户端的时间是否是在于服务端的时间之后
	// 似乎有两种使用情况，一种是普通请求，一种是文件时间，预计是来处理客户端缓存用的
	CheckIfModifiedSince(modtime time.Time) (bool, error)
	// WriteNotModified sends a 304 "Not Modified" status code to the client,
	// it makes sure that the content type, the content length headers
	// and any "ETag" are removed before the response sent.
	//
	// It's mostly used internally on core/router/fs.go and context methods.
	// 返回304的时候，要注意删除Content-Type和Content-Length以及根据Etag得到的Last-Modified
	WriteNotModified()
	// WriteWithExpiration like Write but it sends with an expiration datetime
	// which is refreshed every package-level `StaticCacheDuration` field.
	// 与Write类似，不过多了时间用来修改响应流头协议 Last-Modified
	WriteWithExpiration(body []byte, modtime time.Time) (int, error)
	// StreamWriter registers the given stream writer for populating
	// response body.
	//
	// Access to context's and/or its' members is forbidden from writer.
	//
	// This function may be used in the following cases:
	//
	//     * if response body is too big (more than iris.LimitRequestBodySize(if setted)).
	//     * if response body is streamed from slow external sources.
	//     * if response body must be streamed to the client in chunks.
	//     (aka `http server push`).
	//
	// receives a function which receives the response writer
	// and returns false when it should stop writing, otherwise true in order to continue
	// 注册一个写入响应体的方法，可以用 and/or 来禁止，当响应体很大（超过了iris设置的请求体大小），或返回的数据是外部数据（比如硬盘），
	// 或返回的数据要成块
	// todo 问题：in chunks 不理解？？可能是gzipResponseWrtier
	// 暂时还没有地方被使用
	StreamWriter(writer func(w io.Writer) bool)

	//  +------------------------------------------------------------+
	//  | Body Writers with compression                              |
	//  +------------------------------------------------------------+
	// ClientSupportsGzip retruns true if the client supports gzip compression.
	// 判断iris是否支持Gzip压缩
	ClientSupportsGzip() bool
	// WriteGzip accepts bytes, which are compressed to gzip format and sent to the client.
	// returns the number of bytes written and an error ( if the client doesn' supports gzip compression)
	// You may re-use this function in the same handler
	// to write more data many times without any troubles.
	// 如果客户端不支持gzip压缩，则会报错，而且这个方法可以在一样的handler中多次使用，
	// 内部通过ctx.GzipResponseWriter().Write()来实现
	WriteGzip(b []byte) (int, error)
	// TryWriteGzip accepts bytes, which are compressed to gzip format and sent to the client.
	// If client does not supprots gzip then the contents are written as they are, uncompressed.
	// 这个方式就比之前的方式柔和了很多
	TryWriteGzip(b []byte) (int, error)
	// GzipResponseWriter converts the current response writer into a response writer
	// which when its .Write called it compress the data to gzip and writes them to the client.
	//
	// Can be also disabled with its .Disable and .ResetBody to rollback to the usual response writer.
	// 将当前的response Writer 转成 GzipResponseWriter
	// 可以通过 GzipResponseWriter中的 .Disable 和 .ResetBody 来回滚到之前的ResponseWriter
	GzipResponseWriter() *GzipResponseWriter
	// Gzip enables or disables (if enabled before) the gzip response writer,if the client
	// supports gzip compression, so the following response data will
	// be sent as compressed gzip data to the client.
	// 这里表示是否开启Gzip
	Gzip(enable bool)

	//  +------------------------------------------------------------+
	//  | Rich Body Content Writers/Renderers                        |
	//  +------------------------------------------------------------+

	// ViewLayout sets the "layout" option if and when .View
	// is being called afterwards, in the same request.
	// Useful when need to set or/and change a layout based on the previous handlers in the chain.
	//
	// Note that the 'layoutTmplFile' argument can be setted to iris.NoLayout || view.NoLayout
	// to disable the layout for a specific view render action,
	// it disables the engine's configuration's layout property.
	//
	// Look .ViewData and .View too.
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/view/context-view-data/
	// 表示具体layout模板的文件，内部通过Configuration.go 中的 ViewLayoutContextKey 字段来保存
	ViewLayout(layoutTmplFile string)
	// ViewData saves one or more key-value pair in order to be passed if and when .View
	// is being called afterwards, in the same request.
	// Useful when need to set or/and change template data from previous hanadlers in the chain.
	//
	// If .View's "binding" argument is not nil and it's not a type of map
	// then these data are being ignored, binding has the priority, so the main route's handler can still decide.
	// If binding is a map or context.Map then these data are being added to the view data
	// and passed to the template.
	//
	// After .View, the data are not destroyed, in order to be re-used if needed (again, in the same request as everything else),
	// to clear the view data, developers can call:
	// ctx.Set(ctx.Application().ConfigurationReadOnly().GetViewDataContextKey(), nil)
	//
	// If 'key' is empty then the value is added as it's (struct or map) and developer is unable to add other value.
	//
	// Look .ViewLayout and .View too.
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/view/context-view-data/
	// 首先描述viewDataContextKey（可以通过ctx.Application().ConfigurationReadOnly().GetViewDataContextKey()，
	// 即Configuration中ViewDataContextKey字段获取），
	// 如果key为""，则这里的value是存储的容器，存储在context.Values中，key是viewDataContextKey
	// 如果key!=""，则通过viewDataContextKey获取context.Values对应的容器，如果容器不存在，则新建一个context.Map{}的容器，
	// 并保存key和value，如果容器存在，则判断是否是map或者是context.Map，如果有则更新，没有则新增，如果不是则忽略
	ViewData(key string, value interface{})
	// GetViewData returns the values registered by `context#ViewData`.
	// The return value is `map[string]interface{}`, this means that
	// if a custom struct registered to ViewData then this function
	// will try to parse it to map, if failed then the return value is nil
	// A check for nil is always a good practise if different
	// kind of values or no data are registered via `ViewData`.
	//
	// Similarly to `viewData := ctx.Values().Get("iris.viewData")` or
	// `viewData := ctx.Values().Get(ctx.Application().ConfigurationReadOnly().GetViewDataContextKey())`.
	// 这个说明了如果存储的容器（容器的意思看ViewData()）是自定义结构，则会自发的将其转为map形式，如果失败则返回nil，所以使用的时候要注意是否为nil
	GetViewData() map[string]interface{}
	// View renders a template based on the registered view engine(s).
	// First argument accepts the filename, relative to the view engine's Directory and Extension,
	// i.e: if directory is "./templates" and want to render the "./templates/users/index.html"
	// then you pass the "users/index.html" as the filename argument.
	//
	// The second optional argument can receive a single "view model"
	// that will be binded to the view template if it's not nil,
	// otherwise it will check for previous view data stored by the `ViewData`
	// even if stored at any previous handler(middleware) for the same request.
	//
	// Look .ViewData` and .ViewLayout too.
	//
	// Examples: https://github.com/kataras/iris/tree/master/_examples/view
	View(filename string, optionalViewModel ...interface{}) error

	// Binary writes out the raw bytes as binary data.
	Binary(data []byte) (int, error)
	// Text writes out a string as plain text.
	Text(text string) (int, error)
	// HTML writes out a string as text/html.
	HTML(htmlContents string) (int, error)
	// JSON marshals the given interface object and writes the JSON response.
	JSON(v interface{}, options ...JSON) (int, error)
	// JSONP marshals the given interface object and writes the JSON response.
	JSONP(v interface{}, options ...JSONP) (int, error)
	// XML marshals the given interface object and writes the XML response.
	XML(v interface{}, options ...XML) (int, error)
	// Markdown parses the markdown to html and renders its result to the client.
	Markdown(markdownB []byte, options ...Markdown) (int, error)
	// YAML parses the "v" using the yaml parser and renders its result to the client.
	YAML(v interface{}) (int, error)
	//  +------------------------------------------------------------+
	//  | Serve files                                                |
	//  +------------------------------------------------------------+

	// ServeContent serves content, headers are autoset
	// receives three parameters, it's low-level function, instead you can use .ServeFile(string,bool)/SendFile(string,string)
	//
	//
	// You can define your own "Content-Type" with `context#ContentType`, before this function call.
	//
	// This function doesn't support resuming (by range),
	// use ctx.SendFile or router's `StaticWeb` instead.
	// 自动设置content和headers，是比较低级的方法，可以被.ServeFile()/SendFile()取代
	// 可以在这个方法前自己定义Conetnt-Type
	// 这个方法不支持重新设置，可以使用ctx.SendFile 或者是 router's StaticWeb替代
	// todo io.ReadSeeker 源码阅读？？
	// ServeContent 是通过 io的角度处理
	ServeContent(content io.ReadSeeker, filename string, modtime time.Time, gzipCompression bool) error
	// ServeFile serves a file (to send a file, a zip for example to the client you should use the `SendFile` instead)
	// receives two parameters
	// filename/path (string)
	// gzipCompression (bool)
	//
	// You can define your own "Content-Type" with `context#ContentType`, before this function call.
	//
	// This function doesn't support resuming (by range),
	// use ctx.SendFile or router's `StaticWeb` instead.
	//
	// Use it when you want to serve dynamic files to the client.
	// 内部实现是通过ServeContent()来实现，这里封装了从File角度处理
	ServeFile(filename string, gzipCompression bool) error
	// SendFile sends file for force-download to the client
	//
	// Use this instead of ServeFile to 'force-download' bigger files to the client.
	// 这里是为了让客户端强制下载（毕竟大文件直接浏览耗时间）
	// 设置加了一个请求头通过"Content-Disposition = attachment;filename= destinationName" 来处理
	// 然后调用ServeFile
	SendFile(filename string, destinationName string) error

	//  +------------------------------------------------------------+
	//  | Cookies                                                    |
	//  +------------------------------------------------------------+

	// SetCookie adds a cookie.
	// Use of the "options" is not required, they can be used to amend the "cookie".
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
	// todo 阅读http.Cookie源码？？
	// options是在cookie被返回时进行的处理
	SetCookie(cookie *http.Cookie, options ...CookieOption)
	// SetCookieKV adds a cookie, requires the name(string) and the value(string).
	//
	// By default it expires at 2 hours and it's added to the root path,
	// use the `CookieExpires` and `CookiePath` to modify them.
	// Alternatively: ctx.SetCookie(&http.Cookie{...})
	//
	// If you want to set custom the path:
	// ctx.SetCookieKV(name, value, iris.CookiePath("/custom/path/cookie/will/be/stored"))
	//
	// If you want to be visible only to current request path:
	// ctx.SetCookieKV(name, value, iris.CookieCleanPath/iris.CookiePath(""))
	// More:
	//                              iris.CookieExpires(time.Duration)
	//                              iris.CookieHTTPOnly(false)
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
	// 在cookie中添加 name 和 value，且默认生存时间是2小时，且添加到根路径，可以通过 CookieExpires 和 CookiePath 修改
	// 具体的使用方式可以看上面注释的例子，本质还是SetCookie(&http.Cookie{})
	SetCookieKV(name, value string, options ...CookieOption)
	// GetCookie returns cookie's value by it's name
	// returns empty string if nothing was found.
	//
	// If you want more than the value then:
	// cookie, err := ctx.Request().Cookie("name")
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
	// 根据指定的name来查询Cookie
	GetCookie(name string, options ...CookieOption) string
	// RemoveCookie deletes a cookie by it's name and path = "/".
	// Tip: change the cookie's path to the current one by: RemoveCookie("name", iris.CookieCleanPath)
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
	// 删除cookie中path为"/"中对应的name，可以通过iris.CookieCleanPath 修改成当前路径
	// todo 阅读 iris.CookieCleanPath 实现
	RemoveCookie(name string, options ...CookieOption)
	// VisitAllCookies takes a visitor which loops
	// on each (request's) cookies' name and value.
	// 自定义接口来循环处理Cookie的值
	VisitAllCookies(visitor func(name string, value string))

	// MaxAge returns the "cache-control" request header's value
	// seconds as int64
	// if header not found or parse failed then it returns -1.
	// 如果请求头有 Cache-Control ，才返回int64 结构的生存时间，如果没有则返回-1
	MaxAge() int64

	//  +------------------------------------------------------------+
	//  | Advanced: Response Recorder and Transactions               |
	//  +------------------------------------------------------------+

	// Record transforms the context's basic and direct responseWriter to a ResponseRecorder
	// which can be used to reset the body, reset headers, get the body,
	// get & set the status code at any time and more.
	// Record 将ResponseWriter 转变成 ResponseRecorder
	// todo ResponseRecorder 是什么作用，阅读ResponseRecorder 源码？？
	Record()
	// Recorder returns the context's ResponseRecorder
	// if not recording then it starts recording and returns the new context's ResponseRecorder
	// 返回当前context的ResponseRecorder
	Recorder() *ResponseRecorder
	// IsRecording returns the response recorder and a true value
	// when the response writer is recording the status code, body, headers and so on,
	// else returns nil and false.
	// 就是断言类型 ResponseRecorder
	IsRecording() (*ResponseRecorder, bool)

	// todo BeginTransaction 想了解可以看一下？？？
	// BeginTransaction starts a scoped transaction.
	//
	// You can search third-party articles or books on how Business Transaction works (it's quite simple, especially here).
	//
	// Note that this is unique and new
	// (=I haver never seen any other examples or code in Golang on this subject, so far, as with the most of iris features...)
	// it's not covers all paths,
	// such as databases, this should be managed by the libraries you use to make your database connection,
	// this transaction scope is only for context's response.
	// Transactions have their own middleware ecosystem also, look iris.go:UseTransaction.
	//
	// See https://github.com/kataras/iris/tree/master/_examples/ for more
	BeginTransaction(pipe func(t *Transaction))
	// SkipTransactions if called then skip the rest of the transactions
	// or all of them if called before the first transaction
	SkipTransactions()
	// TransactionsSkipped returns true if the transactions skipped or canceled at all.
	TransactionsSkipped() bool

	// Exec calls the `context/Application#ServeCtx`
	// based on this context but with a changed method and path
	// like it was requested by the user, but it is not.
	//
	// Offline means that the route is registered to the iris and have all features that a normal route has
	// BUT it isn't available by browsing, its handlers executed only when other handler's context call them
	// it can validate paths, has sessions, path parameters and all.
	//
	// You can find the Route by app.GetRoute("theRouteName")
	// you can set a route name as: myRoute := app.Get("/mypath", handler)("theRouteName")
	// that will set a name to the route and returns its RouteInfo instance for further usage.
	//
	// It doesn't changes the global state, if a route was "offline" it remains offline.
	//
	// app.None(...) and app.GetRoutes().Offline(route)/.Online(route, method)
	//
	// Example: https://github.com/kataras/iris/tree/master/_examples/routing/route-state
	//
	// User can get the response by simple using rec := ctx.Recorder(); rec.Body()/rec.StatusCode()/rec.Header().
	//
	// Context's Values and the Session are kept in order to be able to communicate via the result route.
	//
	// It's for extreme use cases, 99% of the times will never be useful for you.
	Exec(method, path string)

	// RouteExists reports whether a particular route exists
	// It will search from the current subdomain of context's host, if not inside the root domain.
	// 判断当前的context.Application中是否有对应的方法和路径的路由
	RouteExists(method, path string) bool

	// Application returns the iris app instance which belongs to this context.
	// Worth to notice that this function returns an interface
	// of the Application, which contains methods that are safe
	// to be executed at serve-time. The full app's fields
	// and methods are not available here for the developer's safety.
	// 返回当前iris 的 app 实例
	Application() Application

	// String returns the string representation of this request.
	// Each context has a unique string representation.
	// It can be used for simple debugging scenarios, i.e print context as string.
	//
	// What it returns? A number which declares the length of the
	// total `String` calls per executable application, followed
	// by the remote IP (the client) and finally the method:url.
	// 表示当前的 Request 的string
	// 每一个Context有一个唯一的标志
	String() string
}

var _ Context = (*context)(nil)

// Do calls the SetHandlers(handlers)
// and executes the first handler,
// handlers should not be empty.
//
// It's used by the router, developers may use that
// to replace and execute handlers immediately.
// Do的作用是给当前的Context设置全部的handlers，然后执行第一个handler
func Do(ctx Context, handlers Handlers) {
	if len(handlers) > 0 {
		//给当前的context绑定请求路径的路由的Handler
		ctx.SetHandlers(handlers)
		handlers[0](ctx)
	}
}

// LimitRequestBodySize is a middleware which sets a request body size limit
// for all next handlers in the chain.
var LimitRequestBodySize = func(maxRequestBodySizeBytes int64) Handler {
	return func(ctx Context) {
		ctx.SetMaxRequestBodySize(maxRequestBodySizeBytes)
		ctx.Next()
	}
}

// Gzip is a middleware which enables writing
// using gzip compression, if client supports.
var Gzip = func(ctx Context) {
	ctx.Gzip(true)
	ctx.Next()
}

// Map is just a shortcut of the map[string]interface{}.
type Map map[string]interface{}

//  +------------------------------------------------------------+
//  | Context Implementation                                     |
//  +------------------------------------------------------------+

// 这里生成的就是每次HTTP请求当前应用生成的context（上下文环境，估计使用完就会返回）
type context struct {
	// the unique id, it's zero until `String` function is called,
	// it's here to cache the random, unique context's id, although `String`
	// returns more than this.
	// 在String()调用前都是0，通过atomic.AddUint64(&lastCapturedContextID, 1)来实现，所以不会重复
	id uint64

	// the http.ResponseWriter wrapped by custom writer.
	// todo 这个封装最全面的一个接口，需要学习为啥这么做
	writer ResponseWriter

	// the original http.Request
	// 这个是原生的 http.Request
	request *http.Request

	// the current route's name registered to this request path.
	//当前对应的请求路径的路由名称
	currentRouteName string

	// the local key-value storage
	// RequestParams用来用于动态路径用的
	params RequestParams // url named parameters.
	// 问题：这个暂时还不知道什么作用，是一个[]Key-Value结构？？？
	// 解答：这是用来存储中间件之间传输数据的载体
	values memstore.Store // generic storage, middleware communication.

	// the underline application app.
	// 问题：不知道是当前的Application的作用？？
	// 解答：保存的是整个当前运行的Application，任何请求生成的Context都通用这个Application
	app Application

	// the route's handlers
	// 可以说当前路由所绑定的Handlers
	handlers Handlers

	// the current position of the handler's chain
	// 当前处理的handler在handler链中的位置
	// 问题:这里啥时候变更呢？？
	// 通过context.Next()来进行变更，而且表示包含这个索引以及之前的handler都已经调用过了
	currentHandlerIndex int
}

// NewContext returns the default, internal, context implementation.
// You may use this function to embed the default context implementation
// to a custom one.
//
// This context is received by the context pool.
// 在iris.go中的contextPool中返回的context实例
func NewContext(app Application) Context {
	return &context{app: app}
}

// BeginRequest is executing once for each request
// it should prepare the (new or acquired from pool) context's fields for the new request.
//
// To follow the iris' flow, developer should:
// 1. reset handlers to nil
// 2. reset store to empty
// 3. reset sessions to nil
// 4. reset response writer to the http.ResponseWriter
// 5. reset request to the *http.Request
// and any other optional steps, depends on dev's application type.
func (ctx *context) BeginRequest(w http.ResponseWriter, r *http.Request) {
	ctx.handlers = nil           // will be filled by router.Serve/HTTP
	ctx.values = ctx.values[0:0] // >>      >>     by context.Values().Set
	ctx.params.Store = ctx.params.Store[0:0]
	ctx.request = r
	ctx.currentHandlerIndex = 0
	// 这里的writer内在是response_writer.go中的responseWriter struct
	ctx.writer = AcquireResponseWriter()
	// 这里就是初始化了responseWriter的初始数据
	ctx.writer.BeginResponse(w)
}

// StatusCodeNotSuccessful defines if a specific "statusCode" is not
// a valid status code for a successful response.
// It defaults to < 200 || >= 400
//
// Read more at `iris#DisableAutoFireStatusCode`, `iris/core/router#ErrorCodeHandler`
// and `iris/core/router#OnAnyErrorCode` for relative information.
//
// Do NOT change it.
//
// It's exported for extreme situations--special needs only, when the Iris server and the client
// is not following the RFC: https://www.w3.org/Protocols/rfc2616/rfc2616-sec10.html
var StatusCodeNotSuccessful = func(statusCode int) bool {
	return statusCode < 200 || statusCode >= 400
}

// EndRequest is executing once after a response to the request was sent and this context is useless or released.
//
// To follow the iris' flow, developer should:
// 1. flush the response writer's result
// 2. release the response writer
// and any other optional steps, depends on dev's application type.
func (ctx *context) EndRequest() {
	if StatusCodeNotSuccessful(ctx.GetStatusCode()) &&
		!ctx.Application().ConfigurationReadOnly().GetDisableAutoFireStatusCode() {
		// author's note:
		// if recording, the error handler can handle
		// the rollback and remove any response written before,
		// we don't have to do anything here, written is <=0 (-1 for default empty, even no status code)
		// when Recording
		// because we didn't flush the response yet
		// if !recording  then check if the previous handler didn't send something
		// to the client.
		// todo 上面的注释是通过Transaction，想了解的时候再看？？？
		if ctx.writer.Written() <= 0 {
			// Author's notes:
			// previously: == -1,
			// <=0 means even if empty write called which has meaning;
			// rel: core/router/status.go#Fire-else
			// mvc/activator/funcmethod/func_result_dispatcher.go#DispatchCommon-write
			// mvc/response.go#defaultFailureResponse - no text given but
			// status code should be fired, but it couldn't because of the .Write
			// action, the .Written() was 0 even on empty response, this 0 means that
			// a status code given, the previous check of the "== -1" didn't make check for that,
			// we do now.
			// 这里是通过APIBuilder实现了FireErrorCode()，即根据当前的context.ResponseWriter接口里的实现类responseWriter
			// 得到的状态码返回错误信息
			ctx.Application().FireErrorCode(ctx)
		}
	}

	ctx.writer.FlushResponse()
	ctx.writer.EndResponse()
}

// ResponseWriter returns an http.ResponseWriter compatible response writer, as expected.
func (ctx *context) ResponseWriter() ResponseWriter {
	return ctx.writer
}

// ResetResponseWriter should change or upgrade the context's ResponseWriter.
func (ctx *context) ResetResponseWriter(newResponseWriter ResponseWriter) {
	ctx.writer = newResponseWriter
}

// Request returns the original *http.Request, as expected.
func (ctx *context) Request() *http.Request {
	return ctx.request
}

// SetCurrentRouteName sets the route's name internally,
// in order to be able to find the correct current "read-only" Route when
// end-developer calls the `GetCurrentRoute()` function.
// It's being initialized by the Router, if you change that name
// manually nothing really happens except that you'll get other
// route via `GetCurrentRoute()`.
// Instead, to execute a different path
// from this context you should use the `Exec` function
// or change the handlers via `SetHandlers/AddHandler` functions.
func (ctx *context) SetCurrentRouteName(currentRouteName string) {
	ctx.currentRouteName = currentRouteName
}

// GetCurrentRoute returns the current registered "read-only" route that
// was being registered to this request's path.
//返回context
func (ctx *context) GetCurrentRoute() RouteReadOnly {
	return ctx.app.GetRouteReadOnly(ctx.currentRouteName)
}

// Do calls the SetHandlers(handlers)
// and executes the first handler,
// handlers should not be empty.
//
// It's used by the router, developers may use that
// to replace and execute handlers immediately.
// todo 这里只是给当前ctx设置Handlers 而且只执行第一个，啥时候执行接下来的呢?，什么时候调用这个的呢，currentIndex要更新吗？？
func (ctx *context) Do(handlers Handlers) {
	Do(ctx, handlers)
}

// AddHandler can add handler(s)
// to the current request in serve-time,
// these handlers are not persistenced to the router.
//
// Router is calling this function to add the route's handler.
// If AddHandler called then the handlers will be inserted
// to the end of the already-defined route's handler.
//
func (ctx *context) AddHandler(handlers ...Handler) {
	ctx.handlers = append(ctx.handlers, handlers...)
}

// SetHandlers replaces all handlers with the new.
func (ctx *context) SetHandlers(handlers Handlers) {
	ctx.handlers = handlers
}

// Handlers keeps tracking of the current handlers.
func (ctx *context) Handlers() Handlers {
	return ctx.handlers
}

// HandlerIndex sets the current index of the
// current context's handlers chain.
// If -1 passed then it just returns the
// current handler index without change the current index.rns that index, useless return value.
//
// Look Handlers(), Next() and StopExecution() too.
func (ctx *context) HandlerIndex(n int) (currentIndex int) {
	if n < 0 || n > len(ctx.handlers)-1 {
		return ctx.currentHandlerIndex
	}

	ctx.currentHandlerIndex = n
	return n
}

// Proceed is an alternative way to check if a particular handler
// has been executed and called the `ctx.Next` function inside it.
// This is useful only when you run a handler inside
// another handler. It justs checks for before index and the after index.
//
// A usecase example is when you want to execute a middleware
// inside controller's `BeginRequest` that calls the `ctx.Next` inside it.
// The Controller looks the whole flow (BeginRequest, method handler, EndRequest)
// as one handler, so `ctx.Next` will not be reflected to the method handler
// if called from the `BeginRequest`.
//
// Although `BeginRequest` should NOT be used to call other handlers,
// the `BeginRequest` has been introduced to be able to set
// common data to all method handlers before their execution.
// Controllers can accept middleware(s) from the MVC's Application's Router as normally.
//
// That said let's see an example of `ctx.Proceed`:
//
// var authMiddleware = basicauth.New(basicauth.Config{
// 	Users: map[string]string{
// 		"admin": "password",
// 	},
// })
//
// func (c *UsersController) BeginRequest(ctx iris.Context) {
// 	if !ctx.Proceed(authMiddleware) {
// 		ctx.StopExecution()
// 	}
// }
// This Get() will be executed in the same handler as `BeginRequest`,
// internally controller checks for `ctx.StopExecution`.
// So it will not be fired if BeginRequest called the `StopExecution`.
// func(c *UsersController) Get() []models.User {
//	  return c.Service.GetAll()
//}
// Alternative way is `!ctx.IsStopped()` if middleware make use of the `ctx.StopExecution()` on failure.
// 大部分在apply(handlers)中的handlers封装了!ctx.Proceed(),然后再ctx.Next()
func (ctx *context) Proceed(h Handler) bool {
	beforeIdx := ctx.currentHandlerIndex
	h(ctx)
	if ctx.currentHandlerIndex > beforeIdx && !ctx.IsStopped() {
		return true
	}
	return false
}

// HandlerName returns the current handler's name, helpful for debugging.
func (ctx *context) HandlerName() string {
	return HandlerName(ctx.handlers[ctx.currentHandlerIndex])
}

// Next is the function that executed when `ctx.Next()` is called.
// It can be changed to a customized one if needed (very advanced usage).
//
// See `DefaultNext` for more information about this and why it's exported like this.
var Next = DefaultNext

// DefaultNext is the default function that executed on each middleware if `ctx.Next()`
// is called.
//
// DefaultNext calls the next handler from the handlers chain by registration order,
// it should be used inside a middleware.
//
// It can be changed to a customized one if needed (very advanced usage).
//
// Developers are free to customize the whole or part of the Context's implementation
// by implementing a new `context.Context` (see https://github.com/kataras/iris/tree/master/_examples/routing/custom-context)
// or by just override the `context.Next` package-level field, `context.DefaultNext` is exported
// in order to be able for developers to merge your customized version one with the default behavior as well.
func DefaultNext(ctx Context) {
	if ctx.IsStopped() {
		return
	}
	if n, handlers := ctx.HandlerIndex(-1)+1, ctx.Handlers(); n < len(handlers) {
		ctx.HandlerIndex(n)
		handlers[n](ctx)
	}
}

// Next calls all the next handler from the handlers chain,
// it should be used inside a middleware.
//
// Note: Custom context should override this method in order to be able to pass its own context.Context implementation.
// 问题:这个什么时候被调用呢？？(这个用Context interface 的Next()查询会更好)
// 解答：在当前context需要调用handler链下面一个handler的时候调用
func (ctx *context) Next() { // or context.Next(ctx)
	// Next=DefaultNext，DefaultNext的作用就是让currentHandlerIndex+1，然后对应位置的Handler调用
	Next(ctx)
}

// NextOr checks if chain has a next handler, if so then it executes it
// otherwise it sets a new chain assigned to this Context based on the given handler(s)
// and executes its first handler.
//
// Returns true if next handler exists and executed, otherwise false.
//
// Note that if no next handler found and handlers are missing then
// it sends a Status Not Found (404) to the client and it stops the execution.
// 判断保证之前设置的Handler链中是否还有下一个，如果有则返回true，
// 如果没有, 当参数长度为0的时候，则返回false，会调用ctx.NotFound()和ctx.StopExecution(),表示请求头为404而且也把currentHandlerIndex 设置为-1表示没有接下来的handler了
// 如果参数不为0，则重新设置handers，并执行第一个，然而这里没有重新设置currentHandlerIndex(该方法也只有在NextOrNotFound()一处调用，而且此时没有传参，所以这情况不会出现，直接再第二情况就返回了)
func (ctx *context) NextOr(handlers ...Handler) bool {
	// 这里是保证旧的能继续执行
	if next := ctx.NextHandler(); next != nil { //如果有下一个，则直接执行下一个
		next(ctx)
		ctx.Skip() // skip this handler from the chain.
		return true
	}
	// 这里表示的ctx.NotFound()就表示将请求头转换为404
	if len(handlers) == 0 {
		ctx.NotFound()
		ctx.StopExecution()
		return false
	}
	//todo 如果没有，则设置新的handlers，然后执行第一个,不过这里并没有更新currentIndex，而且也重新设置了Handlers，为啥不更新currentIndex？？更觉得是NextOr()这个接口还没有开放
	ctx.Do(handlers)

	return false
}

// NextOrNotFound checks if chain has a next handler, if so then it executes it
// otherwise it sends a Status Not Found (404) to the client and stops the execution.
//
// Returns true if next handler exists and executed, otherwise false.
// 判断原来的Handler链中是否还有为处理的Handler
// todo 什么时候调用这个接口？？个人觉得这个接口也没有开放
func (ctx *context) NextOrNotFound() bool { return ctx.NextOr() }

// NextHandler returns (it doesn't execute) the next handler from the handlers chain.
//
// Use .Skip() to skip this handler if needed to execute the next of this returning handler.
// 用NextHandler可以判断接下来是否还有没使用的Handler
// 问题：这里为啥不需要锁呢？？
// 解答：因为每一个Request请求都会单独的从cpool生成一个Context，都是独立的
func (ctx *context) NextHandler() Handler {
	if ctx.IsStopped() {
		return nil
	}
	nextIndex := ctx.currentHandlerIndex + 1
	// check if it has a next middleware
	if nextIndex < len(ctx.handlers) {
		return ctx.handlers[nextIndex]
	}
	return nil
}

// Skip skips/ignores the next handler from the handlers chain,
// it should be used inside a middleware.
// 这里的前提就已经保证了next!=nil的
func (ctx *context) Skip() {
	// 设置当前的Handler在Handler链的位置
	ctx.HandlerIndex(ctx.currentHandlerIndex + 1)
}

const stopExecutionIndex = -1 // I don't set to a max value because we want to be able to reuse the handlers even if stopped with .Skip

// StopExecution if called then the following .Next calls are ignored,
// as a result the next handlers in the chain will not be fire.
func (ctx *context) StopExecution() {
	ctx.currentHandlerIndex = stopExecutionIndex
}

// IsStopped checks and returns true if the current position of the context is -1,
// means that the StopExecution() was called.
func (ctx *context) IsStopped() bool {
	return ctx.currentHandlerIndex == stopExecutionIndex
}

// OnConnectionClose registers the "cb" function which will fire (on its own goroutine, no need to be registered goroutine by the end-dev)
// when the underlying connection has gone away.
// OnConnectionCLose 注册一个回调函数，这个回调函数会在链接断开的时候执行（而且自己生成一个协程）
//
// This mechanism can be used to cancel long operations on the server
// if the client has disconnected before the response is ready.
// 这个机制可以被用在取消长操作，比如在应答前客户端以及取消链接了
//
// It depends on the `http#CloseNotify`.
// CloseNotify may wait to notify until Request.Body has been
// fully read.
// 这个取决于CloseNotify，CloseNotify等请求体被全部读取完后去notify
// todo CloseNotify去notify什么？？
//
// After the main Handler has returned, there is no guarantee
// that the channel receives a value.
// 当mainHandler全部返回，通过也没法保证有接收到值
//
// Finally, it reports whether the protocol supports pipelines (HTTP/1.1 with pipelines disabled is not supported).
// The "cb" will not fire for sure if the output value is false.
//
// Note that you can register only one callback for the entire request handler chain/per route.
//
// Look the `ResponseWriter#CloseNotifier` for more.
// todo 看context#CloseNotifier如何实现？？？
func (ctx *context) OnConnectionClose(cb func()) bool {
	// Note that `ctx.ResponseWriter().CloseNotify()` can already do the same
	// but it returns a channel which will never fire if it the protocol version is not compatible,
	// here we don't want to allocate an empty channel, just skip it.
	// todo 有关Notifier这里的机制并不了解？？？需要学习
	notifier, ok := ctx.writer.CloseNotifier()
	if !ok {
		return false
	}

	notify := notifier.CloseNotify()
	// 这里自己开了一个协程去接数据，等有notify然后调用回调函数
	go func() {
		<-notify
		if cb != nil {
			cb()
		}
	}()

	return true
}

// OnClose registers the callback function "cb" to the underline connection closing event using the `Context#OnConnectionClose`
// and also in the end of the request handler using the `ResponseWriter#SetBeforeFlush`.
// Note that you can register only one callback for the entire request handler chain/per route.
//
// Look the `Context#OnConnectionClose` and `ResponseWriter#SetBeforeFlush` for more.
func (ctx *context) OnClose(cb func()) {
	if cb == nil {
		return
	}

	// Register the on underline connection close handler first.
	ctx.OnConnectionClose(cb)

	// Author's notes:
	// This is fired on `ctx.ResponseWriter().FlushResponse()` which is fired by the framework automatically, internally, on the end of request handler(s),
	// it is not fired on the underline streaming function of the writer: `ctx.ResponseWriter().Flush()` (which can be fired more than one if streaming is supported by the client).
	// The `FlushResponse` is called only once, so add the "cb" here, no need to add done request handlers each time `OnClose` is called by the end-dev.
	//
	// Don't allow more than one because we don't allow that on `OnConnectionClose` too:
	// old := ctx.writer.GetBeforeFlush()
	// if old != nil {
	// 	ctx.writer.SetBeforeFlush(func() {
	// 		old()
	// 		cb()
	// 	})
	// 	return
	// }
	// 这就是设置 FlushResponse() 的时候要调用的函数回调
	ctx.writer.SetBeforeFlush(cb)
}

//  +------------------------------------------------------------+
//  | Current "user/request" storage                             |
//  | and share information between the handlers - Values().     |
//  | Save and get named path parameters - Params()              |
//  +------------------------------------------------------------+

// Params returns the current url's named parameters key-value storage.
// Named path parameters are being saved here.
// This storage, as the whole context, is per-request lifetime.
func (ctx *context) Params() *RequestParams {
	return &ctx.params
}

// Values returns the current "user" storage.
// Named path parameters and any optional data can be saved here.
// This storage, as the whole context, is per-request lifetime.
//
// You can use this function to Set and Get local values
// that can be used to share information between handlers and middleware.
func (ctx *context) Values() *memstore.Store {
	return &ctx.values
}

// Translate is the i18n (localization) middleware's function,
// it calls the Get("translate") to return the translated value.
//
// Example: https://github.com/kataras/iris/tree/master/_examples/miscellaneous/i18n
func (ctx *context) Translate(format string, args ...interface{}) string {
	if cb, ok := ctx.values.Get(ctx.Application().ConfigurationReadOnly().GetTranslateFunctionContextKey()).(func(format string, args ...interface{}) string); ok {
		return cb(format, args...)
	}

	return ""
}

//  +------------------------------------------------------------+
//  | Path, Host, Subdomain, IP, Headers etc...                  |
//  +------------------------------------------------------------+

// Method returns the request.Method, the client's http method to the server.
func (ctx *context) Method() string {
	return ctx.request.Method
}

// Path returns the full request path,
// escaped if EnablePathEscape config field is true.
func (ctx *context) Path() string {
	return ctx.RequestPath(ctx.Application().ConfigurationReadOnly().GetEnablePathEscape())
}

// DecodeQuery returns the uri parameter as url (string)
// useful when you want to pass something to a database and be valid to retrieve it via context.Param
// use it only for special cases, when the default behavior doesn't suits you.
//
// http://www.blooberry.com/indexdot/html/topics/urlencoding.htm
// it uses just the url.QueryUnescape
// 可以看上面的网址来知道哪些需要Encoding
func DecodeQuery(path string) string {
	if path == "" {
		return ""
	}
	encodedPath, err := url.QueryUnescape(path)
	if err != nil {
		return path
	}
	return encodedPath
}

// DecodeURL returns the decoded uri
// useful when you want to pass something to a database and be valid to retrieve it via context.Param
// use it only for special cases, when the default behavior doesn't suits you.
//
// http://www.blooberry.com/indexdot/html/topics/urlencoding.htm
// it uses just the url.Parse
func DecodeURL(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.String()
}

// RequestPath returns the full request path,
// based on the 'escape'.
func (ctx *context) RequestPath(escape bool) string {
	if escape {
		return DecodeQuery(ctx.request.URL.EscapedPath())
	}
	return ctx.request.URL.Path // RawPath returns empty, requesturi can be used instead also.
}

// PathPrefixMap accepts a map of string and a handler.
// The key of "m" is the key, which is the prefix, regular expressions are not valid.
// The value of "m" is the handler that will be executed if HasPrefix(context.Path).
// func (ctx *context) PathPrefixMap(m map[string]context.Handler) bool {
// 	path := ctx.Path()
// 	for k, v := range m {
// 		if strings.HasPrefix(path, k) {
// 			v(ctx)
// 			return true
// 		}
// 	}
// 	return false
// } no, it will not work because map is a random peek data structure.

// Host returns the host part of the current URI.
func (ctx *context) Host() string {
	return GetHost(ctx.request)
}

// GetHost returns the host part of the current URI.
func GetHost(r *http.Request) string {
	// 返回的是原生 request.go 中 Request 的 URL 字段中的host部分
	h := r.URL.Host
	if h == "" {
		h = r.Host
	}
	return h
}

// Subdomain returns the subdomain of this request, if any.
// Note that this is a fast method which does not cover all cases.
// todo  这里没有地方调用，不理解这个方法的作用
func (ctx *context) Subdomain() (subdomain string) {
	host := ctx.Host()
	// SubDomain() 返回的是第一个'.'之前的
	// todo 问题：那如果是www.baidu.com 那子域名是www ？？？这里的子域名与平时的理解的有些不一样Ω
	if index := strings.IndexByte(host, '.'); index > 0 {
		subdomain = host[0:index]
	}

	// listening on mydomain.com:80
	// subdomain = mydomain, but it's wrong, it should return ""
	// todo 问题：这个虚拟host是什么？？？？ iris中addr.go文件中ResolveVHost(addr string) string 方法
	// 解答：就是处理一些特殊的addr，这里的addr是iris.Addr()里面执行的，比如0.0.0.0之类的，以及:https之类的额
	vhost := ctx.Application().ConfigurationReadOnly().GetVHost()
	// 如果虚拟host包含了子域名，则直接返回""，说明vhost是专门处理一些特殊的域名的
	// todo 问题：为啥这样就不算""？？
	if strings.Contains(vhost, subdomain) { // then it's not subdomain
		return ""
	}

	return
}

// IsWWW returns true if the current subdomain (if any) is www.
func (ctx *context) IsWWW() bool {
	host := ctx.Host()
	if index := strings.IndexByte(host, '.'); index > 0 {
		// if it has a subdomain and it's www then return true.
		// todo 这里也出现了VHost与subdomain的关系，必须不包含？？？
		if subdomain := host[0:index]; !strings.Contains(ctx.Application().ConfigurationReadOnly().GetVHost(), subdomain) {
			return subdomain == "www"
		}
	}
	return false
}

const xForwardedForHeaderKey = "X-Forwarded-For"

// RemoteAddr tries to parse and return the real client's request IP.
//
// Based on allowed headers names that can be modified from Configuration.RemoteAddrHeaders.
//
// If parse based on these headers fail then it will return the Request's `RemoteAddr` field
// which is filled by the server before the HTTP handler.
//
// Look `Configuration.RemoteAddrHeaders`,
//      `Configuration.WithRemoteAddrHeader(...)`,
//      `Configuration.WithoutRemoteAddrHeader(...)` for more.
func (ctx *context) RemoteAddr() string {
	remoteHeaders := ctx.Application().ConfigurationReadOnly().GetRemoteAddrHeaders()

	for headerName, enabled := range remoteHeaders {
		if enabled {
			headerValue := ctx.GetHeader(headerName)
			// exception needed for 'X-Forwarded-For' only , if enabled.
			if headerName == xForwardedForHeaderKey {
				idx := strings.IndexByte(headerValue, ',')
				if idx >= 0 {
					headerValue = headerValue[0:idx]
				}
			}

			realIP := strings.TrimSpace(headerValue)
			if realIP != "" {
				return realIP
			}
		}
	}

	addr := strings.TrimSpace(ctx.request.RemoteAddr)
	if addr != "" {
		// if addr has port use the net.SplitHostPort otherwise(error occurs) take as it is
		if ip, _, err := net.SplitHostPort(addr); err == nil {
			return ip
		}
	}

	return addr
}

// GetHeader returns the request header's value based on its name.
func (ctx *context) GetHeader(name string) string {
	return ctx.request.Header.Get(name)
}

// IsAjax returns true if this request is an 'ajax request'( XMLHttpRequest)
//
// There is no a 100% way of knowing that a request was made via Ajax.
// You should never trust data coming from the client, they can be easily overcome by spoofing.
//
// Note that "X-Requested-With" Header can be modified by any client(because of "X-"),
// so don't rely on IsAjax for really serious stuff,
// try to find another way of detecting the type(i.e, content type),
// there are many blogs that describe these problems and provide different kind of solutions,
// it's always depending on the application you're building,
// this is the reason why this `IsAjax`` is simple enough for general purpose use.
//
// Read more at: https://developer.mozilla.org/en-US/docs/AJAX
// and https://xhr.spec.whatwg.org/
func (ctx *context) IsAjax() bool {
	return ctx.GetHeader("X-Requested-With") == "XMLHttpRequest"
}

var isMobileRegex = regexp.MustCompile(`(?i)(android|avantgo|blackberry|bolt|boost|cricket|docomo|fone|hiptop|mini|mobi|palm|phone|pie|tablet|up\.browser|up\.link|webos|wos)`)

// IsMobile checks if client is using a mobile device(phone or tablet) to communicate with this server.
// If the return value is true that means that the http client using a mobile
// device to communicate with the server, otherwise false.
//
// Keep note that this checks the "User-Agent" request header.
func (ctx *context) IsMobile() bool {
	s := ctx.GetHeader("User-Agent")
	return isMobileRegex.MatchString(s)
}

type (
	// Referrer contains the extracted information from the `GetReferrer`
	//
	// The structure contains struct tags for JSON, form, XML, YAML and TOML.
	// Look the `GetReferrer() Referrer` and `goreferrer` external package.
	Referrer struct {
		Type       ReferrerType             `json:"type" form:"referrer_type" xml:"Type" yaml:"Type" toml:"Type"`
		Label      string                   `json:"label" form:"referrer_form" xml:"Label" yaml:"Label" toml:"Label"`
		URL        string                   `json:"url" form:"referrer_url" xml:"URL" yaml:"URL" toml:"URL"`
		Subdomain  string                   `json:"subdomain" form:"referrer_subdomain" xml:"Subdomain" yaml:"Subdomain" toml:"Subdomain"`
		Domain     string                   `json:"domain" form:"referrer_domain" xml:"Domain" yaml:"Domain" toml:"Domain"`
		Tld        string                   `json:"tld" form:"referrer_tld" xml:"Tld" yaml:"Tld" toml:"Tld"`
		Path       string                   `json:"path" form:"referrer_path" xml:"Path" yaml:"Path" toml:"Path"`
		Query      string                   `json:"query" form:"referrer_query" xml:"Query" yaml:"Query" toml:"GoogleType"`
		GoogleType ReferrerGoogleSearchType `json:"googleType" form:"referrer_google_type" xml:"GoogleType" yaml:"GoogleType" toml:"GoogleType"`
	}

	// ReferrerType is the goreferrer enum for a referrer type (indirect, direct, email, search, social).
	ReferrerType int

	// ReferrerGoogleSearchType is the goreferrer enum for a google search type (organic, adwords).
	ReferrerGoogleSearchType int
)

// Contains the available values of the goreferrer enums.
const (
	ReferrerInvalid ReferrerType = iota
	ReferrerIndirect
	ReferrerDirect
	ReferrerEmail
	ReferrerSearch
	ReferrerSocial

	ReferrerNotGoogleSearch ReferrerGoogleSearchType = iota
	ReferrerGoogleOrganicSearch
	ReferrerGoogleAdwords
)

func (gs ReferrerGoogleSearchType) String() string {
	return goreferrer.GoogleSearchType(gs).String()
}

func (r ReferrerType) String() string {
	return goreferrer.ReferrerType(r).String()
}

// unnecessary but good to know the default values upfront.
var emptyReferrer = Referrer{Type: ReferrerInvalid, GoogleType: ReferrerNotGoogleSearch}

// GetReferrer extracts and returns the information from the "Referer" header as specified
// in https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Referrer-Policy
// or by the URL query parameter "referer".
func (ctx *context) GetReferrer() Referrer {
	// the underline net/http follows the https://tools.ietf.org/html/rfc7231#section-5.5.2,
	// so there is nothing special left to do.
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Referrer-Policy
	refURL := ctx.GetHeader("Referer")
	if refURL == "" {
		refURL = ctx.URLParam("referer")
	}

	if ref := goreferrer.DefaultRules.Parse(refURL); ref.Type > goreferrer.Invalid {
		return Referrer{
			Type:       ReferrerType(ref.Type),
			Label:      ref.Label,
			URL:        ref.URL,
			Subdomain:  ref.Subdomain,
			Domain:     ref.Domain,
			Tld:        ref.Tld,
			Path:       ref.Path,
			Query:      ref.Query,
			GoogleType: ReferrerGoogleSearchType(ref.GoogleType),
		}
	}

	return emptyReferrer
}

//  +------------------------------------------------------------+
//  | Response Headers helpers                                   |
//  +------------------------------------------------------------+

// Header adds a header to the response, if value is empty
// it removes the header by its name.
// 这里是增删请求头
func (ctx *context) Header(name string, value string) {
	if value == "" {
		ctx.writer.Header().Del(name)
		return
	}
	ctx.writer.Header().Add(name, value)
}

// ContentType sets the response writer's header key "Content-Type" to the 'cType'.
func (ctx *context) ContentType(cType string) {
	if cType == "" {
		return
	}

	// 1. if it's path or a filename or an extension,
	// then take the content type from that
	// 可以通过文件名或者后缀名生成Content-Type 的值比如在当前文件中ServeFile()的使用
	if strings.Contains(cType, ".") {
		ext := filepath.Ext(cType)
		cType = mime.TypeByExtension(ext)
	}
	// if doesn't contain a charset already then append it
	if !strings.Contains(cType, "charset") {
		if cType != ContentBinaryHeaderValue {
			cType += "; charset=" + ctx.Application().ConfigurationReadOnly().GetCharset()
		}
	}

	ctx.writer.Header().Set(ContentTypeHeaderKey, cType)
}

// GetContentType returns the response writer's header value of "Content-Type"
// which may, setted before with the 'ContentType'.
func (ctx *context) GetContentType() string {
	return ctx.writer.Header().Get(ContentTypeHeaderKey)
}

// GetContentType returns the request's header value of "Content-Type".
func (ctx *context) GetContentTypeRequested() string {
	return ctx.GetHeader(ContentTypeHeaderKey)
}

// GetContentLength returns the request's header value of "Content-Length".
// Returns 0 if header was unable to be found or its value was not a valid number.
func (ctx *context) GetContentLength() int64 {
	if v := ctx.GetHeader(ContentLengthHeaderKey); v != "" {
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

// StatusCode sets the status code header to the response.
// Look .GetStatusCode & .FireStatusCode too.
//
// Remember, the last one before .Write matters except recorder and transactions.
func (ctx *context) StatusCode(statusCode int) {
	ctx.writer.WriteHeader(statusCode)
}

// NotFound emits an error 404 to the client, using the specific custom error error handler.
// Note that you may need to call ctx.StopExecution() if you don't want the next handlers
// to be executed. Next handlers are being executed on iris because you can alt the
// error code and change it to a more specific one, i.e
// users := app.Party("/users")
// users.Done(func(ctx context.Context){ if ctx.StatusCode() == 400 { /*  custom error code for /users */ }})
func (ctx *context) NotFound() {
	ctx.StatusCode(http.StatusNotFound)
}

// GetStatusCode returns the current status code of the response.
// Look StatusCode too.
func (ctx *context) GetStatusCode() int {
	return ctx.writer.StatusCode()
}

//  +------------------------------------------------------------+
//  | Various Request and Post Data                              |
//  +------------------------------------------------------------+

// URLParam returns true if the url parameter exists, otherwise false.
func (ctx *context) URLParamExists(name string) bool {
	// Query()返回的是原生 url.go 中的 Values 数据类型（type Values map[string][]string）
	if q := ctx.request.URL.Query(); q != nil {
		_, exists := q[name]
		return exists
	}

	return false
}

// URLParamDefault returns the get parameter from a request, if not found then "def" is returned.
func (ctx *context) URLParamDefault(name string, def string) string {
	if v := ctx.request.URL.Query().Get(name); v != "" {
		return v
	}

	return def
}

// URLParam returns the get parameter from a request, if any.
func (ctx *context) URLParam(name string) string {
	return ctx.URLParamDefault(name, "")
}

// URLParamTrim returns the url query parameter with trailing white spaces removed from a request.
func (ctx *context) URLParamTrim(name string) string {
	return strings.TrimSpace(ctx.URLParam(name))
}

// URLParamTrim returns the escaped url query parameter from a request.
// 将一些东西进行编码，为了安全起见，比如xss攻击，编码后会安全，而这里接收到编码的数据，然后进行解码，拿到原始数据
func (ctx *context) URLParamEscape(name string) string {
	return DecodeQuery(ctx.URLParam(name))
}

var errURLParamNotFound = errors.New("url param '%s' does not exist")

// URLParamInt returns the url query parameter as int value from a request,
// returns -1 and an error if parse failed or not found.
func (ctx *context) URLParamInt(name string) (int, error) {
	if v := ctx.URLParam(name); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return -1, err
		}
		return n, nil
	}

	return -1, errURLParamNotFound.Format(name)
}

// URLParamIntDefault returns the url query parameter as int value from a request,
// if not found or parse failed then "def" is returned.
func (ctx *context) URLParamIntDefault(name string, def int) int {
	v, err := ctx.URLParamInt(name)
	if err != nil {
		return def
	}

	return v
}

// URLParamInt32Default returns the url query parameter as int32 value from a request,
// if not found or parse failed then "def" is returned.
func (ctx *context) URLParamInt32Default(name string, def int32) int32 {
	if v := ctx.URLParam(name); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			return def
		}

		return int32(n)
	}

	return def
}

// URLParamInt64 returns the url query parameter as int64 value from a request,
// returns -1 and an error if parse failed or not found.
func (ctx *context) URLParamInt64(name string) (int64, error) {
	if v := ctx.URLParam(name); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return -1, err
		}
		return n, nil
	}

	return -1, errURLParamNotFound.Format(name)
}

// URLParamInt64Default returns the url query parameter as int64 value from a request,
// if not found or parse failed then "def" is returned.
func (ctx *context) URLParamInt64Default(name string, def int64) int64 {
	v, err := ctx.URLParamInt64(name)
	if err != nil {
		return def
	}

	return v
}

// URLParamFloat64 returns the url query parameter as float64 value from a request,
// returns an error and -1 if parse failed.
func (ctx *context) URLParamFloat64(name string) (float64, error) {
	if v := ctx.URLParam(name); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return -1, err
		}
		return n, nil
	}

	return -1, errURLParamNotFound.Format(name)
}

// URLParamFloat64Default returns the url query parameter as float64 value from a request,
// if not found or parse failed then "def" is returned.
func (ctx *context) URLParamFloat64Default(name string, def float64) float64 {
	v, err := ctx.URLParamFloat64(name)
	if err != nil {
		return def
	}

	return v
}

// URLParamBool returns the url query parameter as boolean value from a request,
// returns an error if parse failed.
func (ctx *context) URLParamBool(name string) (bool, error) {
	return strconv.ParseBool(ctx.URLParam(name))
}

// URLParams returns a map of GET query parameters separated by comma if more than one
// it returns an empty map if nothing found.
func (ctx *context) URLParams() map[string]string {
	values := map[string]string{}

	q := ctx.request.URL.Query()
	if q != nil {
		for k, v := range q {
			values[k] = strings.Join(v, ",")
		}
	}

	return values
}

// No need anymore, net/http checks for the Form already.
// func (ctx *context) askParseForm() error {
// 	if ctx.request.Form == nil {
// 		if err := ctx.request.ParseForm(); err != nil {
// 			return err
// 		}
// 	}
// 	return nil
// }

// FormValueDefault returns a single parsed form value by its "name",
// including both the URL field's query parameters and the POST or PUT form data.
//
// Returns the "def" if not found.
func (ctx *context) FormValueDefault(name string, def string) string {
	if form, has := ctx.form(); has {
		if v := form[name]; len(v) > 0 {
			return v[0]
		}
	}
	return def
}

// FormValue returns a single parsed form value by its "name",
// including both the URL field's query parameters and the POST or PUT form data.
func (ctx *context) FormValue(name string) string {
	return ctx.FormValueDefault(name, "")
}

// FormValues returns the parsed form data, including both the URL
// field's query parameters and the POST or PUT form data.
//
// The default form's memory maximum size is 32MB, it can be changed by the
// `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
// NOTE: A check for nil is necessary.
func (ctx *context) FormValues() map[string][]string {
	form, _ := ctx.form()
	return form
}

// Form contains the parsed form data, including both the URL
// field's query parameters and the POST or PUT form data.
func (ctx *context) form() (form map[string][]string, found bool) {
	/*
		net/http/request.go#1219
		for k, v := range f.Value {
			r.Form[k] = append(r.Form[k], v...)
			// r.PostForm should also be populated. See Issue 9305.
			r.PostForm[k] = append(r.PostForm[k], v...)
		}
	*/

	// ParseMultipartForm calls `request.ParseForm` automatically
	// therefore we don't need to call it here, although it doesn't hurt.
	// After one call to ParseMultipartForm or ParseForm,
	// subsequent calls have no effect, are idempotent.
	// 由于 ParseMultipartForm() 内部也会自动调用 request.ParseForm，所以调用这个足矣
	// todo 阅读原生的 request.go ParseMultipartForm(maxMemory int64) 方法？？？
	ctx.request.ParseMultipartForm(ctx.Application().ConfigurationReadOnly().GetPostMaxMemory())

	//  顺序 reuqest.Form -> request.PostForm -> request.MultipartForm
	// todo 问题:Form、PostForm、MultipartForm什么区别？？？
	if form := ctx.request.Form; len(form) > 0 {
		return form, true
	}

	if form := ctx.request.PostForm; len(form) > 0 {
		return form, true
	}

	if m := ctx.request.MultipartForm; m != nil {
		// todo multipartForm 中的 Value什么用？？
		if len(m.Value) > 0 {
			return m.Value, true
		}
	}

	return nil, false
}

// PostValueDefault returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name".
//
// If not found then "def" is returned instead.
func (ctx *context) PostValueDefault(name string, def string) string {
	ctx.form()
	if v := ctx.request.PostForm[name]; len(v) > 0 {
		return v[0]
	}
	return def
}

// PostValue returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name"
func (ctx *context) PostValue(name string) string {
	return ctx.PostValueDefault(name, "")
}

// PostValueTrim returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name",  without trailing spaces.
func (ctx *context) PostValueTrim(name string) string {
	return strings.TrimSpace(ctx.PostValue(name))
}

var errUnableToFindPostValue = errors.New("unable to find post value '%s'")

// PostValueInt returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name", as int.
//
// If not found returns -1 and a non-nil error.
func (ctx *context) PostValueInt(name string) (int, error) {
	v := ctx.PostValue(name)
	if v == "" {
		return -1, errUnableToFindPostValue.Format(name)
	}
	return strconv.Atoi(v)
}

// PostValueIntDefault returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name", as int.
//
// If not found or parse errors returns the "def".
func (ctx *context) PostValueIntDefault(name string, def int) int {
	if v, err := ctx.PostValueInt(name); err == nil {
		return v
	}

	return def
}

// PostValueInt64 returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name", as float64.
//
// If not found returns -1 and a non-nil error.
func (ctx *context) PostValueInt64(name string) (int64, error) {
	v := ctx.PostValue(name)
	if v == "" {
		return -1, errUnableToFindPostValue.Format(name)
	}
	return strconv.ParseInt(v, 10, 64)
}

// PostValueInt64Default returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name", as int64.
//
// If not found or parse errors returns the "def".
func (ctx *context) PostValueInt64Default(name string, def int64) int64 {
	if v, err := ctx.PostValueInt64(name); err == nil {
		return v
	}

	return def
}

// PostValueInt64Default returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name", as float64.
//
// If not found returns -1 and a non-nil error.
func (ctx *context) PostValueFloat64(name string) (float64, error) {
	v := ctx.PostValue(name)
	if v == "" {
		return -1, errUnableToFindPostValue.Format(name)
	}
	return strconv.ParseFloat(v, 64)
}

// PostValueInt64Default returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name", as float64.
//
// If not found or parse errors returns the "def".
func (ctx *context) PostValueFloat64Default(name string, def float64) float64 {
	if v, err := ctx.PostValueFloat64(name); err == nil {
		return v
	}

	return def
}

// PostValueInt64Default returns the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name", as bool.
//
// If not found or value is false, then it returns false, otherwise true.
func (ctx *context) PostValueBool(name string) (bool, error) {
	v := ctx.PostValue(name)
	if v == "" {
		return false, errUnableToFindPostValue.Format(name)
	}

	return strconv.ParseBool(v)
}

// PostValues returns all the parsed form data from POST, PATCH,
// or PUT body parameters based on a "name" as a string slice.
//
// The default form's memory maximum size is 32MB, it can be changed by the
// `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
func (ctx *context) PostValues(name string) []string {
	ctx.form()
	return ctx.request.PostForm[name]
}

// FormFile returns the first uploaded file that received from the client.
//
//
// The default form's memory maximum size is 32MB, it can be changed by the
// `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
//
// Example: https://github.com/kataras/iris/tree/master/_examples/http_request/upload-file
func (ctx *context) FormFile(key string) (multipart.File, *multipart.FileHeader, error) {
	// we don't have access to see if the request is body stream
	// and then the ParseMultipartForm can be useless
	// here but do it in order to apply the post limit,
	// the internal request.FormFile will not do it if that's filled
	// and it's not a stream body.
	if err := ctx.request.ParseMultipartForm(ctx.Application().ConfigurationReadOnly().GetPostMaxMemory()); err != nil {
		return nil, nil, err
	}

	return ctx.request.FormFile(key)
}

// UploadFormFiles uploads any received file(s) from the client
// to the system physical location "destDirectory".
// 这是将客户端上传的图片 保存到磁盘中
//
// The second optional argument "before" gives caller the chance to
// modify the *miltipart.FileHeader before saving to the disk,
// it can be used to change a file's name based on the current request,
// all FileHeader's options can be changed. You can ignore it if
// you don't need to use this capability before saving a file to the disk.
// 参数 before 是用来将文件上传到指定磁盘时候，可以让其多一步操作
//
// Note that it doesn't check if request body streamed.
// 如果是请求流，则不用检查
// todo 问题：这里的请求流是什么意思？？，检查什么呢？？
//
// Returns the copied length as int64 and
// a not nil error if at least one new file
// can't be created due to the operating system's permissions or
// http.ErrMissingFile if no file received.
//
// If you want to receive & accept files and manage them manually you can use the `context#FormFile`
// instead and create a copy function that suits your needs, the below is for generic usage.
// 如果想手动处理文件流，则可以用上面的 FormFile() ，UploadFormFiles是通用的处理方式
//
// The default form's memory maximum size is 32MB, it can be changed by the
//  `iris#WithPostMaxMemory` configurator at main configuration passed on `app.Run`'s second argument.
//
// See `FormFile` to a more controlled to receive a file.
//
// Example: https://github.com/kataras/iris/tree/master/_examples/http_request/upload-files
func (ctx *context) UploadFormFiles(destDirectory string, before ...func(Context, *multipart.FileHeader)) (n int64, err error) {
	err = ctx.request.ParseMultipartForm(ctx.Application().ConfigurationReadOnly().GetPostMaxMemory())
	if err != nil {
		return 0, err
	}
	// 通过ctx.Request.MultipartForm来寻找文件数据
	if ctx.request.MultipartForm != nil {
		// 下面 MultipartForm.File 的 File 字段的数据类型是 map[string][]*FileHeader
		if fhs := ctx.request.MultipartForm.File; fhs != nil {
			for _, files := range fhs {
				for _, file := range files {

					for _, b := range before {
						b(ctx, file)
					}
					// 这里才是实际的上传文件的接口
					// todo FileHeader 结构？？
					// 内部实际就是通过io.Copy()来进行拷贝
					n0, err0 := uploadTo(file, destDirectory)
					// 有一个失败就直接为0
					if err0 != nil {
						return 0, err0
					}
					n += n0
				}
			}
			return n, nil
		}
	}

	return 0, http.ErrMissingFile
}

// todo 学习原生multipart.FileHeader 源码
func uploadTo(fh *multipart.FileHeader, destDirectory string) (int64, error) {
	src, err := fh.Open()
	if err != nil {
		return 0, err
	}
	// 记得打开文件记得关闭
	defer src.Close()

	out, err := os.OpenFile(filepath.Join(destDirectory, fh.Filename),
		os.O_WRONLY|os.O_CREATE, os.FileMode(0666))

	if err != nil {
		return 0, err
	}
	defer out.Close()
	// 通过io.Copy来复制文件
	// todo io.Copy() 源码阅读
	return io.Copy(out, src)
}

// Redirect sends a redirect response to the client
// to a specific url or relative path.
// accepts 2 parameters string and an optional int
// first parameter is the url to redirect
// second parameter is the http status should send,
// default is 302 (StatusFound),
// you can set it to 301 (Permant redirect)
// or 303 (StatusSeeOther) if POST method,
// or StatusTemporaryRedirect(307) if that's nessecery.
func (ctx *context) Redirect(urlToRedirect string, statusHeader ...int) {
	ctx.StopExecution()
	// get the previous status code given by the end-developer.
	status := ctx.GetStatusCode()
	if status < 300 { // the previous is not a RCF-valid redirect status.
		status = 0
	}

	if len(statusHeader) > 0 {
		// check if status code is passed via receivers.
		if s := statusHeader[0]; s > 0 {
			status = s
		}
	}
	if status == 0 {
		// if status remains zero then default it.
		// a 'temporary-redirect-like' which works better than for our purpose
		status = http.StatusFound
	}
	// todo 学习原生 server.go 的源码
	http.Redirect(ctx.writer, ctx.request, urlToRedirect, status)
}

//  +------------------------------------------------------------+
//  | Body Readers                                               |
//  +------------------------------------------------------------+

// SetMaxRequestBodySize sets a limit to the request body size
// should be called before reading the request body from the client.
// 限制请求体的大小，在读取来自客户端请求体数据之前调用
// 其本质是设置Request.Body的参数，其中Body是 io.ReadCloser
// todo 原生 io.ReadCloser，以及 Request.Body 源码阅读？？
// 通过原生 request.go 中 maxBytesReader 来限制请求体的大小
func (ctx *context) SetMaxRequestBodySize(limitOverBytes int64) {
	ctx.request.Body = http.MaxBytesReader(ctx.writer, ctx.request.Body, limitOverBytes)
}

// UnmarshalBody reads the request's body and binds it to a value or pointer of any type
// Examples of usage: context.ReadJSON, context.ReadXML.
//
// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-custom-via-unmarshaler/main.go
//
// UnmarshalBody does not check about gzipped data.
// Do not rely on compressed data incoming to your server. The main reason is: https://en.wikipedia.org/wiki/Zip_bomb
// However you are still free to read the `ctx.Request().Body io.Reader` manually.
func (ctx *context) UnmarshalBody(outPtr interface{}, unmarshaler Unmarshaler) error {
	if ctx.request.Body == nil {
		return errors.New("unmarshal: empty body")
	}
	//读取请求体全部的数据
	rawData, err := ioutil.ReadAll(ctx.request.Body)
	if err != nil {
		return err
	}

	// DisableBodyConsumptionOnunmashal 只有在测试用例设置为 true，而且测试用例的例子没看到对app数据的影响
	if ctx.Application().ConfigurationReadOnly().GetDisableBodyConsumptionOnUnmarshal() {
		// * remember, Request.Body has no Bytes(), we have to consume them first
		// and after re-set them to the body, this is the only solution.
		// ioutil.NopCloser()封装Reader实现类的目的是将Close()方法直接返回nil，即没法关闭
		// 问题:这if里的逻辑并不是特别理解？？？？
		// 解答:是否打开配置让请求体的数据原始的接受过来，且封装一个缓存以及不能关闭
		// todo bytes.NewBuffer()源码阅读
		ctx.request.Body = ioutil.NopCloser(bytes.NewBuffer(rawData))
	}

	// check if the v contains its own decode
	// in this case the v should be a pointer also,
	// but this is up to the user's custom Decode implementation*
	//
	// See 'BodyDecoder' for more.
	// 这里则说明了outPtr如果实现了 BodyDecoder ，可以直接拿来解析原始数据
	if decoder, isDecoder := outPtr.(BodyDecoder); isDecoder {
		return decoder.Decode(rawData)
	}

	// // check if v is already a pointer, if yes then pass as it's
	// if reflect.TypeOf(v).Kind() == reflect.Ptr {
	// 	return unmarshaler.Unmarshal(rawData, v)
	// } <- no need for that, ReadJSON is documented enough to receive a pointer,
	// we don't need to reduce the performance here by using the reflect.TypeOf method.

	// f the v doesn't contains a self-body decoder use the custom unmarshaler to bind the body.
	return unmarshaler.Unmarshal(rawData, outPtr)
}

func (ctx *context) shouldOptimize() bool {
	return ctx.Application().ConfigurationReadOnly().GetEnableOptimizations()
}

// ReadJSON reads JSON from request's body and binds it to a value of any json-valid type.
//
// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-json/main.go
func (ctx *context) ReadJSON(jsonObject interface{}) error {
	// 这里调用原生的 json.Unmarshal
	var unmarshaler = json.Unmarshal
	// 如果ctx.shouldOptimize开启优化，则使用jsoniter
	if ctx.shouldOptimize() {
		unmarshaler = jsoniter.Unmarshal
	}
	return ctx.UnmarshalBody(jsonObject, UnmarshalerFunc(unmarshaler))
}

// ReadXML reads XML from request's body and binds it to a value of any xml-valid type.
//
// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-xml/main.go
func (ctx *context) ReadXML(xmlObject interface{}) error {
	// 这里直接使用了原生的 xml.Unmarshal
	return ctx.UnmarshalBody(xmlObject, UnmarshalerFunc(xml.Unmarshal))
}

// IsErrPath can be used at `context#ReadForm`.
// It reports whether the incoming error is type of `formbinder.ErrPath`,
// which can be ignored when server allows unknown post values to be sent by the client.
//
// A shortcut for the `formbinder#IsErrPath`.
var IsErrPath = formbinder.IsErrPath

// ReadForm binds the formObject  with the form data
// it supports any kind of type, including custom structs.
// It will return nothing if request data are empty.
//
// Example: https://github.com/kataras/iris/blob/master/_examples/http_request/read-form/main.go
// todo 本质是通过formbinder.Decode()来实现，阅读formbinder.Decode()
func (ctx *context) ReadForm(formObject interface{}) error {
	// values 的结构是 map[string][]string
	values := ctx.FormValues()
	// 这里是要判断是否ctx.FormValues里面是否为nil
	if len(values) == 0 {
		return nil
	}

	// or dec := formbinder.NewDecoder(&formbinder.DecoderOptions{TagName: "form"})
	// somewhere at the app level. I did change the tagName to "form"
	// inside its source code, so it's not needed for now.
	// todo 本质的form格式转化为对象实际的调用方式，需要看源码？？？？？
	return formbinder.Decode(values, formObject)
}

//  +------------------------------------------------------------+
//  | Body (raw) Writers                                         |
//  +------------------------------------------------------------+

// Write writes the data to the connection as part of an HTTP reply.
//
// If WriteHeader has not yet been called, Write calls
// WriteHeader(http.StatusOK) before writing the data. If the Header
// does not contain a Content-Type line, Write adds a Content-Type set
// to the result of passing the initial 512 bytes of written data to
// DetectContentType.
// 如果在这之前，WriteHeader没有被调用，则会调用WriteHeader(http.StatusOK)，
// 如果Header没有 Content-Type ，则会设置去通过返回的数据最初的512字节数来判断
// todo 512字节数判断的规则？？？？
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
// 不同的客户端HTTP协议，Write()执行后会有不同的效果
// HTTP/1.x：服务端Write调用，其请求体则会过期
// HTTP/2  ：服务端Write可以和读取请求体并发执行，不过有些行为不会支持
// 实现是用原生的responseWriter.Write()来实现
func (ctx *context) Write(rawBody []byte) (int, error) {
	return ctx.writer.Write(rawBody)
}

// Writef formats according to a format specifier and writes to the response.
//
// Returns the number of bytes written and any write error encountered.
func (ctx *context) Writef(format string, a ...interface{}) (n int, err error) {
	// 这里的Writef不是原生的，本质是通过fmt.Fprintf(w, format, a...)
	return ctx.writer.Writef(format, a...)
}

// WriteString writes a simple string to the response.
//
// Returns the number of bytes written and any write error encountered.
func (ctx *context) WriteString(body string) (n int, err error) {
	// 这里的WriteString不是原生的，本质是io.WriteString()的形式实现的
	return ctx.writer.WriteString(body)
}

const (
	// ContentTypeHeaderKey is the header key of "Content-Type".
	ContentTypeHeaderKey = "Content-Type"

	// LastModifiedHeaderKey is the header key of "Last-Modified".
	LastModifiedHeaderKey = "Last-Modified"
	// IfModifiedSinceHeaderKey is the header key of "If-Modified-Since".
	IfModifiedSinceHeaderKey = "If-Modified-Since"
	// CacheControlHeaderKey is the header key of "Cache-Control".
	CacheControlHeaderKey = "Cache-Control"
	// ETagHeaderKey is the header key of "ETag".
	// 问题：ETag是什么？？
	// 解答：ETag是HTTP响应头资源是特定版本的标识符，这可以让缓存更高效，并节省带宽，因为如果内容没有改变，
	// Web服务器不需要发送完整的响应。而如果内容发生了变化，使用ETag有助于防止资源的同时更新相互覆盖（“空中碰撞”）
	ETagHeaderKey = "ETag"

	// ContentDispositionHeaderKey is the header key of "Content-Disposition".
	ContentDispositionHeaderKey = "Content-Disposition"
	// ContentLengthHeaderKey is the header key of "Content-Length"
	ContentLengthHeaderKey = "Content-Length"
	// ContentEncodingHeaderKey is the header key of "Content-Encoding".
	ContentEncodingHeaderKey = "Content-Encoding"
	// GzipHeaderValue is the header value of "gzip".
	GzipHeaderValue = "gzip"
	// AcceptEncodingHeaderKey is the header key of "Accept-Encoding".
	AcceptEncodingHeaderKey = "Accept-Encoding"
	// VaryHeaderKey is the header key of "Vary".
	// 问题：Vary 这个请求头是什么用的？？
	// 解答：表示下一个请求是用缓存回复还是向源服务器请求（https://developer.mozilla.org/zh-CN/docs/Web/HTTP/Headers/Vary）
	VaryHeaderKey = "Vary"
)

var unixEpochTime = time.Unix(0, 0)

// IsZeroTime reports whether t is obviously unspecified (either zero or Unix()=0).
func IsZeroTime(t time.Time) bool {
	return t.IsZero() || t.Equal(unixEpochTime)
}

// ParseTime parses a time header (such as the Date: header),
// trying each forth formats (or three if Application's configuration's TimeFormat is defaulted)
// that are allowed by HTTP/1.1:
// Application's configuration's TimeFormat or/and http.TimeFormat,
// time.RFC850, and time.ANSIC.
//
// Look `context#FormatTime` for the opossite operation (Time to string).
var ParseTime = func(ctx Context, text string) (t time.Time, err error) {
	t, err = time.Parse(ctx.Application().ConfigurationReadOnly().GetTimeFormat(), text)
	if err != nil {
		return http.ParseTime(text)
	}

	return
}

// FormatTime returns a textual representation of the time value formatted
// according to the Application's configuration's TimeFormat field
// which defines the format.
//
// Look `context#ParseTime` for the opossite operation (string to Time).
var FormatTime = func(ctx Context, t time.Time) string {
	return t.Format(ctx.Application().ConfigurationReadOnly().GetTimeFormat())
}

// SetLastModified sets the "Last-Modified" based on the "modtime" input.
// If "modtime" is zero then it does nothing.
//
// It's mostly internally on core/router and context packages.
func (ctx *context) SetLastModified(modtime time.Time) {
	if !IsZeroTime(modtime) {
		// 通过context中设置的时间格式，来通过UTC来进行填充
		ctx.Header(LastModifiedHeaderKey, FormatTime(ctx, modtime.UTC())) // or modtime.UTC()?
	}
}

// CheckIfModifiedSince checks if the response is modified since the "modtime".
// Note that it has nothing to do with server-side caching.
// It does those checks by checking if the "If-Modified-Since" request header
// sent by client or a previous server response header
// (e.g with WriteWithExpiration or StaticEmbedded or Favicon etc.)
// is a valid one and it's before the "modtime".
//
// A check for !modtime && err == nil is necessary to make sure that
// it's not modified since, because it may return false but without even
// had the chance to check the client-side (request) header due to some errors,
// like the HTTP Method is not "GET" or "HEAD" or if the "modtime" is zero
// or if parsing time from the header failed.
//
// It's mostly used internally, e.g. `context#WriteWithExpiration`.
// 判断客户端请求的时间与服务端的时间在UTC格式下，客户端的时间是否是在于服务端的时间之后
// 似乎有两种使用情况，一种是普通请求，一种是文件时间，预计是来处理客户端缓存用的
func (ctx *context) CheckIfModifiedSince(modtime time.Time) (bool, error) {
	// 说明方法必须是GET或者Head
	if method := ctx.Method(); method != http.MethodGet && method != http.MethodHead {
		return false, errors.New("skip: method")
	}
	// 获取请求头中 If-Modified-Since 的值
	ims := ctx.GetHeader(IfModifiedSinceHeaderKey)
	if ims == "" || IsZeroTime(modtime) {
		return false, errors.New("skip: zero time")
	}
	t, err := ParseTime(ctx, ims)
	if err != nil {
		return false, errors.New("skip: " + err.Error())
	}
	// sub-second precision, so
	// use mtime < t+1s instead of mtime <= t to check for unmodified.
	if modtime.UTC().Before(t.Add(1 * time.Second)) {
		return false, nil
	}
	return true, nil
}

// WriteNotModified sends a 304 "Not Modified" status code to the client,
// it makes sure that the content type, the content length headers
// and any "ETag" are removed before the response sent.
//
// It's mostly used internally on core/router/fs.go and context methods.
// 返回304的时候，要注意删除Content-Type和Content-Length以及根据Etag得到的Last-Modified
func (ctx *context) WriteNotModified() {
	// RFC 7232 section 4.1:
	// a sender SHOULD NOT generate representation metadata other than the
	// above listed fields unless said metadata exists for the purpose of
	// guiding cache updates (e.g.," Last-Modified" might be useful if the
	// response does not have an ETag field).
	h := ctx.ResponseWriter().Header()
	delete(h, ContentTypeHeaderKey)
	delete(h, ContentLengthHeaderKey)
	if h.Get(ETagHeaderKey) != "" {
		delete(h, LastModifiedHeaderKey)
	}
	ctx.StatusCode(http.StatusNotModified)
}

// WriteWithExpiration like Write but it sends with an expiration datetime
// which is refreshed every package-level `StaticCacheDuration` field.
// 与Write类似，不过多了时间用来修改响应流头协议的 Last-Modified
func (ctx *context) WriteWithExpiration(body []byte, modtime time.Time) (int, error) {
	if modified, err := ctx.CheckIfModifiedSince(modtime); !modified && err == nil {
		ctx.WriteNotModified()
		return 0, nil
	}

	ctx.SetLastModified(modtime)
	return ctx.writer.Write(body)
}

// StreamWriter registers the given stream writer for populating
// response body.
//
// Access to context's and/or its' members is forbidden from writer.
//
// This function may be used in the following cases:
//
//     * if response body is too big (more than iris.LimitRequestBodySize(if setted)).
//     * if response body is streamed from slow external sources.
//     * if response body must be streamed to the client in chunks.
//     (aka `http server push`).
//
// receives a function which receives the response writer
// and returns false when it should stop writing, otherwise true in order to continue
// 注册一个写入响应体的方法，可以用 and/or 来禁止，当响应体很大（超过了iris设置的请求体大小），
// 或返回的数据是外部数据（比如硬盘），
// 或返回的数据要成块
// 暂时还没有地方被使用
func (ctx *context) StreamWriter(writer func(w io.Writer) bool) {
	w := ctx.writer
	// todo 问题:这个是什么意思不理解
	notifyClosed := w.CloseNotify()
	for {
		select {
		// response writer forced to close, exit.
		case <-notifyClosed:
			return
		default:
			// 对响应流进行回调，并进行w.Flush()
			shouldContinue := writer(w)
			w.Flush()
			if !shouldContinue {
				return
			}
		}
	}
}

//  +------------------------------------------------------------+
//  | Body Writers with compression                              |
//  +------------------------------------------------------------+

// ClientSupportsGzip retruns true if the client supports gzip compression.
// 判断iris是否支持Gzip压缩
func (ctx *context) ClientSupportsGzip() bool {
	// 首先判断请求是否有 Accept-Encoding 参数，且有 gzip ，则可以表示压缩
	if h := ctx.GetHeader(AcceptEncodingHeaderKey); h != "" {
		for _, v := range strings.Split(h, ";") {
			if strings.Contains(v, GzipHeaderValue) { // we do Contains because sometimes browsers has the q=, we don't use it atm. || strings.Contains(v,"deflate"){
				return true
			}
		}
	}
	return false
}

var (
	errClientDoesNotSupportGzip = errors.New("client doesn't supports gzip compression")
)

// WriteGzip accepts bytes, which are compressed to gzip format and sent to the client.
// returns the number of bytes written and an error ( if the client doesn' supports gzip compression)
//
// You may re-use this function in the same handler
// to write more data many times without any troubles.
//// 如果客户端不支持gzip压缩，则会报错，而且这个方法可以在一样的handler中多次使用，
func (ctx *context) WriteGzip(b []byte) (int, error) {
	if !ctx.ClientSupportsGzip() {
		return 0, errClientDoesNotSupportGzip
	}
	//核心的实现方法是GzipResponseWriter().Write()
	return ctx.GzipResponseWriter().Write(b)
}

// TryWriteGzip accepts bytes, which are compressed to gzip format and sent to the client.
// If client does not supprots gzip then the contents are written as they are, uncompressed.
// 这个方式就比之前的方式柔和了很多
func (ctx *context) TryWriteGzip(b []byte) (int, error) {
	n, err := ctx.WriteGzip(b)
	if err != nil {
		// check if the error came from gzip not allowed and not the writer itself
		if _, ok := err.(*errors.Error); ok {
			// client didn't supported gzip, write them uncompressed:
			return ctx.writer.Write(b)
		}
	}
	return n, err
}

// GzipResponseWriter converts the current response writer into a response writer
// which when its .Write called it compress the data to gzip and writes them to the client.
//
// Can be also disabled with its .Disable and .ResetBody to rollback to the usual response writer.
func (ctx *context) GzipResponseWriter() *GzipResponseWriter {
	// if it's already a gzip response writer then just return it
	if gzipResWriter, ok := ctx.writer.(*GzipResponseWriter); ok {
		return gzipResWriter
	}
	// if it's not acquire a new from a pool
	// and register that as the ctx.ResponseWriter.
	// 这里是具体实现转换成GzipResponseWriter的地方
	gzipResWriter := AcquireGzipResponseWriter()
	gzipResWriter.BeginGzipResponse(ctx.writer)
	ctx.ResetResponseWriter(gzipResWriter)
	return gzipResWriter
}

// Gzip enables or disables (if enabled before) the gzip response writer,if the client
// supports gzip compression, so the following response data will
// be sent as compressed gzip data to the client.
// 这里表示是否开启Gzip
func (ctx *context) Gzip(enable bool) {
	if enable {
		if ctx.ClientSupportsGzip() {
			_ = ctx.GzipResponseWriter()
		}
	} else {
		if gzipResWriter, ok := ctx.writer.(*GzipResponseWriter); ok {
			gzipResWriter.Disable()
		}
	}
}

//  +------------------------------------------------------------+
//  | Rich Body Content Writers/Renderers                        |
//  +------------------------------------------------------------+

const (
	// NoLayout to disable layout for a particular template file
	NoLayout = "iris.nolayout"
)

// ViewLayout sets the "layout" option if and when .View
// is being called afterwards, in the same request.
// Useful when need to set or/and change a layout based on the previous handlers in the chain.
//
// Note that the 'layoutTmplFile' argument can be setted to iris.NoLayout || view.NoLayout || context.NoLayout
// to disable the layout for a specific view render action,
// it disables the engine's configuration's layout property.
//
// Look .ViewData and .View too.
//
// Example: https://github.com/kataras/iris/tree/master/_examples/view/context-view-data/
// 表示具体layout模板的文件，内部通过Configuration.go 中的 ViewLayoutContextKey 字段来保存
func (ctx *context) ViewLayout(layoutTmplFile string) {
	ctx.values.Set(ctx.Application().ConfigurationReadOnly().GetViewLayoutContextKey(), layoutTmplFile)
}

// ViewData saves one or more key-value pair in order to be passed if and when .View
// is being called afterwards, in the same request.
// Useful when need to set or/and change template data from previous hanadlers in the chain.
//
// If .View's "binding" argument is not nil and it's not a type of map
// then these data are being ignored, binding has the priority, so the main route's handler can still decide.
// If binding is a map or context.Map then these data are being added to the view data
// and passed to the template.
//
// After .View, the data are not destroyed, in order to be re-used if needed (again, in the same request as everything else),
// to clear the view data, developers can call:
// ctx.Set(ctx.Application().ConfigurationReadOnly().GetViewDataContextKey(), nil)
//
// If 'key' is empty then the value is added as it's (struct or map) and developer is unable to add other value.
//
// Look .ViewLayout and .View too.
//
// Example: https://github.com/kataras/iris/tree/master/_examples/view/context-view-data/
//// 首先描述viewDataContextKey（可以通过ctx.Application().ConfigurationReadOnly().GetViewDataContextKey()，
// 即Configuration中ViewDataContextKey字段获取），
// 如果key为""，则这里的value是存储的容器，存储在context.Values中，key是viewDataContextKey
// 如果key!=""，则通过viewDataContextKey获取context.Values对应的容器，如果容器不存在，则新建一个context.Map{}的容器，
// 并保存key和value，如果容器存在，则判断是否是map或者是context.Map，如果有则更新，没有则新增，如果不是则忽略
func (ctx *context) ViewData(key string, value interface{}) {
	viewDataContextKey := ctx.Application().ConfigurationReadOnly().GetViewDataContextKey()
	if key == "" {
		ctx.values.Set(viewDataContextKey, value)
		return
	}

	v := ctx.values.Get(viewDataContextKey)
	if v == nil {
		ctx.values.Set(viewDataContextKey, Map{key: value})
		return
	}

	if data, ok := v.(map[string]interface{}); ok {
		data[key] = value
	} else if data, ok := v.(Map); ok {
		data[key] = value
	}
}

// GetViewData returns the values registered by `context#ViewData`.
// The return value is `map[string]interface{}`, this means that
// if a custom struct registered to ViewData then this function
// will try to parse it to map, if failed then the return value is nil
// A check for nil is always a good practise if different
// kind of values or no data are registered via `ViewData`.
//
// Similarly to `viewData := ctx.Values().Get("iris.viewData")` or
// `viewData := ctx.Values().Get(ctx.Application().ConfigurationReadOnly().GetViewDataContextKey())`.
//	这个说明了如果存储的容器（容器的意思看ViewData()）是自定义结构，则会自发的将其转为map形式，如果失败则返回nil，所以使用的时候要注意是否为nil
func (ctx *context) GetViewData() map[string]interface{} {
	viewDataContextKey := ctx.Application().ConfigurationReadOnly().GetViewDataContextKey()
	v := ctx.Values().Get(viewDataContextKey)

	// if no values found, then return nil
	if v == nil {
		return nil
	}

	// if struct, convert it to map[string]interface{}
	if structs.IsStruct(v) {
		return structs.Map(v)
	}

	// if pure map[string]interface{}
	if viewData, ok := v.(map[string]interface{}); ok {
		return viewData
	}

	// if context#Map
	if viewData, ok := v.(Map); ok {
		return viewData
	}

	// if failure, then return nil
	return nil
}

// View renders a template based on the registered view engine(s).
// First argument accepts the filename, relative to the view engine's Directory and Extension,
// i.e: if directory is "./templates" and want to render the "./templates/users/index.html"
// then you pass the "users/index.html" as the filename argument.
//
// The second optional argument can receive a single "view model"
// that will be binded to the view template if it's not nil,
// otherwise it will check for previous view data stored by the `ViewData`
// even if stored at any previous handler(middleware) for the same request.
//
// Look .ViewData and .ViewLayout too.
//
// Examples: https://github.com/kataras/iris/tree/master/_examples/view
func (ctx *context) View(filename string, optionalViewModel ...interface{}) error {
	// 设置 Content-Type 为 text/html
	ctx.ContentType(ContentHTMLHeaderValue)
	cfg := ctx.Application().ConfigurationReadOnly()

	layout := ctx.values.GetString(cfg.GetViewLayoutContextKey())

	var bindingData interface{}
	if len(optionalViewModel) > 0 {
		// a nil can override the existing data or model sent by `ViewData`.
		bindingData = optionalViewModel[0]
	} else {
		bindingData = ctx.values.Get(cfg.GetViewDataContextKey())
	}

	// 核心的功能在于View()，在 iris.go 中实现
	// todo iris.go 中 View() 的实现？？好像是viewEngine啥的，想了解就去了解？？
	err := ctx.Application().View(ctx.writer, filename, layout, bindingData)
	if err != nil {
		ctx.StatusCode(http.StatusInternalServerError)
		ctx.StopExecution()
	}

	return err
}

const (
	// ContentBinaryHeaderValue header value for binary data.
	ContentBinaryHeaderValue = "application/octet-stream"
	// ContentHTMLHeaderValue is the  string of text/html response header's content type value.
	ContentHTMLHeaderValue = "text/html"
	// ContentJSONHeaderValue header value for JSON data.
	ContentJSONHeaderValue = "application/json"
	// ContentJavascriptHeaderValue header value for JSONP & Javascript data.
	ContentJavascriptHeaderValue = "application/javascript"
	// ContentTextHeaderValue header value for Text data.
	ContentTextHeaderValue = "text/plain"
	// ContentXMLHeaderValue header value for XML data.
	ContentXMLHeaderValue = "text/xml"
	// ContentMarkdownHeaderValue custom key/content type, the real is the text/html.
	ContentMarkdownHeaderValue = "text/markdown"
	// ContentYAMLHeaderValue header value for YAML data.
	ContentYAMLHeaderValue = "application/x-yaml"
)

// Binary writes out the raw bytes as binary data.
func (ctx *context) Binary(data []byte) (int, error) {
	// 设置 Content-Type 为 application/octet-stream
	ctx.ContentType(ContentBinaryHeaderValue)
	return ctx.Write(data)
}

// Text writes out a string as plain text.
func (ctx *context) Text(text string) (int, error) {
	// 设置 Content-Type 为 text/plain
	ctx.ContentType(ContentTextHeaderValue)
	return ctx.writer.WriteString(text)
}

// HTML writes out a string as text/html.
func (ctx *context) HTML(htmlContents string) (int, error) {
	// 设置 Content-Type 为 text/html
	ctx.ContentType(ContentHTMLHeaderValue)
	return ctx.writer.WriteString(htmlContents)
}

// JSON contains the options for the JSON (Context's) Renderer.
type JSON struct {
	// http-specific
	// 问题：http明确？？？ 看其有啥作用
	// 答案：是否进行编码
	StreamingJSON bool
	// content-specific
	UnescapeHTML bool
	// Indent估计是跟格式化JSON形式有关系
	Indent string
	Prefix string
}

// JSONP contains the options for the JSONP (Context's) Renderer.
type JSONP struct {
	// content-specific
	Indent   string
	Callback string
}

// XML contains the options for the XML (Context's) Renderer.
type XML struct {
	// content-specific
	Indent string
	// 多了一个前缀
	Prefix string
}

// Markdown contains the options for the Markdown (Context's) Renderer.
type Markdown struct {
	// content-specific
	Sanitize bool
}

var (
	newLineB = []byte("\n")
	// the html codes for unescaping
	ltHex = []byte("\\u003c")
	lt    = []byte("<")

	gtHex = []byte("\\u003e")
	gt    = []byte(">")

	andHex = []byte("\\u0026")
	and    = []byte("&")
)

// WriteJSON marshals the given interface object and writes the JSON response to the 'writer'.
// Ignores StatusCode, Gzip, StreamingJSON options.
// Unescape 表示将url部分转码的内容解码
func WriteJSON(writer io.Writer, v interface{}, options JSON, enableOptimization ...bool) (int, error) {
	var (
		result   []byte
		err      error
		optimize = len(enableOptimization) > 0 && enableOptimization[0]
	)

	if indent := options.Indent; indent != "" {
		marshalIndent := json.MarshalIndent
		if optimize {
			marshalIndent = jsoniter.ConfigCompatibleWithStandardLibrary.MarshalIndent
		}

		result, err = marshalIndent(v, "", indent)
		result = append(result, newLineB...)
	} else {
		marshal := json.Marshal
		if optimize {
			marshal = jsoniter.ConfigCompatibleWithStandardLibrary.Marshal
		}
		// 这个就默认的形式
		result, err = marshal(v)
	}

	if err != nil {
		return 0, err
	}
	// Unescape则是取消转码的意思，比如 \\u003c -> <
	if options.UnescapeHTML {
		result = bytes.Replace(result, ltHex, lt, -1)
		result = bytes.Replace(result, gtHex, gt, -1)
		result = bytes.Replace(result, andHex, and, -1)
	}
	// 在返回的结果中加入前置
	if prefix := options.Prefix; prefix != "" {
		result = append([]byte(prefix), result...)
	}

	return writer.Write(result)
}

// DefaultJSONOptions is the optional settings that are being used
// inside `ctx.JSON`.
var DefaultJSONOptions = JSON{}

// JSON marshals the given interface object and writes the JSON response to the client.
func (ctx *context) JSON(v interface{}, opts ...JSON) (n int, err error) {
	options := DefaultJSONOptions

	if len(opts) > 0 {
		options = opts[0]
	}
	// 设置Content-Type为 application/json
	ctx.ContentType(ContentJSONHeaderValue)
	// 如果这里为true，则通过json进行编码
	if options.StreamingJSON {
		if ctx.shouldOptimize() {
			var jsoniterConfig = jsoniter.Config{
				EscapeHTML:    !options.UnescapeHTML,
				IndentionStep: 4,
			}.Froze()
			enc := jsoniterConfig.NewEncoder(ctx.writer)
			err = enc.Encode(v)
		} else {
			enc := json.NewEncoder(ctx.writer)
			enc.SetEscapeHTML(!options.UnescapeHTML)
			enc.SetIndent(options.Prefix, options.Indent)
			err = enc.Encode(v)
		}

		if err != nil {
			ctx.StatusCode(http.StatusInternalServerError) // it handles the fallback to normal mode here which also removes the gzip headers.
			return 0, err
		}
		return ctx.writer.Written(), err
	}
	// WriteJSON的差别在于忽略了StatusCode, Gzip, StreamingJSON选项
	n, err = WriteJSON(ctx.writer, v, options, ctx.shouldOptimize())
	if err != nil {
		ctx.StatusCode(http.StatusInternalServerError)
		return 0, err
	}

	return n, err
}

var (
	finishCallbackB = []byte(");")
)

// WriteJSONP marshals the given interface object and writes the JSON response to the writer.
// 与WriteJSON的差别在于多了callback();这样的结构
func WriteJSONP(writer io.Writer, v interface{}, options JSONP, enableOptimization ...bool) (int, error) {
	if callback := options.Callback; callback != "" {
		// 这里一开始的说个事就是callback + (，这里的callback预计是前端给的
		writer.Write([]byte(callback + "("))
		// 最后再加上 );
		defer writer.Write(finishCallbackB)
	}

	optimize := len(enableOptimization) > 0 && enableOptimization[0]
	// 这里的indent与JSON类似，也是跟格式有关
	if indent := options.Indent; indent != "" {
		marshalIndent := json.MarshalIndent
		if optimize {
			marshalIndent = jsoniter.ConfigCompatibleWithStandardLibrary.MarshalIndent
		}

		result, err := marshalIndent(v, "", indent)
		if err != nil {
			return 0, err
		}
		result = append(result, newLineB...)
		return writer.Write(result)
	}

	marshal := json.Marshal
	if optimize {
		marshal = jsoniter.ConfigCompatibleWithStandardLibrary.Marshal
	}

	result, err := marshal(v)
	if err != nil {
		return 0, err
	}
	return writer.Write(result)
}

// DefaultJSONPOptions is the optional settings that are being used
// inside `ctx.JSONP`.
var DefaultJSONPOptions = JSONP{}

// JSONP marshals the given interface object and writes the JSON response to the client.
func (ctx *context) JSONP(v interface{}, opts ...JSONP) (int, error) {
	options := DefaultJSONPOptions

	if len(opts) > 0 {
		options = opts[0]
	}
	// 设置 Content-Type 为 application/javascript
	ctx.ContentType(ContentJavascriptHeaderValue)

	n, err := WriteJSONP(ctx.writer, v, options, ctx.shouldOptimize())
	if err != nil {
		ctx.StatusCode(http.StatusInternalServerError)
		return 0, err
	}

	return n, err
}

// WriteXML marshals the given interface object and writes the XML response to the writer.
func WriteXML(writer io.Writer, v interface{}, options XML) (int, error) {
	if prefix := options.Prefix; prefix != "" {
		// 如果有前缀，则优先写前缀
		writer.Write([]byte(prefix))
	}

	if indent := options.Indent; indent != "" {
		// 这里就是xml如果有indent的格式
		result, err := xml.MarshalIndent(v, "", indent)
		if err != nil {
			return 0, err
		}
		result = append(result, newLineB...)
		return writer.Write(result)
	}

	result, err := xml.Marshal(v)
	if err != nil {
		return 0, err
	}
	return writer.Write(result)
}

// DefaultXMLOptions is the optional settings that are being used
// from `ctx.XML`.
var DefaultXMLOptions = XML{}

// XML marshals the given interface object and writes the XML response to the client.
func (ctx *context) XML(v interface{}, opts ...XML) (int, error) {
	options := DefaultXMLOptions

	if len(opts) > 0 {
		options = opts[0]
	}
	// 设置Content-Type 为 text/xml
	ctx.ContentType(ContentXMLHeaderValue)

	n, err := WriteXML(ctx.writer, v, options)
	if err != nil {
		ctx.StatusCode(http.StatusInternalServerError)
		return 0, err
	}

	return n, err
}

// WriteMarkdown parses the markdown to html and writes these contents to the writer.
func WriteMarkdown(writer io.Writer, markdownB []byte, options Markdown) (int, error) {
	buf := blackfriday.Run(markdownB)
	if options.Sanitize {
		buf = bluemonday.UGCPolicy().SanitizeBytes(buf)
	}
	return writer.Write(buf)
}

// DefaultMarkdownOptions is the optional settings that are being used
// from `WriteMarkdown` and `ctx.Markdown`.
var DefaultMarkdownOptions = Markdown{}

// Markdown parses the markdown to html and renders its result to the client.
func (ctx *context) Markdown(markdownB []byte, opts ...Markdown) (int, error) {
	options := DefaultMarkdownOptions

	if len(opts) > 0 {
		options = opts[0]
	}
	// 设置 Content-Type 为 text/html
	ctx.ContentType(ContentHTMLHeaderValue)
	// todo 这里里面的实现想了解可以看下？？
	n, err := WriteMarkdown(ctx.writer, markdownB, options)
	if err != nil {
		ctx.StatusCode(http.StatusInternalServerError)
		return 0, err
	}

	return n, err
}

// YAML marshals the "v" using the yaml marshaler and renders its result to the client.
func (ctx *context) YAML(v interface{}) (int, error) {
	// todo 用gopkg.in 包中的，想了解可以看下？？
	out, err := yaml.Marshal(v)
	if err != nil {
		ctx.StatusCode(http.StatusInternalServerError)
		return 0, err
	}
	// 设置 Content-Type 为 application/x-yaml
	ctx.ContentType(ContentYAMLHeaderValue)
	return ctx.Write(out)
}

//  +------------------------------------------------------------+
//  | Serve files                                                |
//  +------------------------------------------------------------+

var (
	errServeContent = errors.New("while trying to serve content to the client. Trace %s")
)

// ServeContent serves content, headers are autoset
// receives three parameters, it's low-level function, instead you can use .ServeFile(string,bool)/SendFile(string,string)
//
// You can define your own "Content-Type" header also, after this function call
// Doesn't implements resuming (by range), use ctx.SendFile instead
// 自动设置content和headers，是比较低级的方法，可以被.ServeFile()/SendFile()取代
// 可以在这个方法前自己定义Conetnt-Type
// 这个方法不支持重新设置，可以使用ctx.SendFile 或者是 router's StaticWeb替代
// todo io.ReadSeeker 源码阅读？？
// ServeContent 是通过 io的角度处理
func (ctx *context) ServeContent(content io.ReadSeeker, filename string, modtime time.Time, gzipCompression bool) error {
	// 这里判断服务端这边是否有过更新
	if modified, err := ctx.CheckIfModifiedSince(modtime); !modified && err == nil {
		ctx.WriteNotModified()
		return nil
	}

	ctx.ContentType(filename)
	ctx.SetLastModified(modtime)
	var out io.Writer
	if gzipCompression && ctx.ClientSupportsGzip() {
		AddGzipHeaders(ctx.writer)
		// 内部有一个gzipPool池
		gzipWriter := acquireGzipWriter(ctx.writer)
		defer releaseGzipWriter(gzipWriter)
		out = gzipWriter
	} else {
		out = ctx.writer
	}
	_, err := io.Copy(out, content)
	// 就是 errServeContent 整合了 err 的错误信息
	return errServeContent.With(err) ///TODO: add an int64 as return value for the content length written like other writers or let it as it's in order to keep the stable api?
}

// ServeFile serves a view file, to send a file ( zip for example) to the client you should use the SendFile(serverfilename,clientfilename)
// receives two parameters
// filename/path (string)
// gzipCompression (bool)
//
// You can define your own "Content-Type" header also, after this function call
// This function doesn't implement resuming (by range), use ctx.SendFile instead
//
// Use it when you want to serve css/js/... files to the client, for bigger files and 'force-download' use the SendFile.
// 内部实现是通过ServeContent()来实现，这里封装了从File角度处理
func (ctx *context) ServeFile(filename string, gzipCompression bool) error {
	f, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("%d", 404)
	}
	defer f.Close()
	// f.Stat()即返回文件的属性
	fi, _ := f.Stat()
	// 如果是目录，则进入这个目录并调用index.html这个文件
	if fi.IsDir() {
		return ctx.ServeFile(path.Join(filename, "index.html"), gzipCompression)
	}

	return ctx.ServeContent(f, fi.Name(), fi.ModTime(), gzipCompression)
}

// SendFile sends file for force-download to the client
//
// Use this instead of ServeFile to 'force-download' bigger files to the client.
// 这里是为了让客户端强制下载（毕竟大文件直接浏览耗时间）
// 设置加了一个请求头通过"Content-Disposition = attachment;filename= destinationName" 来处理，然后调用ServeFile
func (ctx *context) SendFile(filename string, destinationName string) error {
	// 问题：Set和Add()什么区别？？？
	// 解答：因为头字段指定的key后面是一个数组，所以Add就是添加后面，set就是直接更新整个
	ctx.writer.Header().Set(ContentDispositionHeaderKey, "attachment;filename="+destinationName)
	return ctx.ServeFile(filename, false)
}

//  +------------------------------------------------------------+
//  | Cookies                                                    |
//  +------------------------------------------------------------+

// CookieOption is the type of function that is accepted on
// context's methods like `SetCookieKV`, `RemoveCookie` and `SetCookie`
// as their (last) variadic input argument to amend the end cookie's form.
//
// Any custom or built'n `CookieOption` is valid,
// see `CookiePath`, `CookieCleanPath`, `CookieExpires` and `CookieHTTPOnly` for more.
type CookieOption func(*http.Cookie)

// CookiePath is a `CookieOption`.
// Use it to change the cookie's Path field.
func CookiePath(path string) CookieOption {
	return func(c *http.Cookie) {
		c.Path = path
	}
}

// CookieCleanPath is a `CookieOption`.
// Use it to clear the cookie's Path field, exactly the same as `CookiePath("")`.
func CookieCleanPath(c *http.Cookie) {
	c.Path = ""
}

// CookieExpires is a `CookieOption`.
// Use it to change the cookie's Expires and MaxAge fields by passing the lifetime of the cookie.
func CookieExpires(durFromNow time.Duration) CookieOption {
	return func(c *http.Cookie) {
		c.Expires = time.Now().Add(durFromNow)
		c.MaxAge = int(durFromNow.Seconds())
	}
}

// CookieHTTPOnly is a `CookieOption`.
// Use it to set the cookie's HttpOnly field to false or true.
// HttpOnly field defaults to true for `RemoveCookie` and `SetCookieKV`.
func CookieHTTPOnly(httpOnly bool) CookieOption {
	return func(c *http.Cookie) {
		c.HttpOnly = httpOnly
	}
}

type (
	// CookieEncoder should encode the cookie value.
	// Should accept as first argument the cookie name
	// and as second argument the cookie value ptr.
	// Should return an encoded value or an empty one if encode operation failed.
	// Should return an error if encode operation failed.
	//
	// Note: Errors are not printed, so you have to know what you're doing,
	// and remember: if you use AES it only supports key sizes of 16, 24 or 32 bytes.
	// You either need to provide exactly that amount or you derive the key from what you type in.
	//
	// See `CookieDecoder` too.
	CookieEncoder func(cookieName string, value interface{}) (string, error)
	// CookieDecoder should decode the cookie value.
	// Should accept as first argument the cookie name,
	// as second argument the encoded cookie value and as third argument the decoded value ptr.
	// Should return a decoded value or an empty one if decode operation failed.
	// Should return an error if decode operation failed.
	//
	// Note: Errors are not printed, so you have to know what you're doing,
	// and remember: if you use AES it only supports key sizes of 16, 24 or 32 bytes.
	// You either need to provide exactly that amount or you derive the key from what you type in.
	//
	// See `CookieEncoder` too.
	CookieDecoder func(cookieName string, cookieValue string, v interface{}) error
)

// CookieEncode is a `CookieOption`.
// Provides encoding functionality when adding a cookie.
// Accepts a `CookieEncoder` and sets the cookie's value to the encoded value.
// Users of that is the `SetCookie` and `SetCookieKV`.
//
// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/securecookie
func CookieEncode(encode CookieEncoder) CookieOption {
	return func(c *http.Cookie) {
		newVal, err := encode(c.Name, c.Value)
		if err != nil {
			c.Value = ""
		} else {
			c.Value = newVal
		}
	}
}

// CookieDecode is a `CookieOption`.
// Provides decoding functionality when retrieving a cookie.
// Accepts a `CookieDecoder` and sets the cookie's value to the decoded value before return by the `GetCookie`.
// User of that is the `GetCookie`.
//
// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/securecookie
func CookieDecode(decode CookieDecoder) CookieOption {
	return func(c *http.Cookie) {
		if err := decode(c.Name, c.Value, &c.Value); err != nil {
			c.Value = ""
		}
	}
}

// SetCookie adds a cookie.
// Use of the "options" is not required, they can be used to amend the "cookie".
//
// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
// todo 阅读http.Cookie源码？？
// options是在cookie被返回时进行的处理
func (ctx *context) SetCookie(cookie *http.Cookie, options ...CookieOption) {
	for _, opt := range options {
		opt(cookie)
	}
	// 用原生的SetCookie来保存
	http.SetCookie(ctx.writer, cookie)
}

// SetCookieKV adds a cookie, requires the name(string) and the value(string).
//
// By default it expires at 2 hours and it's added to the root path,
// use the `CookieExpires` and `CookiePath` to modify them.
// Alternatively: ctx.SetCookie(&http.Cookie{...})
//
// If you want to set custom the path:
// ctx.SetCookieKV(name, value, iris.CookiePath("/custom/path/cookie/will/be/stored"))
//
// If you want to be visible only to current request path:
// (note that client should be responsible for that if server sent an empty cookie's path, all browsers are compatible)
// ctx.SetCookieKV(name, value, iris.CookieCleanPath/iris.CookiePath(""))
// More:
//                              iris.CookieExpires(time.Duration)
//                              iris.CookieHTTPOnly(false)
//
// Examples: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
// 在cookie中添加 name 和 value，且默认生存时间是2小时，且添加到根路径，可以通过 CookieExpires 和 CookiePath 修改
// 具体的使用方式可以看上面注释的例子，本质还是SetCookie(&http.Cookie{})
func (ctx *context) SetCookieKV(name, value string, options ...CookieOption) {
	c := &http.Cookie{}
	c.Path = "/"
	c.Name = name
	c.Value = url.QueryEscape(value)
	// 问题：httpOnly是什么意思？？（https://dreamer-yzy.github.io/2014/12/22/Cookie-%E7%9A%84-HttpOnly-%E5%92%8C-Secure-%E5%B1%9E%E6%80%A7%E4%BD%9C%E7%94%A8/）
	// 解答：作用是保护了cookie的安全，即浏览器不能在HTTP/HTTPS之外暴露Cookie，这样就避免了用JS来暴露Cookie
	c.HttpOnly = true
	c.Expires = time.Now().Add(SetCookieKVExpiration)
	c.MaxAge = int(SetCookieKVExpiration.Seconds())
	ctx.SetCookie(c, options...)
}

// GetCookie returns cookie's value by it's name
// returns empty string if nothing was found.
//
// If you want more than the value then:
// cookie, err := ctx.Request().Cookie("name")
//
// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
// 根据 name 来进行处理
func (ctx *context) GetCookie(name string, options ...CookieOption) string {
	cookie, err := ctx.request.Cookie(name)
	if err != nil {
		return ""
	}

	for _, opt := range options {
		opt(cookie)
	}

	value, _ := url.QueryUnescape(cookie.Value)
	return value
}

// SetCookieKVExpiration is 2 hours by-default
// you can change it or simple, use the SetCookie for more control.
//
// See `SetCookieKVExpiration` and `CookieExpires` for more.
var SetCookieKVExpiration = time.Duration(120) * time.Minute

// RemoveCookie deletes a cookie by it's name and path = "/".
// Tip: change the cookie's path to the current one by: RemoveCookie("name", iris.CookieCleanPath)
//
// Example: https://github.com/kataras/iris/tree/master/_examples/cookies/basic
func (ctx *context) RemoveCookie(name string, options ...CookieOption) {
	c := &http.Cookie{}
	c.Name = name
	c.Value = ""
	c.Path = "/" // if user wants to change it, use of the CookieOption `CookiePath` is required if not `ctx.SetCookie`.
	c.HttpOnly = true
	// RFC says 1 second, but let's do it 1  to make sure is working
	exp := time.Now().Add(-time.Duration(1) * time.Minute)
	c.Expires = exp
	c.MaxAge = -1
	ctx.SetCookie(c, options...)
	// delete request's cookie also, which is temporary available.
	// todo 阅读原生 Set("Cookie","")的源码
	ctx.request.Header.Set("Cookie", "")
}

// VisitAllCookies takes a visitor which loops
// on each (request's) cookies' name and value.
// 自定义接口来循环处理Cookie的值
func (ctx *context) VisitAllCookies(visitor func(name string, value string)) {
	for _, cookie := range ctx.request.Cookies() {
		visitor(cookie.Name, cookie.Value)
	}
}

var maxAgeExp = regexp.MustCompile(`maxage=(\d+)`)

// MaxAge returns the "cache-control" request header's value
// seconds as int64
// if header not found or parse failed then it returns -1.
// 如果请求头有 Cache-Control ，才返回int64 结构的生存时间，如果没有则返回-1
func (ctx *context) MaxAge() int64 {
	header := ctx.GetHeader(CacheControlHeaderKey)
	if header == "" {
		return -1
	}
	// maxAgeExp的格式是 "maxage=(\d+)"，即 maxage = xx
	m := maxAgeExp.FindStringSubmatch(header)
	if len(m) == 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return int64(v)
		}
	}
	return -1
}

//  +------------------------------------------------------------+
//  | Advanced: Response Recorder and Transactions               |
//  +------------------------------------------------------------+

// Record transforms the context's basic and direct responseWriter to a *ResponseRecorder
// which can be used to reset the body, reset headers, get the body,
// get & set the status code at any time and more.
// Record 将ResponseWriter 转变成 ResponseRecorder
// todo ResponseRecorder 是什么作用？？
func (ctx *context) Record() {
	if w, ok := ctx.writer.(*responseWriter); ok {
		// todo 阅读AcquireResponseRecorder()，BeginRecord的实现方法
		recorder := AcquireResponseRecorder()
		recorder.BeginRecord(w)
		ctx.ResetResponseWriter(recorder)
	}
}

// Recorder returns the context's ResponseRecorder
// if not recording then it starts recording and returns the new context's ResponseRecorder
// 返回当前context的ResponseRecorder
func (ctx *context) Recorder() *ResponseRecorder {
	ctx.Record()
	return ctx.writer.(*ResponseRecorder)
}

// IsRecording returns the response recorder and a true value
// when the response writer is recording the status code, body, headers and so on,
// else returns nil and false.
// 就是断言类型 ResponseRecorder
func (ctx *context) IsRecording() (*ResponseRecorder, bool) {
	//NOTE:
	// two return values in order to minimize the if statement:
	// if (Recording) then writer = Recorder()
	// instead we do: recorder,ok = Recording()
	rr, ok := ctx.writer.(*ResponseRecorder)
	return rr, ok
}

// non-detailed error log for transacton unexpected panic
var errTransactionInterrupted = errors.New("transaction interrupted, recovery from panic:\n%s")

// BeginTransaction starts a scoped transaction.
//
// Can't say a lot here because it will take more than 200 lines to write about.
// You can search third-party articles or books on how Business Transaction works (it's quite simple, especially here).
//
// Note that this is unique and new
// (=I haver never seen any other examples or code in Golang on this subject, so far, as with the most of iris features...)
// it's not covers all paths,
// such as databases, this should be managed by the libraries you use to make your database connection,
// this transaction scope is only for context's response.
// Transactions have their own middleware ecosystem also.
//
// See https://github.com/kataras/iris/tree/master/_examples/ for more
func (ctx *context) BeginTransaction(pipe func(t *Transaction)) {
	// do NOT begin a transaction when the previous transaction has been failed
	// and it was requested scoped or SkipTransactions called manually.
	if ctx.TransactionsSkipped() {
		return
	}

	// start recording in order to be able to control the full response writer
	ctx.Record()

	t := newTransaction(ctx) // it calls this *context, so the overriding with a new pool's New of context.Context wil not work here.
	defer func() {
		if err := recover(); err != nil {
			ctx.Application().Logger().Warn(errTransactionInterrupted.Format(err).Error())
			// complete (again or not , doesn't matters) the scope without loud
			t.Complete(nil)
			// we continue as normal, no need to return here*
		}

		// write the temp contents to the original writer
		t.Context().ResponseWriter().WriteTo(ctx.writer)
		// give back to the transaction the original writer (SetBeforeFlush works this way and only this way)
		// this is tricky but nessecery if we want ctx.FireStatusCode to work inside transactions
		t.Context().ResetResponseWriter(ctx.writer)

	}()

	// run the worker with its context clone inside.
	pipe(t)
}

// skipTransactionsContextKey set this to any value to stop executing next transactions
// it's a context-key in order to be used from anywhere, set it by calling the SkipTransactions()
const skipTransactionsContextKey = "@transictions_skipped"

// SkipTransactions if called then skip the rest of the transactions
// or all of them if called before the first transaction
func (ctx *context) SkipTransactions() {
	ctx.values.Set(skipTransactionsContextKey, 1)
}

// TransactionsSkipped returns true if the transactions skipped or canceled at all.
func (ctx *context) TransactionsSkipped() bool {
	if n, err := ctx.values.GetInt(skipTransactionsContextKey); err == nil && n == 1 {
		return true
	}
	return false
}

// Exec calls the framewrok's ServeHTTPC
// based on this context but with a changed method and path
// like it was requested by the user, but it is not.
//
// Offline means that the route is registered to the iris and have all features that a normal route has
// BUT it isn't available by browsing, its handlers executed only when other handler's context call them
// it can validate paths, has sessions, path parameters and all.
//
// You can find the Route by app.GetRoute("theRouteName")
// you can set a route name as: myRoute := app.Get("/mypath", handler)("theRouteName")
// that will set a name to the route and returns its RouteInfo instance for further usage.
//
// It doesn't changes the global state, if a route was "offline" it remains offline.
//
// app.None(...) and app.GetRoutes().Offline(route)/.Online(route, method)
//
// Example: https://github.com/kataras/iris/tree/master/_examples/routing/route-state
//
// User can get the response by simple using rec := ctx.Recorder(); rec.Body()/rec.StatusCode()/rec.Header().
//
// context's Values and the Session are kept in order to be able to communicate via the result route.
//
// It's for extreme use cases, 99% of the times will never be useful for you.
// 这里的实现的功能是服务端处理一个请求逻辑中，中途使用method 方法调用 path，然后再变回来
func (ctx *context) Exec(method string, path string) {
	if path == "" {
		return
	}

	if method == "" {
		method = "GET"
	}

	// backup the handlers
	backupHandlers := ctx.handlers[0:]
	backupPos := ctx.currentHandlerIndex

	req := ctx.request
	// backup the request path information
	backupPath := req.URL.Path
	backupMethod := req.Method
	// don't backupValues := ctx.Values().ReadOnly()
	// set the request to be align with the 'againstRequestPath'
	req.RequestURI = path
	req.URL.Path = path
	req.Method = method

	// [values stays]
	// reset handlers
	ctx.handlers = ctx.handlers[0:0]
	ctx.currentHandlerIndex = 0

	// execute the route from the (internal) context router
	// this way we keep the sessions and the values
	ctx.Application().ServeHTTPC(ctx)

	// set the request back to its previous state
	req.RequestURI = backupPath
	req.URL.Path = backupPath
	req.Method = backupMethod

	// set back the old handlers and the last known index
	ctx.handlers = backupHandlers
	ctx.currentHandlerIndex = backupPos
}

// RouteExists reports whether a particular route exists
// It will search from the current subdomain of context's host, if not inside the root domain.
// 判断当前的context.Application中是否有对应的方法和路径的路由
func (ctx *context) RouteExists(method, path string) bool {
	return ctx.Application().RouteExists(ctx, method, path)
}

// Application returns the iris app instance which belongs to this context.
// Worth to notice that this function returns an interface
// of the Application, which contains methods that are safe
// to be executed at serve-time. The full app's fields
// and methods are not available here for the developer's safety.
// 返回当前iris 的 app 实例
func (ctx *context) Application() Application {
	return ctx.app
}

var lastCapturedContextID uint64

// LastCapturedContextID returns the total number of `context#String` calls.
func LastCapturedContextID() uint64 {
	return atomic.LoadUint64(&lastCapturedContextID)
}

// String returns the string representation of this request.
// Each context has a unique string representation.
// It can be used for simple debugging scenarios, i.e print context as string.
//
// What it returns? A number which declares the length of the
// total `String` calls per executable application, followed
// by the remote IP (the client) and finally the method:url.
// 表示当前的 Request 的string
// 每一个Context有一个唯一的标志
func (ctx *context) String() string {
	if ctx.id == 0 {
		// set the id here.
		forward := atomic.AddUint64(&lastCapturedContextID, 1)
		ctx.id = forward
	}

	return fmt.Sprintf("[%d] %s ▶ %s:%s",
		ctx.id, ctx.RemoteAddr(), ctx.Method(), ctx.Request().RequestURI)
}
