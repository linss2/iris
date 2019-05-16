package host

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/kataras/iris/core/errors"
	"github.com/kataras/iris/core/netutil"
)

// Configurator provides an easy way to modify
// the Supervisor.
//
// Look the `Configure` func for more.
type Configurator func(su *Supervisor)

// Supervisor is the wrapper and the manager for a compatible server
// and it's relative actions, called Tasks.
//
// Interfaces are separated to return relative functionality(实用) to them.
// supervisor封装和管理 共用的服务(从Server *http.Server看出)
//
type Supervisor struct {
	Server *http.Server

	//表示是否是手动关闭的，如果不是值则为非0
	closedManually int32 // future use, accessed atomically (non-zero means we've called the Shutdown)

	//tls:安全传输层
	//用来觉得在服务情动前，是否输出到控制台上
	manuallyTLS bool // we need that in order to determinate what to output on the console before the server begin.

	// 如果值不为0表示应该等待解锁
	shouldWait  int32 // non-zero means that the host should wait for unblocking
	unblockChan chan struct{}

	mu sync.Mutex

	// 这个暂时不知道什么作用(预计是Server真正调用走这里)?
	// 看204行，不过是通过TaskHost来实现的
	onServe []func(TaskHost)

	// IgnoreErrors should contains the errors that should be ignored
	// on both serve functions return statements and error handlers.
	//
	// i.e: http.ErrServerClosed.Error().
	//
	// Note that this will match the string value instead of the equality of the type's variables.
	//
	// Defaults to empty.
	// 表示在返回状态码或者error handler 应该忽视的一些错误
	IgnoredErrors []string

	//表示对error所要进行的处理
	onErr      []func(error)
	onShutdown []func()
}

// New returns a new host supervisor
// based on a native net/http "srv".
//
// It contains all native net/http's Server methods.
// Plus you can add tasks on specific events.
// It has its own flow, which means that you can prevent
// to return and exit and restore the flow too.
// 这里就是封装一个原生的 net/http中的server，然后可以对原生的请求进行其他的状态流的处理
func New(srv *http.Server) *Supervisor {
	return &Supervisor{
		Server: srv,
		// 这里unblockChan是什么作用？
		// 这是用来判断当前这个supervisor是否已经不阻塞(不理解为啥不直接使用atomic.StoreInt32())
		unblockChan: make(chan struct{}, 1),
	}
}

// Configure accepts one or more `Configurator`.
// With this function you can use simple functions
// that are spread across your app to modify
// the supervisor, these Configurators can be
// used on any Supervisor instance.
//
// Look `Configurator` too.
//
// Returns itself.
// 这里表示对当前的Supervisor进行更多的处理,可以理解为路由的beginHandler
func (su *Supervisor) Configure(configurators ...Configurator) *Supervisor {
	for _, conf := range configurators {
		conf(su)
	}
	return su
}

// DeferFlow defers the flow of the exeuction,
// i.e: when server should return error and exit
// from app, a DeferFlow call inside a Task
// can wait for a `RestoreFlow` to exit or not exit if
// host's server is "fixed".
//
// See `RestoreFlow` too.
//DeferFlow 定义了当前的执行流，如果服务出现问题，其中一个任务就会调用DeferFlow
// 可以等待RestoreFlow来退出，或者服务自己被修复
// 通过su.shouldWait地址以及atomic.StoreInt32来保证
// 这个暂时也只有测试用例调用
func (su *Supervisor) DeferFlow() {
	atomic.StoreInt32(&su.shouldWait, 1)
}

// RestoreFlow restores the flow of the execution,
// if called without a `DeferFlow` call before
// then it does nothing.
// See tests to understand how that can be useful on specific cases.
//
// See `DeferFlow` too.
// 解除suprevisor的等待情况
func (su *Supervisor) RestoreFlow() {
	if su.isWaiting() {
		//如果supervisor是等待中，则解除shouldWait的情况，然后将unblockChan填充一个空结构表示supervisor为阻塞情况
		atomic.StoreInt32(&su.shouldWait, 0)
		su.mu.Lock()
		su.unblockChan <- struct{}{}
		su.mu.Unlock()
	}
}

