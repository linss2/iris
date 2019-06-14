package errors

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/iris-contrib/go.uuid"
)

var (
	// Prefix the error prefix, applies to each error's message.
	Prefix = ""
)

// Error holds the error message, this message never really changes
type Error struct {
	// ID returns the unique id of the error, it's needed
	// when we want to check if a specific error returned
	// but the `Error() string` value is not the same because the error may be dynamic
	// by a `Format` call.
	// 这个ID表示的是这个 错误的唯一值，当我们去检查一个指定的错误，而错误信息是动态修改的情况下，非常有用，
	// 即我们可以通过id知道是哪个问题
	// todo 这个还需要去检验下？？？
	ID string `json:"id"`
	// The message of the error.
	Message string `json:"message"`
	// Apennded is true whenever it's a child error.
	// todo 子错误可以appended？？？？不理解
	Appended bool `json:"appended"`
	// Stack returns the list of the errors that are shown at `Error() string`.
	Stack []Error `json:"stack"` // filled on AppendX.
}

// New creates and returns an Error with a pre-defined user output message
// all methods below that doesn't accept a pointer receiver because actually they are not changing the original message
func New(errMsg string) Error {
	// 通过 uuid.NewV4 来生成唯一的id
	// todo 阅读 uuid.NewV4 的问题
	uidv4, _ := uuid.NewV4() // skip error.
	return Error{
		ID:      uidv4.String(),
		Message: Prefix + errMsg,
	}
}

// NewFromErr same as `New` but pointer for nil checks without the need of the `Return()` function.
// 将系统的error 封装成 iris中的error
func NewFromErr(err error) *Error {
	if err == nil {
		return nil
	}

	errp := New(err.Error())
	return &errp
}

// Equal returns true if "e" and "to" are matched, by their IDs if it's a core/errors type otherwise it tries to match their error messages.
// It will always returns true if the "to" is a children of "e"
// or the error messages are exactly the same, otherwise false.
// 判断只要两个error Id是一样，或者是其 error()返回的字符串是一样（一般是错误信息），则相同
func (e Error) Equal(to error) bool {
	if e2, ok := to.(Error); ok {
		return e.ID == e2.ID
	} else if e2, ok := to.(*Error); ok {
		return e.ID == e2.ID
	}

	return e.Error() == to.Error()
}

// Empty returns true if the "e" Error has no message on its stack.
func (e Error) Empty() bool {
	return e.Message == ""
}

// NotEmpty returns true if the "e" Error has got a non-empty message on its stack.
func (e Error) NotEmpty() bool {
	return !e.Empty()
}

// String returns the error message
func (e Error) String() string {
	return e.Message
}

// Error returns the message of the actual error
// implements the error
func (e Error) Error() string {
	return e.String()
}

// Format returns a formatted new error based on the arguments
// it does NOT change the original error's message
func (e Error) Format(a ...interface{}) Error {
	e.Message = fmt.Sprintf(e.Message, a...)
	return e
}

// 无视 message 最后的换行
func omitNewLine(message string) string {
	if strings.HasSuffix(message, "\n") {
		return message[0 : len(message)-2]
	} else if strings.HasSuffix(message, "\\n") {
		return message[0 : len(message)-3]
	}
	return message
}

// AppendInline appends an error to the stack.
// It doesn't try to append a new line if needed.
// 表示将错误信息写在一个 Error 信息中，并将其添加到当前的Stack中
func (e Error) AppendInline(format string, a ...interface{}) Error {
	msg := fmt.Sprintf(format, a...)
	e.Message += msg
	e.Appended = true
	e.Stack = append(e.Stack, New(omitNewLine(msg)))
	return e
}

// Append adds a message to the predefined error message and returns a new error
// it does NOT change the original error's message
// 这里已经将对应的 Message 换行了 ，这样的写法的好处是最后一行少了一个换行
func (e Error) Append(format string, a ...interface{}) Error {
	// if new line is false then append this error but first
	// we need to add a new line to the first, if it was true then it has the newline already.
	if e.Message != "" {
		e.Message += "\n"
	}

	return e.AppendInline(format, a...)
}

// AppendErr adds an error's message to the predefined error message and returns a new error.
// it does NOT change the original error's message
func (e Error) AppendErr(err error) Error {
	return e.Append(err.Error())
}

// HasStack returns true if the Error instance is created using Append/AppendInline/AppendErr funcs.
func (e Error) HasStack() bool {
	return len(e.Stack) > 0
}

// With does the same thing as Format but it receives an error type which if it's nil it returns a nil error.
// 此时 Error 中的 message 是格式字符串，其可以用err.Error()
func (e Error) With(err error) error {
	if err == nil {
		return nil
	}

	return e.Format(err.Error())
}

// Ignore will ignore the "err" and return nil.
// todo 这个不知道怎么用，也没有地方使用？？
func (e Error) Ignore(err error) error {
	if err == nil {
		return e
	}
	if e.Error() == err.Error() {
		return nil
	}
	return e
}

// Panic output the message and after panics.
func (e Error) Panic() {
	// todo 阅读 runtime.Caller() 里的源码
	_, fn, line, _ := runtime.Caller(1)
	// 这里只显示当前Error 中的Message
	errMsg := e.Message
	// todo panic 后面还会加上 Caller was？？？了解下
	errMsg += "\nCaller was: " + fmt.Sprintf("%s:%d", fn, line)
	panic(errMsg)
}

// Panicf output the formatted message and after panics.
func (e Error) Panicf(args ...interface{}) {
	_, fn, line, _ := runtime.Caller(1)
	errMsg := e.Format(args...).Error()
	errMsg += "\nCaller was: " + fmt.Sprintf("%s:%d", fn, line)
	panic(errMsg)
}
