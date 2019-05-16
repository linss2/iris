package router

import (
	"html"
	"net/http"
	"sort"
	"strings"

	"github.com/kataras/golog"

	"github.com/kataras/iris/context"
	"github.com/kataras/iris/core/errors"
	"github.com/kataras/iris/core/netutil"
)

// RequestHandler the middle man between acquiring a context and releasing it.
// By-default is the router algorithm.
type RequestHandler interface {
	// HandleRequest should handle the request based on the Context.
	// HandlerRequest通过Context(这个是iris的Context)来进行处理请求
	HandleRequest(context.Context)

	// Build should builds the handler, it's being called on router's BuildRouter.
	// 在router中的BuildRouter进行调用，建立handler
	Build(provider RoutesProvider) error

	// RouteExists reports whether a particular route exists.
	//判断指定的路由是否存在
	RouteExists(ctx context.Context, method, path string) bool
}

//routerHandler实现了RequestHanlder,说明这里算是一个核心
type routerHandler struct {
	//为啥是数组？因为第一个路径可能不一样
	trees []*trie
	hosts bool // true if at least one route contains a Subdomain.
}

var _ RequestHandler = &routerHandler{}

//这里根据方法类型以及子域来判断
func (h *routerHandler) getTree(method, subdomain string) *trie {
	for i := range h.trees {
		t := h.trees[i]
		//因此可以看出subdomain以及method的不同组合分别可以独立代表一个分支
		if t.method == method && t.subdomain == subdomain {
			return t
		}
	}

	return nil
}

func (h *routerHandler) addRoute(r *Route) error {
	var (
		routeName = r.Name
		method    = r.Method
		subdomain = r.Subdomain
		path      = r.Path
		handlers  = r.Handlers
	)

	t := h.getTree(method, subdomain)

	if t == nil {
		n := newTrieNode()
		// first time we register a route to this method with this subdomain
		t = &trie{method: method, subdomain: subdomain, root: n}
		h.trees = append(h.trees, t)
	}
	//根据method和subdomain直接开始进行填充
	t.insert(path, routeName, handlers)
	return nil
}

// NewDefaultHandler returns the handler which is responsible
// to map the request with a route (aka mux implementation).
// 直接返回一个默认的routerHandler
func NewDefaultHandler() RequestHandler {
	h := &routerHandler{}
	return h
}

// RoutesProvider should be implemented by
// iteral which contains the registered routes.
//(APIBuilder实现了RoutesProvider)
type RoutesProvider interface {
	// api builder
	GetRoutes() []*Route
	GetRoute(routeName string) *Route
}

// iris就是通过这里来实现路由与树的绑定，在router.go中由于是通过interface之间的调用，所以找不到
func (h *routerHandler) Build(provider RoutesProvider) error {
	registeredRoutes := provider.GetRoutes()
	//这里重置了routerHandler的trees
	h.trees = h.trees[0:0] // reset, inneed when rebuilding.

	// sort, subdomains goes first.
	// 这就是将此时的routesProvider的route排序
	// 首先根据路径层次的长度(strings.Count())，然后再通过Route的tmpl字段中的Params字段
	sort.Slice(registeredRoutes, func(i, j int) bool {
		first, second := registeredRoutes[i], registeredRoutes[j]
		lsub1 := len(first.Subdomain)
		lsub2 := len(second.Subdomain)

		firstSlashLen := strings.Count(first.Path, "/")
		secondSlashLen := strings.Count(second.Path, "/")

		if lsub1 == lsub2 && first.Method == second.Method {
			if secondSlashLen < firstSlashLen {
				// fixes order when wildcard root is registered before other wildcard paths
				return true
			}
			if secondSlashLen == firstSlashLen {
				// fixes order when static path with the same prefix with a wildcard path
				// is registered after the wildcard path, although this is managed
				// by the low-level node but it couldn't work if we registered a root level wildcard, this fixes it.
				if len(first.Tmpl().Params) == 0 {
					return false
				}
				if len(second.Tmpl().Params) == 0 {
					return true
				}
			}
		}

		// the rest are handled inside the node
		return lsub1 > lsub2

	})

	//这里的Reporter也是iris自己定义的
	rp := errors.NewReporter()

	for _, r := range registeredRoutes {
		// build the r.Handlers based on begin and done handlers, if any.
		//这就是之前的每个route通过routeHandle.build()来进行整合handler
		r.BuildHandlers()
		//todo hosts=true有什么额外的作用吗(看起来都是拿来判断代码的分支)
		if r.Subdomain != "" {
			h.hosts = true
		}

		// the only "bad" with this is if the user made an error
		// on route, it will be stacked shown in this build state
		// and no in the lines of the user's action, they should read
		// the docs better. Or TODO: add a link here in order to help new users.
		//就是这里将路径转化为trie，来绑定
		//routeHandler的build()并没有地方使用,所以真实的地点在哪里?
		//实际上就是使用interface来调用，所以隐藏了
		// todo 不过哪里代码实现的还需要寻找？
		if err := h.addRoute(r); err != nil {
			// node errors:
			rp.Add("%v -> %s", err, r.String())
			continue
		}
		//这里就是在控制台Debug的显示，这个需要以后再看
		golog.Debugf(r.Trace())
	}

	return rp.Return()
}