//判断supervisor是否再等待，通过atomic.LoadInt32获取su.shouldWait的地址所存的值
func (su *Supervisor) isWaiting() bool {
	return atomic.LoadInt32(&su.shouldWait) != 0
}

func (su *Supervisor) newListener() (net.Listener, error) {
	// this will not work on "unix" as network
	// because UNIX doesn't supports the kind of
	// restarts we may want for the server.
	//
	// User still be able to call .Serve instead.
	// 这里表示服务中真实的调用某个服务的地址,返回net.Listener
	// todo 学习netutil.TCPKeepAlive是怎么执行的
	l, err := netutil.TCPKeepAlive(su.Server.Addr)
	if err != nil {
		return nil, err
	}

	// here we can check for sure, without the need of the supervisor's `manuallyTLS` field.
	// 判断这个服务是否是传输层协议
	// 判断这个服务是否要安全认证
	if netutil.IsTLS(su.Server) {
		// means tls
		//如果有，则要生成tls以及服务所需要的证书信息来生成新的net.Listener
		tlsl := tls.NewListener(l, su.Server.TLSConfig)
		return tlsl, nil
	}

	return l, nil
}

// RegisterOnError registers a function to call when errors occurred by the underline http server.
// 这里就是注册当error出现的时候(排除ignoreError设置的errors)，所要执行的func
func (su *Supervisor) RegisterOnError(cb func(error)) {
	su.mu.Lock()
	su.onErr = append(su.onErr, cb)
	su.mu.Unlock()
}

// 判断这个error是否已经存在IgnoerdErrors中
func (su *Supervisor) validateErr(err error) error {
	if err == nil {
		return nil
	}

	su.mu.Lock()
	defer su.mu.Unlock()

	for _, e := range su.IgnoredErrors {
		if err.Error() == e {
			return nil
		}
	}
	return err
}

// 这里指定一个error，如果不存在IgnoredErrors中，则通过 []func(error) 用协程调用
func (su *Supervisor) notifyErr(err error) {
	err = su.validateErr(err)
	if err != nil {
		su.mu.Lock()
		for _, f := range su.onErr {
			go f(err)
		}
		su.mu.Unlock()
	}
}

// RegisterOnServe registers a function to call on
// Serve/ListenAndServe/ListenAndServeTLS/ListenAndServeAutoTLS.
// 这个在iris.go中Application.NewHost()中一个分支上调用，上面的注释没有调用
func (su *Supervisor) RegisterOnServe(cb func(TaskHost)) {
	// 注意这里添加都是同步锁
	su.mu.Lock()
	su.onServe = append(su.onServe, cb)
	su.mu.Unlock()
}

// 通过同步锁
func (su *Supervisor) notifyServe(host TaskHost) {
	su.mu.Lock()
	// onServe表示这个TaskHost所有要经过的方法
	for _, f := range su.onServe {
		go f(host)
	}
	su.mu.Unlock()
}

// Remove all channels, do it with events
// or with channels but with a different channel on each task proc
// I don't know channels are not so safe, when go func and race risk..
// so better with callbacks....
// 想移除所有的channel，不过不同的task 进程有着不同的channel，不知道channel是否安全，所以用这个方式
// 可以说这个方法其实套了一层在blockFunc这个核心方法中(代理模式)
func (su *Supervisor) supervise(blockFunc func() error) error {
	// 这里生成了一个TaskHost
	host := createTaskHost(su)

	su.notifyServe(host)
	// 这里通过回调来判断是否原生的http.Server是否执行完成
	// blockFunc有两种，一个是su.Server.ListenAndServeTLS("", "")，一个是su.Server.Serve(l)
	// 真实的服务启动在blockFunc()，那上面拿supervisor创建taskHost是什么用意?
	// 是为了执行supervisor 中的 OnServe[]func(TaskHost)
	err := blockFunc()

	// 这里进行对要展示错误的处理
	su.notifyErr(err)

	// todo 啥时候会执行DeferFlow()
	if su.isWaiting() {
		//todo 这里表示如果一直在等待，这里为啥不一直死循环判断，非要用unblockChan的方式？
	blockStatement:
		for {
			select {
			case <-su.unblockChan:
				break blockStatement
			}
		}
	}

	return su.validateErr(err)
}

