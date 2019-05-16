package router

import (
	"fmt"
	"strings"

	"github.com/kataras/iris/context"
	"github.com/kataras/iris/macro"
	"github.com/kataras/iris/macro/handler"
)

// Route contains the information about a registered Route.
// If any of the following fields are changed then the
// caller should Refresh the router.
type Route struct {
	Name   string         `json:"name"`   // "userRoute"
	Method string         `json:"method"` // "GET"

	//methodBckp用在route会变更method的时候记录旧的状态
	methodBckp string                            // if Method changed to something else (which is possible at runtime as well, via RefreshRouter) then this field will be filled with the old one.
	Subdomain  string         `json:"subdomain"` // "admin."

	// todo tmpl看其类型，是与macro有关
	tmpl macro.Template // Tmpl().Src: "/api/user/{id:uint64}"

	// temp storage, they're appended to the Handlers on build.
	// Execution happens before Handlers, can be empty.
	//表示路由实际运行逻辑前要运行的准备，在build的时候，比如准备什么代码逻辑
	beginHandlers context.Handlers

	// Handlers are the main route's handlers, executed by order.
	// Cannot be empty.
	//主要的代码处理部分，不能为空 context.Handlers -> []Handler
	Handlers context.Handlers `json:"-"`

	//默认是主handlers的第一个handler的命名
	MainHandlerName string           `json:"mainHandlerName"`

	// temp storage, they're appended to the Handlers on build.
	// Execution happens after Begin and main Handler(s), can be empty.
	//表示路由实际运行逻辑后要运行的准备，在build的时候，比如一些资源的释放
	doneHandlers context.Handlers

	//路由路径
	Path string `json:"path"` // "/api/user/:id"

	// FormattedPath all dynamic named parameters (if any) replaced with %v,
	// used by Application to validate param values of a Route based on its name.
	// todo 这个是用于动态路径，不影响大致逻辑
	FormattedPath string `json:"formattedPath"`
}

// NewRoute returns a new route based on its method,
// subdomain, the path (unparsed or original),
// handlers and the macro container which all routes should share.
// It parses the path based on the "macros",
// handlers are being changed to validate the macros at serve time, if needed.
// 程序刚运行的时候，APIBuilder.Handle()的时候调用的
func NewRoute(method, subdomain, unparsedPath, mainHandlerName string,
	handlers context.Handlers, macros macro.Macros) (*Route, error) {

	//************这里开始处理path************
	//todo 学习macro的处理
	tmpl, err := macro.Parse(unparsedPath, macros)
	if err != nil {
		return nil, err
	}

	path := convertMacroTmplToNodePath(tmpl)
	// prepend the macro handler to the route, now,
	// right before the register to the tree, so APIBuilder#UseGlobal will work as expected.
	if handler.CanMakeHandler(tmpl) {
		macroEvaluatorHandler := handler.MakeHandler(tmpl)
		handlers = append(context.Handlers{macroEvaluatorHandler}, handlers...)
	}
	//************处理好path，包括原始、macro等************
	path = cleanPath(path) // maybe unnecessary here but who cares in this moment

	//在_example/routing/basic/main.go中直接使用localhost:8080得到tmpl.Src="/"
	defaultName := method + subdomain + tmpl.Src
	formattedPath := formatPath(path)

	route := &Route{
		Name:            defaultName,
		Method:          method,
		methodBckp:      method,
		Subdomain:       subdomain,
		tmpl:            tmpl,
		Path:            path,
		Handlers:        handlers,
		MainHandlerName: mainHandlerName,
		FormattedPath:   formattedPath,
	}
	return route, nil
}

// use adds explicit(明确的) begin handlers(middleware) to this route,
// It's being called internally, it's useless for outsiders
// because `Handlers` field is exported.
// The callers of this function are: `APIBuilder#UseGlobal` and `APIBuilder#Done`.
//
// BuildHandlers should be called to build the route's `Handlers`.
// 这个针对route增加前置中间件，使用`APIBuilder#UseGlobal` and `APIBuilder#Done`内部调用的是
// 当前方法
func (r *Route) use(handlers context.Handlers) {
	if len(handlers) == 0 {
		return
	}
	r.beginHandlers = append(r.beginHandlers, handlers...)
}

