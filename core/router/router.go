package router

import (
	"net/http"
	"sync"

	"github.com/kataras/iris/context"
	"github.com/kataras/iris/core/errors"
)

// Router is the "director".
// Caller should provide a request handler (router implementation or root handler).
// Router is responsible to build the received request handler and run it
// to serve requests, based on the received context.Pool.
//
// User can refresh the router with `RefreshRouter` whenever a route's field is changed by him.
type Router struct {
	mu sync.Mutex // for Downgrade(降级), WrapRouter & BuildRouter,

	// not indeed but we don't to risk its usage by third-parties.
	// 这个可以使用自己设置的路由(_examples/routing/custom-high-level-router/里面的例子可以知道)
	// todo 通过handler.go中258行的处理，可以理解是走这里，所以看下Context.go
	requestHandler RequestHandler // build-accessible, can be changed to define a custom router or proxy, used on RefreshRouter too.

	// 这里什么意思有些不解?
	// 在BuildRouter中看102行，可以看到mainHandler默认走RequestHandler那边过的
	// 使用mainHandler的时候一般都是直接与原生的http.Server一起使用的时候
	mainHandler http.HandlerFunc // init-accessible

	// 这个参数就是在进行mainHandler的时候多在前面封装了一层
	wrapperFunc func(http.ResponseWriter, *http.Request, http.HandlerFunc)

	// 这个第一次数据不知道是哪里来的,似乎一直都是nil
	cPool *context.Pool // used on RefreshRouter

	//提供所有路由的地方
	routesProvider RoutesProvider
}

// NewRouter returns a new empty Router.
func NewRouter() *Router { return &Router{} }

// RefreshRouter re-builds the router. Should be called when a route's state
// changed (i.e Method changed at serve-time).
// 重新建立路由，特别是在代码运行阶段方法变更的时候(RefreshRouter()在_examples/routing/route-state/里面的代码中使用)
func (router *Router) RefreshRouter() error {
	return router.BuildRouter(router.cPool, router.requestHandler, router.routesProvider, true)
}

// BuildRouter builds the router based on
// the context factory (explicit pool in this case),
// the request handler which manages how the main handler will multiplexes the routes
// provided by the third parameter, routerProvider (it's the api builder in this case) and
// its wrapper.
//
// Use of RefreshRouter to re-build the router if needed.
//
// 这里除了RefreshRouter使用外，还有哪里进行使用?
// 这里要看GOPATH的代码会寻找到，是在iris.go中Build()中app.Router.BuildRouter(app.ContextPool, routerHandler, app.APIBuilder, false)
// 这里的前提条件是Downgraded()是false，即没有降级
// 如果走这里了，mainHandler也是走RequestHandler一样
func (router *Router) BuildRouter(cPool *context.Pool, requestHandler RequestHandler, routesProvider RoutesProvider, force bool) error {

	if requestHandler == nil {
		return errors.New("router: request handler is nil")
	}

	if cPool == nil {
		return errors.New("router: context pool is nil")
	}

	// build the handler using the routesProvider
	// 是直接通过接口的方法调用来实现的，来隐藏真实的代码的调用
	// 这里使用抽象方法，更好的符合扩展性
	if err := requestHandler.Build(routesProvider); err != nil {
		return err
	}

	router.mu.Lock()
	defer router.mu.Unlock()

	// store these for RefreshRouter's needs.
	// force为true 就是表示强制更新里面的数据
	if force {
		router.cPool = cPool
		router.requestHandler = requestHandler
		router.routesProvider = routesProvider
	} else {
		if router.cPool == nil {
			router.cPool = cPool
		}

		if router.requestHandler == nil {
			router.requestHandler = requestHandler
		}

		if router.routesProvider == nil && routesProvider != nil {
			router.routesProvider = routesProvider
		}
	}

	// the important
	// 这里整体的意思是从池里取出Context，然后用于当前router的使用，然后在放回到池子中
	// router.mainHandler什么时候被使用?
	// 在这个Router方式兼容原生的Http.Server的时候,需要被使用
	router.mainHandler = func(w http.ResponseWriter, r *http.Request) {
		// todo context.Pool 的源码解析
		ctx := cPool.Acquire(w, r)
		router.requestHandler.HandleRequest(ctx)
		cPool.Release(ctx)
	}

	// 这里的wrapperFunc我的理解是在进行主要的mainHandler之前，先进行wrapperFunc的处理，然后(内部会有router.mainHandler的处理)
	if router.wrapperFunc != nil { // if wrapper used then attach that as the router service
		router.mainHandler = NewWrapper(router.wrapperFunc, router.mainHandler).ServeHTTP
	}

	return nil
}