// Serve accepts incoming connections on the Listener l, creating a
// new service goroutine for each. The service goroutines read requests and
// then call su.server.Handler to reply to them.
// 服务通过监听listener,每一个新的连接创建一个新的服务协程,服务协程读取请求然后通过su.Server.Handler来回应
//
// For HTTP/2 support, server.TLSConfig should be initialized to the
// provided listener's TLS Config before calling Serve. If
// server.TLSConfig is non-nil and doesn't include the string "h2" in
// Config.NextProtos, HTTP/2 support is not enabled.
// 为了支持http/2，server.TLSConfig 应该被在服务被调用前初始化servier.TLSConfig,
// 如果server.TLSConfig是非nil且在Config.NextProtos不包含h2,则不生效
//
// Serve always returns a non-nil error. After Shutdown or Close, the
// returned error is http.ErrServerClosed.
//
//内部其实就是原生的server.Serve()
func (su *Supervisor) Serve(l net.Listener) error {
	return su.supervise(func() error { return su.Server.Serve(l) })
}

// ListenAndServe listens on the TCP network address addr
// and then calls Serve with handler to handle requests
// on incoming connections.
// Accepted connections are configured to enable TCP keep-alives.
// 监听TCP地址链接然后处理请求
func (su *Supervisor) ListenAndServe() error {
	l, err := su.newListener()
	if err != nil {
		return err
	}
	return su.Serve(l)
}

// ListenAndServeTLS acts identically to ListenAndServe, except that it
// expects HTTPS connections. Additionally, files containing a certificate and
// matching private key for the server must be provided. If the certificate
// is signed by a certificate authority, the certFile should be the authority
// of the server's certificate, any intermediates, and the CA's certificate.
// 这个与ListenAndServe类似，只是要求https协议
// 这里二外的需要包含整数以及匹配服务器提供的私钥
func (su *Supervisor) ListenAndServeTLS(certFile string, keyFile string) error {
	// 表示手动实现TLS
	su.manuallyTLS = true

	if certFile != "" && keyFile != "" {
		cfg := new(tls.Config)
		var err error
		cfg.Certificates = make([]tls.Certificate, 1)
		// todo 这里通过LoadX509KeyPair来寻找证书的数据
		if cfg.Certificates[0], err = tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			return err
		}

		// manually inserted as pre-go 1.9 for any case.
		cfg.NextProtos = []string{"h2", "http/1.1"}
		su.Server.TLSConfig = cfg

		// It does nothing more than the su.Server.ListenAndServeTLS anymore.
		// - no hurt if we let it as it is
		// - no problem if we remove it as well
		// but let's comment this as proposed, fewer code is better:
		// return su.ListenAndServe()
	}

	if su.Server.TLSConfig == nil {
		return errors.New("certFile or keyFile missing")
	}

	return su.supervise(func() error { return su.Server.ListenAndServeTLS("", "") })
}