// use adds explicit done handlers to this route.
// It's being called internally, it's useless for outsiders
// because `Handlers` field is exported.
// The callers of this function are: `APIBuilder#UseGlobal` and `APIBuilder#Done`.
//
// BuildHandlers should be called to build the route's `Handlers`.
//同理，与use(handlers context.Handlers)一样
func (r *Route) done(handlers context.Handlers) {
	if len(handlers) == 0 {
		return
	}
	r.doneHandlers = append(r.doneHandlers, handlers...)
}

// ChangeMethod will try to change the HTTP Method of this route instance.
// A call of `RefreshRouter` is required after this type of change in order to change to be really applied.
//可以使用当前方法来修改当前路由实例的HTTP方法，在使用后要调用RefreshRouter才能生效
//这里使用methodBckp字段来记录旧的method
func (r *Route) ChangeMethod(newMethod string) bool {
	if newMethod != r.Method {
		r.methodBckp = r.Method
		r.Method = newMethod
		return true
	}

	return false
}

// SetStatusOffline will try make this route unavailable.
// A call of `RefreshRouter` is required after this type of change in order to change to be really applied.
// iris中有一个新的方法是None(变量是MethodNone)来表示让这个路由失效,同理使用后要调用RefreshRouter生效
func (r *Route) SetStatusOffline() bool {
	return r.ChangeMethod(MethodNone)
}

// RestoreStatus will try to restore the status of this route instance, i.e if `SetStatusOffline` called on a "GET" route,
// then this function will make this route available with "GET" HTTP Method.
// Note if that you want to set status online for an offline registered route then you should call the `ChangeMethod` instead.
// It will return true if the status restored, otherwise false.
// A call of `RefreshRouter` is required after this type of change in order to change to be really applied.
// 看内在代码就是将HTTP调用之前的method
func (r *Route) RestoreStatus() bool {
	return r.ChangeMethod(r.methodBckp)
}

// BuildHandlers is executed automatically by the router handler
// at the `Application#Build` state. Do not call it manually, unless
// you were defined your own request mux handler.
// 将所有的handler整合到Handler中，beginHanlers前，handlers中，doneHandlers后，然后清空begin和done
// 在Application.Build()中被调用(不要自己手动调用，除非定义了自己的路由处理器)
// 可以看例子_example/routing/custom-high-level-router的例子(看那例子，感觉是自己在拦截器中多套了一层)
func (r *Route) BuildHandlers() {
	if len(r.beginHandlers) > 0 {
		r.Handlers = append(r.beginHandlers, r.Handlers...)
		r.beginHandlers = r.beginHandlers[0:0]
	}

	if len(r.doneHandlers) > 0 {
		r.Handlers = append(r.Handlers, r.doneHandlers...)
		r.doneHandlers = r.doneHandlers[0:0]
	} // note: no mutex needed, this should be called in-sync when server is not running of course.
}

// String returns the form of METHOD, SUBDOMAIN, TMPL PATH.
//路由的名称以 方法名、子域、r.Tmpl().Src
func (r Route) String() string {
	return fmt.Sprintf("%s %s%s",
		r.Method, r.Subdomain, r.Tmpl().Src)
}

// Tmpl returns the path template,
// it contains the parsed template
// for the route's path.
// May contain zero named parameters.
//
// Developer can get his registered path
// via Tmpl().Src, Route.Path is the path
// converted to match the underline router's specs.
func (r Route) Tmpl() macro.Template {
	return r.tmpl
}

// RegisteredHandlersLen returns the end-developer's registered handlers, all except the macro evaluator handler
// if was required by the build process.
//这方法是在Trace()被调用，而Trace()在handler.go中的build用golog.DEBUGF()调用
func (r Route) RegisteredHandlersLen() int {
	n := len(r.Handlers)
	if handler.CanMakeHandler(r.tmpl) {
		n--
	}

	return n
}

// IsOnline returns true if the route is marked as "online" (state).
//判断是否是可以访问到的路由
func (r Route) IsOnline() bool {
	return r.Method != MethodNone
}

