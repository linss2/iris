package router

import (
	"strings"

	"github.com/kataras/iris/context"
)

const (
	// ParamStart the character in string representation where the underline router starts its dynamic named parameter.
	ParamStart = ":"
	// WildcardParamStart the character in string representation where the underline router starts its dynamic wildcard
	// path parameter.
	WildcardParamStart = "*"
)

// An iris-specific identical version of the https://github.com/kataras/muxie version 1.0.0 released at 15 Oct 2018
// trie才是路由里的节点
type trieNode struct {
	//一对一父节点
	parent *trieNode
	//一对多子节点，map的key是啥？(175行可以看出是path)
	children map[string]*trieNode

	//判断是否有动态子节点 暂时不考虑
	hasDynamicChild        bool // does one of the children contains a parameter or wildcard?
	childNamedParameter    bool // is the child a named parameter (single segmnet)
	childWildcardParameter bool // or it is a wildcard (can be more than one path segments) ?

	//todo  这个还不是特别理解 没有:和*的param
	paramKeys []string // the param keys without : or *.

	//判断这个是否是叶子节点
	end bool // it is a complete node, here we stop and we can say that the node is valid.

	//如果是叶子节点，代表这个叶子节点的完整的路径 187行
	key string // if end == true then key is filled with the original value of the insertion's key.

	// if key != "" && its parent has childWildcardParameter == true,
	// we need it to track the static part for the closest-wildcard's parameter storage.
	//如果key!=""且他的兄弟节点有动态路径，则保存最长的路径存储
	staticKey string

	// insert data.
	//记录到当前的节点的路由
	Handlers  context.Handlers
	RouteName string
}

func newTrieNode() *trieNode {
	n := new(trieNode)
	return n
}

func (tn *trieNode) hasChild(s string) bool {
	return tn.getChild(s) != nil
}

//通过children中的key来判断是否有
func (tn *trieNode) getChild(s string) *trieNode {
	if tn.children == nil {
		return nil
	}

	return tn.children[s]
}

//添加子节点,如果子节点已经存在，则直接返回(因此以第一个为准)
func (tn *trieNode) addChild(s string, n *trieNode) {
	if tn.children == nil {
		tn.children = make(map[string]*trieNode)
	}

	if _, exists := tn.children[s]; exists {
		return
	}

	n.parent = tn
	tn.children[s] = n
}

//寻找到最上层的静态的动态路由中静态的部分，如果没有动态路由，则返回nil
//这里动态路由是通过寻找接下来的第一个key为*的节点
func (tn *trieNode) findClosestParentWildcardNode() *trieNode {
	tn = tn.parent
	for tn != nil {
		if tn.childWildcardParameter {
			return tn.getChild(WildcardParamStart)
		}

		tn = tn.parent
	}

	return nil
}

//返回当前key所代表的路径
func (tn *trieNode) String() string {
	return tn.key
}

// 这个才是路由里的节点，包含了trieNode
type trie struct {
	//这个表示此时的节点里面的数据
	root *trieNode

	// if true then it will handle any path if not other parent wildcard exists,
	// so even 404 (on http services) is up to it, see trie#insert.
	// 如果刚开始的路径就是动态路由，则这个为true
	hasRootWildcard bool

	//如果是根节点，则为因为路径只有/，则为true
	hasRootSlash bool

	method string

	// subdomain is empty for default-hostname routes,
	// ex: mysubdomain.
	subdomain string
}

func newTrie() *trie {
	return &trie{
		root: newTrieNode(),
	}
}

const (
	pathSep  = "/"
	pathSepB = '/'
)

//将路径进行分割处理
func slowPathSplit(path string) []string {
	if path == "/" {
		return []string{"/"}
	}

	return strings.Split(path, pathSep)[1:]
}

