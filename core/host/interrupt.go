package host

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// RegisterOnInterrupt registers a global function to call when CTRL+C/CMD+C pressed or a unix kill command received.
// RegisterOnInterrupt是当服务接收到中断消息了后要进行的func()，而且这是全局的
func RegisterOnInterrupt(cb func()) {
	Interrupt.Register(cb)
}

// Interrupt watches the os.Signals for interruption signals
// and fires the callbacks when those happens.
// A call of its `FireNow` manually will fire and reset the registered interrupt handlers.
var Interrupt = new(interruptListener)

type interruptListener struct {
	mu sync.Mutex
	// 只有在listenOnce()使用一次
	// listenOnce()是什么作用?
	// 让有中断函数第一次被注册的时候，就调用(开启一个协程来监听信号)
	once sync.Once

	// onInterrupt contains a list of the functions that should be called when CTRL+C/CMD+C or
	// a unix kill command received.
	// 这里就是当服务被中断后要执行的[]func()
	onInterrupt []func()
}

// Register registers a global function to call when CTRL+C/CMD+C pressed or a unix kill command received.
// 添加到interruptListener.onInterrupt（）中
func (i *interruptListener) Register(cb func()) {
	if cb == nil {
		return
	}

	i.listenOnce()
	i.mu.Lock()
	i.onInterrupt = append(i.onInterrupt, cb)
	i.mu.Unlock()
}

// FireNow can be called more than one times from a Consumer in order to
// execute all interrupt handlers manually.
// 手动调用中断的方法，然后清空onInterrupt
func (i *interruptListener) FireNow() {
	i.mu.Lock()
	for _, f := range i.onInterrupt {
		f()
	}
	i.onInterrupt = i.onInterrupt[0:0]
	i.mu.Unlock()
}

// listenOnce fires a goroutine which calls the interrupt handlers when CTRL+C/CMD+C and e.t.c.
// If `FireNow` called before then it does nothing when interrupt signal received,
// so it's safe to be used side by side with `FireNow`.
//
// Btw this `listenOnce` is called automatically on first register, it's useless for outsiders.
// 这个方法在第一次绑定register时候就会调用,会开启一个协程来处理中断信号
func (i *interruptListener) listenOnce() {
	i.once.Do(func() { go i.notifyAndFire() })
}

// 开启了一个信号接收器，如果有信号来，则将注册的中断之后要执行的函数全部进行执行，然后清空中断方法
// 这个是直接监听系统的信号
func (i *interruptListener) notifyAndFire() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch,
		// kill -SIGINT XXXX or Ctrl+c
		os.Interrupt,
		syscall.SIGINT, // register that too, it should be ok
		// os.Kill  is equivalent with the syscall.SIGKILL
		os.Kill,
		syscall.SIGKILL, // register that too, it should be ok
		// kill -SIGTERM XXXX
		syscall.SIGTERM,
	)
	select {
	case <-ch:
		i.FireNow()
	}
}
