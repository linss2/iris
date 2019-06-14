package router

import (
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/kataras/iris/core/netutil"
	"github.com/kataras/iris/macro"
	"github.com/kataras/iris/macro/interpreter/ast"
	"github.com/kataras/iris/macro/interpreter/lexer"
)

// Param receives a parameter name prefixed with the ParamStart symbol.
// 确保前缀是 ':'
func Param(name string) string {
	return prefix(name, ParamStart)
}

// WildcardParam receives a parameter name prefixed with the WildcardParamStart symbol.
// 如果参数不是 ""，则保证其前缀是 '*'
func WildcardParam(name string) string {
	if len(name) == 0 {
		return ""
	}
	return prefix(name, WildcardParamStart)
}

func convertMacroTmplToNodePath(tmpl macro.Template) string {
	routePath := tmpl.Src
	if len(routePath) > 1 && routePath[len(routePath)-1] == '/' {
		routePath = routePath[0 : len(routePath)-1] // remove any last "/"
	}

	// if it has started with {} and it's valid
	// then the tmpl.Params will be filled,
	// so no any further check needed.
	for _, p := range tmpl.Params {
		if ast.IsTrailing(p.Type) {
			routePath = strings.Replace(routePath, p.Src, WildcardParam(p.Name), 1)
		} else {
			routePath = strings.Replace(routePath, p.Src, Param(p.Name), 1)
		}
	}

	return routePath
}

// 如果确保 s 的前缀是 prefix
func prefix(s string, prefix string) string {
	if !strings.HasPrefix(s, prefix) {
		return prefix + s
	}

	return s
}

func suffix(s string, suffix string) string {
	if !strings.HasSuffix(s, suffix) {
		return s + suffix
	}
	return s
}

func splitMethod(methodMany string) []string {
	methodMany = strings.Trim(methodMany, " ")
	return strings.Split(methodMany, " ")
}

func splitPath(pathMany string) (paths []string) {
	pathMany = strings.Trim(pathMany, " ")
	pathsWithoutSlashFromFirstAndSoOn := strings.Split(pathMany, " /")
	for _, path := range pathsWithoutSlashFromFirstAndSoOn {
		if path == "" {
			continue
		}
		if path[0] != '/' {
			path = "/" + path
		}
		paths = append(paths, path)
	}
	return
}

func joinPath(path1 string, path2 string) string {
	return path.Join(path1, path2)
}

// cleanPath applies the following rules
// iteratively until no further processing can be done:
//
//  1. Replace multiple slashes(/) with a single slash.
//  1. // -> /
//  2. Replace '\' with '/'
//  2. \ -> /
//  3. Replace "\\" with '/'
//  3. \\ -> /
//  4. Ignore anything inside '{' and '}'
//  4. 忽视{}里面的内容
//  5. Makes sure that prefixed with '/'
//  5. 保证以/开头
//  6. Remove trailing '/'.
//  6. 删除尾部的 '/'
//
// The returned path ends in a slash only if it is the root "/".
func cleanPath(s string) string {
	// note that we don't care about the performance here, it's before the server ran.
	if s == "" || s == "." {
		return "/"
	}

	// remove suffix "/", if it's root "/" then it will add it as a prefix below.
	if lidx := len(s) - 1; s[lidx] == '/' {
		s = s[:lidx]
	}

	// prefix with "/".
	s = prefix(s, "/")

	// If you're learning go through Iris I will ask you to ignore the
	// following part, it's not the recommending way to do that,
	// but it's understable to me.
	// todo 这边说Macros不够稳定，可能会修改,Macro 想了解可以看下，Macro是针对{id:int}这种东西来处理的
	var (
		insideMacro = false
		i           = -1
	)

	for {
		i++
		if len(s) <= i {
			break
		}

		if s[i] == lexer.Begin {
			insideMacro = true
			continue
		}

		if s[i] == lexer.End {
			insideMacro = false
			continue
		}

		// when inside {} then don't try to clean it.
		if !insideMacro {
			if s[i] == '/' {
				if len(s)-1 >= i+1 && s[i+1] == '/' { // we have "//".
					bckp := s
					s = bckp[:i] + "/"
					// forward two, we ignore the second "/" in the raw.
					i = i + 2
					if len(bckp)-1 >= i {
						s += bckp[i:]
					}
				}
				// if we have just a single slash then continue.
				continue
			}

			if s[i] == '\\' { // this will catch "\\" and "\".
				bckp := s
				s = bckp[:i] + "/"

				if len(bckp)-1 >= i+1 {
					s += bckp[i+1:]
					i++
				}

				if len(s)-1 > i && s[i] == '\\' {
					bckp := s
					s = bckp[:i]
					if len(bckp)-1 >= i+2 {
						s = bckp[:i-1] + bckp[i+1:]
						i++
					}
				}

				continue
			}

		}

	}

	return s
}

