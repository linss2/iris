package router

import (
	"github.com/kataras/iris/context"
)

// ExecutionRules gives control to the execution of the route handlers outside of the handlers themselves.
// Usage:
// Party#SetExecutionRules(ExecutionRules {
//   Done: ExecutionOptions{Force: true},
// })
//
// See `Party#SetExecutionRules` for more.
// 具体例子可以看handler_execution_rules_test.go
// Main成功了，则Begin和Done就不会起作用了,Main失败了，则Main从0开始到倒数第二个都是与begin联系，Main的倒数第一个与Done联系
// （其实这里的Begin和Done和Main实际的效果都是一样的，都是自动进行handler链的下一个来处理
type ExecutionRules struct {
	// Begin applies from `Party#Use`/`APIBUilder#UseGlobal` to the first...!last `Party#Handle`'s IF main handlers > 1.
	// Begin 作用在 从Use[all]/APIBuilder#UseGlobal  到Handle[last]之前  程序执行
	Begin ExecutionOptions
	// Done applies to the latest `Party#Handle`'s (even if one) and all done handlers.
	// Done 用在最后的 Party#Handle 和所有的 done handlers
	Done ExecutionOptions
	// Main applies to the `Party#Handle`'s all handlers, plays nice with the `Done` rule
	// when more than one handler was registered in `Party#Handle` without `ctx.Next()` (for Force: true).
	// Main 作用在 所有的 Party#Handle 上
	Main ExecutionOptions
}

func handlersNames(handlers context.Handlers) (names []string) {
	for _, h := range handlers {
		if h == nil {
			continue
		}
		// todo context.HandlerName()源码阅读？？
		names = append(names, context.HandlerName(h))
	}

	return
}

func applyExecutionRules(rules ExecutionRules, begin, done, main *context.Handlers) {
	if !rules.Begin.Force && !rules.Done.Force && !rules.Main.Force {
		return // do not proceed and spend buld-time here if nothing changed.
	}
	// 这里apply就是来封装 context.Handlers 里面每一个handler
	beginOK := rules.Begin.apply(begin)
	mainOK := rules.Main.apply(main)
	doneOK := rules.Done.apply(done)

	// 这是专门针对main这里失败的情况
	if !mainOK {
		mainCp := (*main)[0:]

		lastIdx := len(mainCp) - 1

		if beginOK {
			if len(mainCp) > 1 {
				mainCpFirstButNotLast := make(context.Handlers, lastIdx)
				copy(mainCpFirstButNotLast, mainCp[:lastIdx])
				//这里的意思应该是让begin处理好了后，直接继续调用mainHandler
				for i, h := range mainCpFirstButNotLast {
					// todo 这里应该将原来的都覆盖了？？？
					(*main)[i] = rules.Begin.buildHandler(h)
				}
			}
		}

		if doneOK {
			latestMainHandler := mainCp[lastIdx]
			// 这里表示等done全部处理好了后，在调用mainCp最后一个
			(*main)[lastIdx] = rules.Done.buildHandler(latestMainHandler)
		}
	}
}

// ExecutionOptions is a set of default behaviors that can be changed in order to customize the execution flow of the routes' handlers with ease.
//
// See `ExecutionRules` and `Party#SetExecutionRules` for more.
// 这个参数就是判断是否行为可以被修改来形成自定义的执行顺序
type ExecutionOptions struct {
	// Force if true then the handler9s) will execute even if the previous (or/and current, depends on the type of the rule)
	// handler does not calling the `ctx.Next()`,
	// note that the only way remained to stop a next handler is with the `ctx.StopExecution()` if this option is true.
	//
	// If true and `ctx.Next()` exists in the handlers that it shouldn't be, the framework will understand it but use it wisely.
	//
	// Defaults to false.
	// Force如果为true，则handler会自己执行，就算前面的handler没有调用ctx.Next()，只有ctx.StopExecution()才能停止
	// 不应该出现在true的时候又调用ctx.Next()，虽然框架可以只能的正确使用
	Force bool
}

func (e ExecutionOptions) buildHandler(h context.Handler) context.Handler {
	if !e.Force {
		return h
	}

	return func(ctx context.Context) {
		// Proceed will fire the handler and return false here if it doesn't contain a `ctx.Next()`,
		// so we add the `ctx.Next()` wherever is necessary in order to eliminate any dev's misuse.
		// 会通过ctx.Proceed()来判断是否有ctx.Next()，如果没有，则调用让其自动调用下一个
		// 这里不用担心下面没有了还调用Next()，因为在Next()超过则略过
		if !ctx.Proceed(h) {
			// `ctx.Next()` always checks for `ctx.IsStopped()` and handler(s) positions by-design.
			ctx.Next()
		}
	}
}

func (e ExecutionOptions) apply(handlers *context.Handlers) bool {
	if !e.Force {
		return false
	}

	tmp := *handlers

	for i, h := range tmp {
		if h == nil {
			if len(tmp) == 1 {
				return false
			}
			continue
		}
		// 让每一个handle多了一层builderHandler封装
		(*handlers)[i] = e.buildHandler(h)
	}

	return true
}