func (h *routerHandler) HandleRequest(ctx context.Context) {
	method := ctx.Method()
	path := ctx.Path()
	//ctx.Application().ConfigurationReadOnly()返回iris.Configuration,然后再调用GetDisablePathCorrection()
	// DisablePathCorrection bool的解析可以看 Configuration struct的字段解析
	// DisablePathCorrection就是表示如果 /home/这个没有指定的handler，如果/home 有，则使用/home 的handler
	// (这个要DisablePathCorrection和DisablePathCorrectionRedirection一起配合)
	if !ctx.Application().ConfigurationReadOnly().GetDisablePathCorrection() {

		if len(path) > 1 && strings.HasSuffix(path, "/") {
			// Remove trailing slash and client-permanent rule for redirection,
			// if confgiuration allows that and path has an extra slash.

			// update the new path and redirect.
			r := ctx.Request()
			// use Trim to ensure there is no open redirect due to two leading slashes
			path = "/" + strings.Trim(path, "/")

			r.URL.Path = path
			if !ctx.Application().ConfigurationReadOnly().GetDisablePathCorrectionRedirection() {
				// do redirect, else continue with the modified path without the last "/".
				url := r.URL.String()

				// Fixes https://github.com/kataras/iris/issues/921
				// This is caused for security reasons, imagine a payment shop,
				// you can't just permantly redirect a POST request, so just 307 (RFC 7231, 6.4.7).
				if method == http.MethodPost || method == http.MethodPut {
					ctx.Redirect(url, http.StatusTemporaryRedirect)
					return
				}

				ctx.Redirect(url, http.StatusMovedPermanently)

				// RFC2616 recommends that a short note "SHOULD" be included in the
				// response because older user agents may not understand 301/307.
				// Shouldn't send the response for POST or HEAD; that leaves GET.
				if method == http.MethodGet {
					note := "<a href=\"" +
						html.EscapeString(url) +
						"\">Moved Permanently</a>.\n"

					ctx.ResponseWriter().WriteString(note)
				}
				return
			}

		}
	}

	for i := range h.trees {
		t := h.trees[i]
		if method != t.method {
			continue
		}
		//todo 这里是判断路由中是否有子域
		if h.hosts && t.subdomain != "" {
			//返回当前http请求的url
			requestHost := ctx.Host()
			if netutil.IsLoopbackSubdomain(requestHost) { //这里就是来修复 127.0.0.1这个bug来引起subdomain的问题
				// this fixes a bug when listening on
				// 127.0.0.1:8080 for example
				// and have a wildcard subdomain and a route registered to root domain.
				continue // it's not a subdomain, it's something like 127.0.0.1 probably
			}
			// it's a dynamic wildcard subdomain, we have just to check if ctx.subdomain is not empty
			//todo 这个表示有子域，先暂时不考虑
			if t.subdomain == SubdomainWildcardIndicator { // SubdomainWildcardIndicator="*."
				// mydomain.com -> invalid
				// localhost -> invalid
				// sub.mydomain.com -> valid
				// sub.localhost -> valid
				serverHost := ctx.Application().ConfigurationReadOnly().GetVHost()
				if serverHost == requestHost {
					continue // it's not a subdomain, it's a full domain (with .com...)
				}

				dotIdx := strings.IndexByte(requestHost, '.')
				slashIdx := strings.IndexByte(requestHost, '/')
				if dotIdx > 0 && (slashIdx == -1 || slashIdx > dotIdx) {
					// if "." was found anywhere but not at the first path segment (host).
				} else {
					continue
				}
				// continue to that, any subdomain is valid.
			} else if !strings.HasPrefix(requestHost, t.subdomain) { // t.subdomain contains the dot.
				continue
			}
		}
		//这里暂时只考虑静态路径的流程，动态的先不管，所以ctx.Params()在静态流程中是无所谓的
		n := t.search(path, ctx.Params())
		if n != nil {
			//找到指定的路由，然后设置其名称，然后调用其Handlers
			ctx.SetCurrentRouteName(n.RouteName)
			ctx.Do(n.Handlers)
			// found
			return
		}
		// not found or method not allowed.
		break
	}

	//这下面的逻辑FireMethodNotAllowed表示如果找不到的话用405顶替，而不是404(具体可以看Configuration中的FireMethodNotAllowed字段)
	if ctx.Application().ConfigurationReadOnly().GetFireMethodNotAllowed() {
		for i := range h.trees {
			t := h.trees[i]
			// if `Configuration#FireMethodNotAllowed` is kept as defaulted(false) then this function will not
			// run, therefore performance kept as before.
			// 寻找是否有路由的方法是""的,里面的逻辑跟上面类似，感觉上面也可以用subdomainAndPathAndMethodExists来代替
			if h.subdomainAndPathAndMethodExists(ctx, t, "", path) {
				// RCF rfc2616 https://www.w3.org/Protocols/rfc2616/rfc2616-sec10.html
				// The response MUST include an Allow header containing a list of valid methods for the requested resource.
				//添加这个Allow头文件是因为rfc2616中规定返回405所要求的
				ctx.Header("Allow", t.method)
				ctx.StatusCode(http.StatusMethodNotAllowed)
				return
			}
		}
	}

	ctx.StatusCode(http.StatusNotFound)
}

