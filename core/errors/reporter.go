package errors

import (
	"sync"
)

// StackError contains the Stack method.
type StackError interface {
	Stack() []Error
	Error() string
}

// PrintAndReturnErrors prints the "err" to the given "printer",
// printer will be called multiple times if the "err" is a StackError, where it contains more than one error.
func PrintAndReturnErrors(err error, printer func(string, ...interface{})) error {
	if err == nil || err.Error() == "" {
		return nil
	}
	// 从这里看，错误只有两层，一种是StackError ，然后是Error的Stack
	// StackError 中的[]Error 不可能是StackError 因为没有实现Stack()接口
	if stackErr, ok := err.(StackError); ok {
		if len(stackErr.Stack()) == 0 {
			return nil
		}

		stack := stackErr.Stack()

		for _, e := range stack {
			if e.HasStack() {
				for _, es := range e.Stack {
					printer("%v", es)
				}
				continue
			}
			printer("%v", e)
		}

		return stackErr
	}

	printer("%v", err)
	return err
}

// Reporter is a helper structure which can
// stack errors and prints them to a printer of func(string).
type Reporter struct {
	// 这里并发控制
	mu      sync.Mutex
	// iris 中封装的 Error
	wrapper Error
}

// NewReporter returns a new empty error reporter.
// NewReporter 生成了wrapper 里是空的iris 封装的Error，预计后面要被替换
func NewReporter() *Reporter {
	return &Reporter{wrapper: New("")}
}

// AddErr adds an error to the error stack.
// if "err" is a StackError then
// each of these errors will be printed as individual.
//
// Returns true if this "err" is not nil and it's added to the reporter's stack.
func (r *Reporter) AddErr(err error) bool {
	if err == nil {
		return false
	}

	// 如果是StackError，这里的实现类是Reporter
	if stackErr, ok := err.(StackError); ok {
		// 这里是把err中的子error全部加载进来
		r.addStack(stackErr.Stack())
	} else {
		r.mu.Lock()
		r.wrapper = r.wrapper.AppendErr(err)
		r.mu.Unlock()
	}

	return true
}

// Add adds a formatted message as an error to the error stack.
//
// Returns true if this "err" is not nil and it's added to the reporter's stack.
// 添加错误信息到 error Stack
func (r *Reporter) Add(format string, a ...interface{}) bool {

	if format == "" && len(a) == 0 {
		return false
	}

	//  usually used as:  "module: %v", err so
	// check if the first argument is error and if that error is empty then don't add it.
	// 这里的逻辑是检查 %v 的情况，而且也只检查第一个
	if len(a) > 0 {
		f := a[0]
		// 这个写法比较巧妙
		if e, ok := f.(interface {
			Error() string
		}); ok {
			if e.Error() == "" {
				return false
			}
		}
	}

	r.mu.Lock()
	r.wrapper = r.wrapper.Append(format, a...)
	r.mu.Unlock()
	return true
}

// Describe same as `Add` but if "err" is nil then it does nothing.
func (r *Reporter) Describe(format string, err error) {
	if err == nil {
		return
	}
	if stackErr, ok := err.(StackError); ok {
		r.addStack(stackErr.Stack())
		return
	}

	r.Add(format, err)
}

// PrintStack prints all the errors to the given "printer".
// Returns itself in order to be used as printer and return the full error in the same time.
// PrintStack 会通过 printer参数 打印出所有的错误
func (r *Reporter) PrintStack(printer func(string, ...interface{})) error {
	return PrintAndReturnErrors(r, printer)
}

// Stack returns the list of the errors in the stack.
// 返回其封装的wrapper（即iris 封装的Error）的Stack，
func (r *Reporter) Stack() []Error {
	return r.wrapper.Stack
}

func (r *Reporter) addStack(stack []Error) {
	for _, e := range stack {
		// 要过滤下 .Error() 为"" 的错误
		if e.Error() == "" {
			continue
		}
		// 由于很多地方要修改这个变量，所以需要加锁
		r.mu.Lock()
		r.wrapper = r.wrapper.AppendErr(e)
		r.mu.Unlock()
	}
}

// Error implements the error, returns the full error string.
// 返回当前Reporter中的msg信息
func (r *Reporter) Error() string {
	return r.wrapper.Error()
}

// Return returns nil if the error is empty, otherwise returns the full error.
// 这个作用是判断当前Reporter是否没有错误，即为nil
func (r *Reporter) Return() error {
	if r.Error() == "" {
		return nil
	}

	return r
}