// Downgrade "downgrades", alters the router supervisor(监督) service(Router.mainHandler)
//  algorithm to a custom one,
// be aware to change the global variables of 'ParamStart' and 'ParamWildcardStart'.
// can be used to implement a custom proxy or
// a custom router which should work with raw ResponseWriter, *Request
// instead of the Context(which again, can be retrieved(取回) by the Framework's context pool).
//
// Note: Downgrade will by-pass the Wrapper, the caller is responsible for everything.
// Downgrade is thread-safe.
// 这里有些不理解（第一反应通过自己新的额handlerFunc来进行服务降级)？
// 这里的就是在直接使用原生http.Server的时候,可以调整具体的请求处理的方式
func (router *Router) Downgrade(newMainHandler http.HandlerFunc) {
	router.mu.Lock()
	router.mainHandler = newMainHandler
	router.mu.Unlock()
}

// Downgraded returns true if this router is downgraded.
// 这个判断有些不理解，为啥requestHandler==nil?
// 这里表示只进行原生http.Server表示降级
func (router *Router) Downgraded() bool {
	return router.mainHandler != nil && router.requestHandler == nil
}

// WrapperFunc is used as an expected input parameter signature
// for the WrapRouter. It's a "low-level" signature which is compatible
// with the net/http.
// It's being used to run or no run the router based on a custom logic.
type WrapperFunc func(w http.ResponseWriter, r *http.Request, firstNextIsTheRouter http.HandlerFunc)

// WrapRouter adds a wrapper on the top of the main router.
// Usually it's useful for third-party middleware
// when need to wrap the entire application with a middleware like CORS.
//
// Developers can add more than one wrappers,
// those wrappers' execution comes from last to first.
// That means that the second wrapper will wrap the first, and so on.
//
// Before build.
// 这个方法的目的是在方法的调用前再增加一个封装
func (router *Router) WrapRouter(wrapperFunc WrapperFunc) {
	if wrapperFunc == nil {
		return
	}

	router.mu.Lock()
	defer router.mu.Unlock()

	if router.wrapperFunc != nil {
		//********** 这里开始进行封装*************
		// wrap into one function, from bottom to top, end to begin.
		nextWrapper := wrapperFunc
		prevWrapper := router.wrapperFunc
		wrapperFunc = func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
			if next != nil {
				// 从这里的顺序，可以看出当前实参传进来的是先执行，然后之前的后执行
				nexthttpFunc := http.HandlerFunc(func(_w http.ResponseWriter, _r *http.Request) {
					prevWrapper(_w, _r, next)
				})
				nextWrapper(w, r, nexthttpFunc)
			}
		}
		//********** 这里结束封装*************
	}

	router.wrapperFunc = wrapperFunc
}

// ServeHTTPC serves the raw context, useful if we have already a context, it by-pass the wrapper.
// iris就是通过routeHandler来进行处理
func (router *Router) ServeHTTPC(ctx context.Context) {
	router.requestHandler.HandleRequest(ctx)
}

// 这个就是此时router的mainHandler进行处理，即不走自定义的路由那边过
func (router *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	router.mainHandler(w, r)
}

// RouteExists reports whether a particular route exists
// It will search from the current subdomain of context's host, if not inside the root domain.
// iris通过routerHandler来进行处理
func (router *Router) RouteExists(ctx context.Context, method, path string) bool {
	return router.requestHandler.RouteExists(ctx, method, path)
}

type wrapper struct {
	router      http.HandlerFunc // http.HandlerFunc to catch the CURRENT state of its .ServeHTTP on case of future change.
	wrapperFunc func(http.ResponseWriter, *http.Request, http.HandlerFunc)
}

// NewWrapper returns a new http.Handler wrapped by the 'wrapperFunc'
// the "next" is the final "wrapped" input parameter.
//
// Application is responsible to make it to work on more than one wrappers
// via composition or func clojure.
func NewWrapper(wrapperFunc func(w http.ResponseWriter, r *http.Request, routerNext http.HandlerFunc), wrapped http.HandlerFunc) http.Handler {
	return &wrapper{
		wrapperFunc: wrapperFunc,
		router:      wrapped,
	}
}

func (wr *wrapper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wr.wrapperFunc(w, r, wr.router)
}