// ListenAndServeAutoTLS acts identically to ListenAndServe, except that it
// expects HTTPS connections. Server's certificates are auto generated(产生) from LETSENCRYPT using
// the golang/x/net/autocert package.
//
// The whitelisted domains are separated by whitespace in "domain" argument, i.e "iris-go.com".
// If empty, all hosts are currently allowed. This is not recommended,
// as it opens a potential(潜在的) attack where clients connect to a server
// by IP address and pretend to be asking for an incorrect host name.
// Manager will attempt to obtain a certificate for that host, incorrectly,
// eventually reaching the CA's rate limit for certificate requests
// and making it impossible to obtain actual certificates.
// domain可以为空，但是不推荐，会有潜在的安全隐患
//
// For an "e-mail" use a non-public one, letsencrypt needs that for your own security.
//
// The "cacheDir" is being, optionally, used to provide cache
// stores and retrieves(取回) previously-obtained certificates.
// If empty, certs will only be cached for the lifetime of the auto tls manager.
// cacheDir是自选的被用来提供缓存或者取回之前被禁止的整数，如果为空，证书只能被缓存到自动tls管理器的生命周期
//
// Note: The domain should be like "iris-go.com www.iris-go.com",
// the e-mail like "kataras2006@hotmail.com" and the cacheDir like "letscache"
// The `ListenAndServeAutoTLS` will start a new server for you,
// which will redirect all http versions to their https, including subdomains as well.
func (su *Supervisor) ListenAndServeAutoTLS(domain string, email string, cacheDir string) error {
	var (
		// todo golang/x/crypto/acme/autocert 这个以后再看
		cache      autocert.Cache
		hostPolicy autocert.HostPolicy
	)

	if cacheDir != "" {
		cache = autocert.DirCache(cacheDir)
	}

	if domain != "" {
		domains := strings.Split(domain, " ")
		hostPolicy = autocert.HostWhitelist(domains...)
	}

	autoTLSManager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: hostPolicy,
		Email:      email,
		Cache:      cache,
		ForceRSA:   true,
	}
	// 本质还是在这里，然后前面通过autoTLSManager.HTTPHandler()来验证https
	srv2 := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		Addr:         ":http",
		Handler:      autoTLSManager.HTTPHandler(nil), // nil for redirect.
	}

	// register a shutdown callback to this
	// supervisor in order to close the "secondary redirect server" as well.
	su.RegisterOnShutdown(func() {
		// give it some time to close itself...
		// 这里再supervsior挺值得时候，给了额外的5秒，让服务自己停止
		timeout := 5 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		srv2.Shutdown(ctx)
	})
	// 说到底，ListenAndServeAutoTLS就是在http前套了一层tls的验证
	go srv2.ListenAndServe()

	su.Server.TLSConfig = &tls.Config{
		MinVersion:               tls.VersionTLS10,
		GetCertificate:           autoTLSManager.GetCertificate,
		PreferServerCipherSuites: true,
		// Keep the defaults.
		CurvePreferences: []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
			tls.CurveP384,
			tls.CurveP521,
		},
	}
	return su.ListenAndServeTLS("", "")
}

// RegisterOnShutdown registers a function to call on Shutdown.
// This can be used to gracefully shutdown connections that have
// undergone NPN/ALPN protocol upgrade or that have been hijacked.
// This function should start protocol-specific graceful shutdown,
// but should not wait for shutdown to complete.
// 注册服务结束后，要执行的方法
func (su *Supervisor) RegisterOnShutdown(cb func()) {
	// when go1.9: replace the following lines with su.Server.RegisterOnShutdown(f)
	su.mu.Lock()
	su.onShutdown = append(su.onShutdown, cb)
	su.mu.Unlock()
}

// 这里就是把supervisor中的每一个onShutdown都执行
func (su *Supervisor) notifyShutdown() {
	// when go1.9: remove the lines below
	su.mu.Lock()
	for _, f := range su.onShutdown {
		go f()
	}
	su.mu.Unlock()
	// end
}

// Shutdown gracefully shuts down the server without interrupting any
// active connections. Shutdown works by first closing all open
// listeners, then closing all idle connections, and then waiting
// indefinitely for connections to return to idle and then shut down.
// If the provided context expires before the shutdown is complete,
// then the context's error is returned.
//
// Shutdown does not attempt to close nor wait for hijacked
// connections such as WebSockets. The caller of Shutdown should
// separately notify such long-lived connections of shutdown and wait
// for them to close, if desired.
//
// 这里的Shutdown没有强制打断各类链接。首先关闭所有的舰艇个，然后关闭所有的空闲链接，
// 然后无限制的等待之前的连接返回然后关闭。如果之前的在关闭完成前超时，也会报出错误
//
// shutdown不会阐释出关闭或等待劫持链接例如WebSocket，只等待那些存活长的链接然后等待去关闭
// todo webSocket了解下
func (su *Supervisor) Shutdown(ctx context.Context) error {
	atomic.AddInt32(&su.closedManually, 1) // future-use
	su.notifyShutdown()
	return su.Server.Shutdown(ctx)
}