// formats the parsed to the underline path syntax.
// path = "/api/users/:id"
// return "/api/users/%v"
//
// path = "/files/*file"
// return /files/%v
//
// path = "/:username/messages/:messageid"
// return "/%v/messages/%v"
// we don't care about performance here, it's prelisten.
func formatPath(path string) string {
	//通过判断path是否有*或者:来进行处理，如果没有则直接返回
	//todo 具体内容以后到macro再看
	if strings.Contains(path, ParamStart) || strings.Contains(path, WildcardParamStart) {
		var (
			startRune         = ParamStart[0]
			wildcardStartRune = WildcardParamStart[0]
		)

		var formattedParts []string
		parts := strings.Split(path, "/")
		for _, part := range parts {
			if len(part) == 0 {
				continue
			}
			if part[0] == startRune || part[0] == wildcardStartRune {
				// is param or wildcard param
				part = "%v"
			}
			formattedParts = append(formattedParts, part)
		}

		return "/" + strings.Join(formattedParts, "/")
	}
	// the whole path is static just return it
	return path
}

// StaticPath returns the static part of the original, registered route path.
// if /user/{id} it will return /user
// if /user/{id}/friend/{friendid:uint64} it will return /user too
// if /assets/{filepath:path} it will return /assets.
//返回最左边的静态路径
func (r Route) StaticPath() string {
	src := r.tmpl.Src
	bidx := strings.IndexByte(src, '{')
	if bidx == -1 || len(src) <= bidx {
		return src // no dynamic part found
	}
	if bidx == 0 { // found at first index,
		// but never happens because of the prepended slash
		return "/"
	}

	return src[:bidx]
}

// ResolvePath returns the formatted path's %v replaced with the args.
// 通过参数将%v处理
// todo 有关动态路由，以后再看
func (r Route) ResolvePath(args ...string) string {
	rpath, formattedPath := r.Path, r.FormattedPath
	if rpath == formattedPath {
		// static, no need to pass args
		return rpath
	}
	// check if we have /*, if yes then join all arguments to one as path and pass that as parameter
	if rpath[len(rpath)-1] == WildcardParamStart[0] {
		parameter := strings.Join(args, "/")
		return fmt.Sprintf(formattedPath, parameter)
	}
	// else return the formattedPath with its args,
	// the order matters.
	for _, s := range args {
		formattedPath = strings.Replace(formattedPath, "%v", s, 1)
	}
	return formattedPath
}

// Trace returns some debug infos as a string sentence.
// Should be called after Build.
func (r Route) Trace() string {
	printfmt := fmt.Sprintf("%s:", r.Method)
	if r.Subdomain != "" {
		printfmt += fmt.Sprintf(" %s", r.Subdomain)
	}
	printfmt += fmt.Sprintf(" %s ", r.Tmpl().Src)

	if l := r.RegisteredHandlersLen(); l > 1 {
		printfmt += fmt.Sprintf("-> %s() and %d more", r.MainHandlerName, l-1)
	} else {
		printfmt += fmt.Sprintf("-> %s()", r.MainHandlerName)
	}

	// printfmt := fmt.Sprintf("%s: %s >> %s", r.Method, r.Subdomain+r.Tmpl().Src, r.MainHandlerName)
	// if l := len(r.Handlers); l > 0 {
	// 	printfmt += fmt.Sprintf(" and %d more", l)
	// }
	return printfmt // without new line.
}

type routeReadOnlyWrapper struct {
	*Route
}

func (rd routeReadOnlyWrapper) Method() string {
	return rd.Route.Method
}

func (rd routeReadOnlyWrapper) Name() string {
	return rd.Route.Name
}

func (rd routeReadOnlyWrapper) Subdomain() string {
	return rd.Route.Subdomain
}

//这里的path竟然是tmpl.Src返回的额?
func (rd routeReadOnlyWrapper) Path() string {
	return rd.Route.tmpl.Src
}

func (rd routeReadOnlyWrapper) Trace() string {
	return rd.Route.Trace()
}

func (rd routeReadOnlyWrapper) Tmpl() macro.Template {
	return rd.Route.Tmpl()
}

func (rd routeReadOnlyWrapper) MainHandlerName() string {
	return rd.Route.MainHandlerName
}