const (
	// SubdomainWildcardIndicator where a registered path starts with '*.'.
	// if subdomain == "*." then its wildcard.
	//
	// used internally by router and api builder.
	SubdomainWildcardIndicator = "*."

	// SubdomainWildcardPrefix where a registered path starts with "*./",
	// then this route should accept any subdomain.
	SubdomainWildcardPrefix = SubdomainWildcardIndicator + "/"
	// SubdomainPrefix where './' exists in a registered path then it contains subdomain
	//
	// used on api builder.
	SubdomainPrefix = "./" // i.e subdomain./ -> Subdomain: subdomain. Path: /
)

//是否是subdomain的判断条件是
//	1:存在"/"，但不是开头， xx/xx
//	2:以"."开始
//  3:是以"*."开始
//  4:末尾是'.'且长度大于2，/索引不是第一个
func hasSubdomain(s string) bool {
	if s == "" {
		return false
	}

	// subdomain./path
	// .*/path
	//
	// remember: path always starts with "/"
	// if not start with "/" then it should be something else,
	// we don't assume anything else but subdomain.
	slashIdx := strings.IndexByte(s, '/')
	return slashIdx > 0 || // for route paths
		s[0] == SubdomainPrefix[0] || // for route paths
		(len(s) >= 2 && s[0:2] == SubdomainWildcardIndicator) || // for party rel path or route paths
		(len(s) >= 2 && slashIdx != 0 && s[len(s)-1] == '.') // for party rel, i.e www., or subsub.www.
}

// splitSubdomainAndPath checks if the path has subdomain and if it's
// it splits the subdomain and path and returns them, otherwise it returns
// an empty subdomain and the clean path.
//
// First return value is the subdomain, second is the path.
// subdomain 是 /之前的字符串，即如果 fullPath 以 / 开头，则subdomain 为""
func splitSubdomainAndPath(fullUnparsedPath string) (subdomain string, path string) {
	s := fullUnparsedPath
	if s == "" || s == "/" {
		return "", "/"
	}

	// 这里似乎就是路由那里设置的path以及api_builder中的relativePath配合
	// 这里的subdomain 意思就是 '/' 之前的值
	slashIdx := strings.IndexByte(s, '/')
	if slashIdx == 0 {
		// no subdomain
		return "", cleanPath(s)
	}

	return s[0:slashIdx], cleanPath(s[slashIdx:]) // return subdomain without slash, path with slash
}

// RoutePathReverserOption option signature for the RoutePathReverser.
type RoutePathReverserOption func(*RoutePathReverser)

// WithScheme is an option for the RoutepathReverser,
// it sets the optional field "vscheme",
// v for virtual.
// if vscheme is empty then it will try to resolve it from
// the RoutePathReverser's vhost field.
//
// See WithHost or WithServer to enable the URL feature.
func WithScheme(scheme string) RoutePathReverserOption {
	return func(ps *RoutePathReverser) {
		ps.vscheme = scheme
	}
}