//handler.go中addRoute()中使用
func (tr *trie) insert(path, routeName string, handlers context.Handlers) {
	input := slowPathSplit(path)

	n := tr.root
	if path == pathSep {
		tr.hasRootSlash = true
	}

	var paramKeys []string

	for _, s := range input {
		//这里是拿到每个//之间的数据，判断第一个值是否是*或:来判断是否是动态路由
		c := s[0]

		if isParam, isWildcard := c == ParamStart[0], c == WildcardParamStart[0]; isParam || isWildcard {
			n.hasDynamicChild = true
			paramKeys = append(paramKeys, s[1:]) // without : or *.

			// if node has already a wildcard, don't force a value, check for true only.
			if isParam {
				n.childNamedParameter = true
				s = ParamStart
			}

			if isWildcard {
				n.childWildcardParameter = true
				s = WildcardParamStart
				if tr.root == n { //判断根节点开始就是动态路由
					tr.hasRootWildcard = true
				}
			}
		}
		//判断这个路径是否已经存在，如果不存在，则创建一个新的节点
		if !n.hasChild(s) {
			child := newTrieNode()
			n.addChild(s, child)
		}
		//然后再下一层
		n = n.getChild(s)
	}
	//此时的n表示当前路径所对应的叶子节点
	n.RouteName = routeName
	n.Handlers = handlers
	n.paramKeys = paramKeys
	n.key = path
	n.end = true

	//todo 由于现在暂时不考虑静态路由，则先跳过
	i := strings.Index(path, ParamStart)
	if i == -1 {
		i = strings.Index(path, WildcardParamStart)
	}
	if i == -1 {
		i = len(n.key)
	}
	//静态路径则是得到动态路由之前的固定路由
	n.staticKey = path[:i]
}

//context.RequestParams表示动态路径的时候，存储的key value值，如果是静态路径，则为空
//这个查询方式不是模糊查询
func (tr *trie) search(q string, params *context.RequestParams) *trieNode {
	end := len(q)

	//如果q为""或"/"
	if end == 0 || (end == 1 && q[0] == pathSepB) {
		// fixes only root wildcard but no / registered at.
		//有一个完整路径为"/"时，hasRootSlash才为true
		if tr.hasRootSlash {
			return tr.root.getChild(pathSep)
		} else if tr.hasRootWildcard {
			// no need to going through setting parameters, this one has not but it is wildcard.
			//或者是起点是"*"开始的
			return tr.root.getChild(WildcardParamStart)
		}

		return nil
	}

	n := tr.root
	start := 1
	i := 1
	var paramValues []string

	for {//每次拿到/与/之间的数据
		if i == end || q[i] == pathSepB { //当path到末尾或者是/，
			if child := n.getChild(q[start:i]); child != nil {
				n = child
			} else if n.childNamedParameter {
				n = n.getChild(ParamStart)
				if ln := len(paramValues); cap(paramValues) > ln {
					paramValues = paramValues[:ln+1]
					paramValues[ln] = q[start:i]
				} else {
					paramValues = append(paramValues, q[start:i])
				}
			} else if n.childWildcardParameter {
				n = n.getChild(WildcardParamStart)
				if ln := len(paramValues); cap(paramValues) > ln {
					paramValues = paramValues[:ln+1]
					paramValues[ln] = q[start:]
				} else {
					paramValues = append(paramValues, q[start:])
				}
				break
			} else {
				n = n.findClosestParentWildcardNode()
				if n != nil {
					// means that it has :param/static and *wildcard, we go trhough the :param
					// but the next path segment is not the /static, so go back to *wildcard
					// instead of not found.
					//
					// Fixes:
					// /hello/*p
					// /hello/:p1/static/:p2
					// req: http://localhost:8080/hello/dsadsa/static/dsadsa => found
					// req: http://localhost:8080/hello/dsadsa => but not found!
					// and
					// /second/wild/*p
					// /second/wild/static/otherstatic/
					// req: /second/wild/static/otherstatic/random => but not found!
					params.Set(n.paramKeys[0], q[len(n.staticKey):])
					return n
				}

				return nil
			}

			if i == end {
				break
			}

			i++
			start = i
			continue
		}

		i++
	}
	//如果查询的q得到的路径是nil或者不是叶子节点
	if n == nil || !n.end {
		if n != nil { // we need it on both places, on last segment (below) or on the first unnknown (above).
			//则返回表示最长的表示:开始的节点
			if n = n.findClosestParentWildcardNode(); n != nil {
				params.Set(n.paramKeys[0], q[len(n.staticKey):])
				return n
			}
		}
		//如果根路径就是动态路由，则wildcardParamStart
		if tr.hasRootWildcard {
			// that's the case for root wildcard, tests are passing
			// even without it but stick with it for reference.
			// Note ote that something like:
			// Routes: /other2/*myparam and /other2/static
			// Reqs: /other2/staticed will be handled
			// the /other2/*myparam and not the root wildcard, which is what we want.
			//
			n = tr.root.getChild(WildcardParamStart)
			params.Set(n.paramKeys[0], q[1:])
			return n
		}

		return nil
	}

	//todo 这些都是动态路由的事情，以后再弄
	for i, paramValue := range paramValues {
		if len(n.paramKeys) > i {
			params.Set(n.paramKeys[i], paramValue)
		}
	}

	return n
}
