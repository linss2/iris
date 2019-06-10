package context

import (
	"net/http"
	"sync"
)

// Pool is the context pool, it's used inside router and the framework by itself.
//
// It's the only one real implementation inside this package because it used widely.
type Pool struct {
	// 问题:这里是从原生的sync.Pool的作用？
	// 解答:这里可以看pool的作用，可以看pool.go红Acquire的效果（核心部分是通过给与的newFunc使用的），即本质的池功能靠原生的sync.Pool保证
	// todo 看原生的sync.Pool的源码
	pool *sync.Pool

	//池中获取Context的初始化方法
	// todo 问题:后面的这个注释有些不理解?
	newFunc func() Context // we need a field otherwise is not working if we change the return value
}

// New creates and returns a new context pool.
// 这里表示池的初始化的方法
func New(newFunc func() Context) *Pool {
	c := &Pool{pool: &sync.Pool{}, newFunc: newFunc}
	//上面那一行的newFunc表示Pool中的
	//实际原生保证safe的是sync.Pool字段里面的New字段为newFunc，在本文件的Acquire使用
	c.pool.New = func() interface{} { return c.newFunc() }
	return c
}

// Attach changes the pool's return value Context.
//
// The new Context should explicitly define the `Next()`
// and `Do(context.Handlers)` functions.
// 必须重写Next()和Do(context.Handlers)
//
// Example: https://github.com/kataras/iris/blob/master/_examples/routing/custom-context/method-overriding/main.go
// 问题:要理解为啥这样改是可以的?这个问题等待context差不多了再回来看？
// 解答:可以返回自己想要的Context结构
func (c *Pool) Attach(newFunc func() Context) {
	c.newFunc = newFunc
}

// Acquire returns a Context from pool.
// See Release.
// 这里从原生的sync.Pool总获取参数，然后调用beginRequest来进行数据的清理和赋值
func (c *Pool) Acquire(w http.ResponseWriter, r *http.Request) Context {
	ctx := c.pool.Get().(Context)
	ctx.BeginRequest(w, r)
	return ctx
}

// Release puts a Context back to its pull, this function releases its resources.
// See Acquire.
func (c *Pool) Release(ctx Context) {
	ctx.EndRequest()
	c.pool.Put(ctx)
}

// ReleaseLight will just release the object back to the pool, but the
// clean method is caller's responsibility now, currently this is only used
// on `SPABuilder`.
func (c *Pool) ReleaseLight(ctx Context) {
	c.pool.Put(ctx)
}
