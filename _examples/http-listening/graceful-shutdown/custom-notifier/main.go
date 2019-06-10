package main

import (
	stdContext "context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kataras/iris"
)

func main() {
	app := iris.New()

	app.Get("/", func(ctx iris.Context) {
		ctx.HTML("<h1>hi, I just exist in order to see if the server is closed</h1>")
	})

	go func() {
		ch := make(chan os.Signal, 1)
		// 这里的Notify就是针对系统的信号量进行处理，如果后面没有参数的话
		// 则表示全部的信号量(具体可以看signal.go的源码)
		signal.Notify(ch,
			// kill -SIGINT XXXX or Ctrl+c
			os.Interrupt,
			syscall.SIGINT, // register that too, it should be ok
			// os.Kill  is equivalent with the syscall.Kill
			os.Kill,
			syscall.SIGKILL, // register that too, it should be ok
			// kill -SIGTERM XXXX
			syscall.SIGTERM,
		)
		select {
		// 在signal.go中的process()方法中将sign导入ch中
		//是通过协程 死循环来处理信号的
		case <-ch:
			println("shutdown...")

			timeout := 5 * time.Second
			ctx, cancel := stdContext.WithTimeout(stdContext.Background(), timeout)
			defer cancel()
			app.Shutdown(ctx)
		}
	}()

	// Start the server and disable the default interrupt handler in order to
	// handle it clear and simple by our own, without any issues.
	app.Run(iris.Addr(":8080"), iris.WithoutInterruptHandler)
}