// WithHost enables the RoutePathReverser's URL feature.
// Both "WithHost" and "WithScheme" can be different from
// the real server's listening address, i.e nginx in front.
func WithHost(host string) RoutePathReverserOption {
	return func(ps *RoutePathReverser) {
		ps.vhost = host
		if ps.vscheme == "" {
			ps.vscheme = netutil.ResolveSchemeFromVHost(host)
		}
	}
}

// WithServer enables the RoutePathReverser's URL feature.
// It receives an *http.Server and tries to resolve
// a scheme and a host to be used in the URL function.
func WithServer(srv *http.Server) RoutePathReverserOption {
	return func(ps *RoutePathReverser) {
		ps.vhost = netutil.ResolveVHost(srv.Addr)
		ps.vscheme = netutil.ResolveSchemeFromServer(srv)
	}
}

// RoutePathReverser contains methods that helps to reverse a
// (dynamic) path from a specific route,
// route name is required because a route may being registered
// on more than one http method.
type RoutePathReverser struct {
	provider RoutesProvider
	// both vhost and vscheme are being used, optionally, for the URL feature.
	vhost   string
	vscheme string
}

// NewRoutePathReverser returns a new path reverser based on
// a routes provider, needed to get a route based on its name.
// Options is required for the URL function.
// See WithScheme and WithHost or WithServer.
// 在iris.go 中如果app.View()的长度大于0，则可以调用这个，传进去的是API_Builder
// 其中的RoutePathReverserOption 来处理 RoutePathReverser 修改的是其字段属性，比如vhost、vscheme
func NewRoutePathReverser(apiRoutesProvider RoutesProvider, options ...RoutePathReverserOption) *RoutePathReverser {
	ps := &RoutePathReverser{
		provider: apiRoutesProvider,
	}
	for _, o := range options {
		o(ps)
	}
	return ps
}

// Path  returns a route path based on a route name and any dynamic named parameter's values-only.
func (ps *RoutePathReverser) Path(routeName string, paramValues ...interface{}) string {
	r := ps.provider.GetRoute(routeName)
	if r == nil {
		return ""
	}

	if len(paramValues) == 0 {
		return r.Path
	}

	return r.ResolvePath(toStringSlice(paramValues)...)
}

func toStringSlice(args []interface{}) (argsString []string) {
	argsSize := len(args)
	if argsSize <= 0 {
		return
	}

	argsString = make([]string, argsSize, argsSize)
	for i, v := range args {
		if s, ok := v.(string); ok {
			argsString[i] = s
		} else if num, ok := v.(int); ok {
			argsString[i] = strconv.Itoa(num)
		} else if b, ok := v.(bool); ok {
			argsString[i] = strconv.FormatBool(b)
		} else if arr, ok := v.([]string); ok {
			if len(arr) > 0 {
				argsString[i] = arr[0]
				argsString = append(argsString, arr[1:]...)
			}
		}
	}
	return
}

// Remove the URL for now, it complicates things for the whole framework without a specific benefits,
// developers can just concat the subdomain, (host can be auto-retrieve by browser using the Path).

// URL same as Path but returns the full uri, i.e https://mysubdomain.mydomain.com/hello/iris
func (ps *RoutePathReverser) URL(routeName string, paramValues ...interface{}) (url string) {
	if ps.vhost == "" || ps.vscheme == "" {
		return "not supported"
	}

	r := ps.provider.GetRoute(routeName)
	if r == nil {
		return
	}

	host := ps.vhost
	scheme := ps.vscheme
	args := toStringSlice(paramValues)

	// if it's dynamic subdomain then the first argument is the subdomain part
	// for this part we are responsible not the custom routers
	if len(args) > 0 && r.Subdomain == SubdomainWildcardIndicator {
		subdomain := args[0]
		host = subdomain + "." + host
		args = args[1:] // remove the subdomain part for the arguments,
	}

	if parsedPath := r.ResolvePath(args...); parsedPath != "" {
		url = scheme + "://" + host + parsedPath
	}

	return
}