func (h *routerHandler) subdomainAndPathAndMethodExists(ctx context.Context, t *trie, method, path string) bool {
	if method != "" && method != t.method {
		return false
	}

	if h.hosts && t.subdomain != "" {
		requestHost := ctx.Host()
		if netutil.IsLoopbackSubdomain(requestHost) {
			// this fixes a bug when listening on
			// 127.0.0.1:8080 for example
			// and have a wildcard subdomain and a route registered to root domain.
			return false // it's not a subdomain, it's something like 127.0.0.1 probably
		}
		// it's a dynamic wildcard subdomain, we have just to check if ctx.subdomain is not empty
		if t.subdomain == SubdomainWildcardIndicator {
			// mydomain.com -> invalid
			// localhost -> invalid
			// sub.mydomain.com -> valid
			// sub.localhost -> valid
			serverHost := ctx.Application().ConfigurationReadOnly().GetVHost()
			if serverHost == requestHost {
				return false // it's not a subdomain, it's a full domain (with .com...)
			}

			dotIdx := strings.IndexByte(requestHost, '.')
			slashIdx := strings.IndexByte(requestHost, '/')
			if dotIdx > 0 && (slashIdx == -1 || slashIdx > dotIdx) {
				// if "." was found anywhere but not at the first path segment (host).
			} else {
				return false
			}
			// continue to that, any subdomain is valid.
		} else if !strings.HasPrefix(requestHost, t.subdomain) { // t.subdomain contains the dot.
			return false
		}
	}

	n := t.search(path, ctx.Params())
	return n != nil
}

// RouteExists reports whether a particular route exists
// It will search from the current subdomain of context's host, if not inside the root domain.
func (h *routerHandler) RouteExists(ctx context.Context, method, path string) bool {
	//这里直接通过所有的代表路由的各类树的根节点开始遍历
	for i := range h.trees {
		t := h.trees[i]
		if h.subdomainAndPathAndMethodExists(ctx, t, method, path) {
			return true
		}
	}

	return false
}
