package main

import (
	"crypto/tls"
	"encoding/json"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 配置常量
const (
	// 基础参数
	targetURL     = "https://www.xxx.com"              // 目标服务器地址
	listenAddr    = ":8433"                                  // 代理监听地址
	certFile      = "/etc/ca/tls.crt"           // TLS证书路径
	keyFile       = "/etc/ca/tls.key" // TLS私钥路径
	skipTLSVerify = true                                     // 是否全局跳过 TLS 证书验证

	// 日志与配置路径
	logFile        = "proxy.log"      // 日志文件路径
	routesFilePath = "routes.json"    // 路由配置文件
	reloadInterval = 10 * time.Second // 路由配置重载间隔

	// 缓冲区设置（每线程最大内存分配）
	bufferSize = 64 * 1024 * 1024 // 64MB 缓冲区

	// HTTP客户端连接池设置
	maxIdleConns        = 8 // 最大空闲连接数
	maxIdleConnsPerHost = 8 // 每主机最大空闲连接数
	maxConnsPerHost     = 8 // 每主机最大并发连接数

	// HTTP 服务器超时时间配置
	readTimeout  = 30 * time.Second  // 读取请求超时
	writeTimeout = 600 * time.Second // 响应写入超时，长时间传输（如视频）需设置较长
	idleTimeout  = 120 * time.Second // 空闲连接最大存活时间

	// 网络连接相关超时设置
	dialTimeout           = 30 * time.Second // 拨号超时时间
	dialKeepAlive         = 60 * time.Second // TCP KeepAlive
	idleConnTimeout       = 90 * time.Second // 空闲连接超时
	tlsHandshakeTimeout   = 10 * time.Second // TLS 握手超时
	expectContinueTimeout = 1 * time.Second  // "Expect: 100-continue" 超时
)

// 固定请求头设置
var fixedHeaders = map[string]string{
	"Host":       "www.xxx.com",
	"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
	"Referer":    "https://www.xxx.com/",
}

// 全局变量
var (
	routeMutex sync.RWMutex      // 路由映射表的读写锁
	routes     map[string]string // 路由映射表
	lastMod    time.Time         // 配置文件最后修改时间
)

// bufferPool 实现内存缓冲池
type bufferPool struct {
	pool sync.Pool
}

// newBufferPool 创建指定大小的缓冲池
func newBufferPool(size int) *bufferPool {
	return &bufferPool{
		pool: sync.Pool{
			New: func() interface{} {
				return make([]byte, size)
			},
		},
	}
}

// Get 从缓冲池获取一个缓冲块
func (b *bufferPool) Get() []byte {
	return b.pool.Get().([]byte)
}

// Put 将缓冲块归还到缓冲池
func (b *bufferPool) Put(buf []byte) {
	if cap(buf) >= bufferSize {
		b.pool.Put(buf[:bufferSize])
	}
}

// proxyLogger 自定义日志记录器
type proxyLogger struct {
	fileLogger *log.Logger
	stdLogger  *log.Logger
	file       *os.File
}

// newProxyLogger 创建日志记录器实例
func newProxyLogger(path string) (*proxyLogger, error) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	return &proxyLogger{
		fileLogger: log.New(file, "", log.LstdFlags),
		stdLogger:  log.New(os.Stdout, "", log.LstdFlags),
		file:       file,
	}, nil
}

// Printf 实现日志输出方法(同时输出到文件和控制台)
func (l *proxyLogger) Printf(format string, v ...interface{}) {
	l.stdLogger.Printf(format, v...)
	l.fileLogger.Printf(format, v...)
}

// Close 关闭日志文件
func (l *proxyLogger) Close() error {
	return l.file.Close()
}

// isChunked 检查是否为分块传输编码
func isChunked(encodings []string) bool {
	for _, e := range encodings {
		if e == "chunked" {
			return true
		}
	}
	return false
}

// loadRoutes 加载路由配置文件
func loadRoutes(logger *proxyLogger) error {
	fileInfo, err := os.Stat(routesFilePath)
	if err != nil {
		return err
	}

	// 检查文件是否修改过
	if !fileInfo.ModTime().After(lastMod) {
		return nil
	}

	file, err := ioutil.ReadFile(routesFilePath)
	if err != nil {
		return err
	}

	var config struct {
		Routes map[string]string `json:"routes"`
	}
	if err := json.Unmarshal(file, &config); err != nil {
		return err
	}

	routeMutex.Lock()
	defer routeMutex.Unlock()
	routes = config.Routes
	lastMod = fileInfo.ModTime()

	logger.Printf("路由配置已重新加载，共 %d 条路由", len(routes))
	return nil
}

// startRouteReloader 启动定期重载路由配置的goroutine
func startRouteReloader(logger *proxyLogger) {
	ticker := time.NewTicker(reloadInterval)
	go func() {
		for range ticker.C {
			if err := loadRoutes(logger); err != nil {
				logger.Printf("路由配置重载失败: %v", err)
			}
		}
	}()
}

// findRoute 查找路由映射
// 返回: 路由前缀, 剩余路径, 是否找到
func findRoute(path string) (string, string, bool) {
	// 分割路径，获取前缀和剩余部分
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
	if len(parts) < 1 {
		return "", "", false
	}

	routeMutex.RLock()
	defer routeMutex.RUnlock()

	prefix := parts[0]
	if route, exists := routes[prefix]; exists {
		remainingPath := ""
		if len(parts) > 1 {
			remainingPath = parts[1]
		}
		return route, remainingPath, true
	}
	return "", "", false
}

func main() {
	// 初始化日志系统
	logger, err := newProxyLogger(logFile)
	if err != nil {
		log.Fatalf("无法创建日志文件: %v", err)
	}
	defer logger.Close()

	// 初始加载路由配置
	if err := loadRoutes(logger); err != nil {
		logger.Printf("初始路由配置加载失败: %v", err)
		log.Fatalf("初始路由配置加载失败: %v", err)
	}
	startRouteReloader(logger) // 启动定期重载

	// 解析目标URL
	target, err := url.Parse(targetURL)
	if err != nil {
		logger.Printf("URL解析失败: %v", err)
		log.Fatalf("URL解析失败: %v", err)
	}

	// 创建反向代理实例
	proxy := httputil.NewSingleHostReverseProxy(target)
	bufPool := newBufferPool(bufferSize)

	// 配置传输层参数
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,   // 拨号超时
			KeepAlive: dialKeepAlive, // TCP KeepAlive
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,          // 最大空闲连接数
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,   // 每主机最大空闲连接数
		MaxConnsPerHost:       maxConnsPerHost,       // 每主机最大连接数
		IdleConnTimeout:       idleConnTimeout,       // 空闲连接超时
		TLSHandshakeTimeout:   tlsHandshakeTimeout,   // TLS 握手超时
		ExpectContinueTimeout: expectContinueTimeout, // “Expect: 100-continue” 超时
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: skipTLSVerify,    // 证书验证
			MinVersion:         tls.VersionTLS12, // 最低 TLS 版本
		},
		DisableCompression: true, // 禁用压缩
	}

	// 配置代理请求处理
	proxy.Director = func(req *http.Request) {
		// 查找路由映射
		mappedPath, remainingPath, exists := findRoute(req.URL.Path)
		if !exists {
			req.Header.Set("X-Proxy-Invalid", "1") // 标记此请求为无效路由
			return
		}

		// 修改请求目标
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		// 拼接新路径：映射路径 + 剩余路径
		newPath := filepath.Join("/", mappedPath, remainingPath)
		req.URL.Path = newPath

		// 设置固定请求头
		for k, v := range fixedHeaders {
			req.Header.Set(k, v)
		}

		// 移除不必要的请求头
		req.Header.Del("Accept-Encoding")
		req.Header.Del("If-Modified-Since")

		logger.Printf("请求转发: %s => %s", req.URL.Path, newPath)
	}

	proxy.Transport = transport
	proxy.BufferPool = bufPool

	// 配置响应处理
	proxy.ModifyResponse = func(resp *http.Response) error {
		// 大文件分块传输处理
		contentType := resp.Header.Get("Content-Type")
		isVideo := contentType == "video/mp4" || contentType == "video/webm" || contentType == "application/octet-stream"

		if isVideo || resp.ContentLength >= 1024*1024 {
			if !isChunked(resp.TransferEncoding) {
				resp.Header.Del("Content-Length")
				resp.TransferEncoding = []string{"chunked"}
				logger.Printf("大文件启用分块: %s (类型: %s, 大小: %.2fMB)",
					resp.Request.URL.Path,
					contentType,
					float64(resp.ContentLength)/(1024*1024))
			}
		}

		// 设置CORS头
		resp.Header.Set("Access-Control-Allow-Origin", "*")

		logger.Printf("响应状态: %s %d (%s)",
			resp.Request.Method,
			resp.StatusCode,
			resp.Request.URL.Path)
		return nil
	}

	// 自定义请求处理函数
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 判断 Director 设置的错误标志
		if r.Header.Get("X-Proxy-Invalid") == "1" {
			logger.Printf("[404] %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		// 处理代理请求
		proxy.ServeHTTP(w, r)
		logger.Printf("[%s] %s %s 用时: %v",
			r.Method,
			r.URL.Path,
			r.RemoteAddr,
			time.Since(start))
	})

	// 配置HTTP服务器
	server := &http.Server{
		Addr:    listenAddr,
		Handler: handler,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: skipTLSVerify,
			MinVersion:         tls.VersionTLS12,
		},
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	// 启动信息日志
	logger.Printf("启动代理服务器，已启用路径前缀路由映射功能")
	logger.Printf("监听地址: %s", listenAddr)
	logger.Printf("目标地址: %s", targetURL)
	logger.Printf("缓冲区大小: %dMB", bufferSize/1024/1024)
	logger.Printf("连接池: 全局 %d, 每主机空闲 %d, 最大并发 %d", maxIdleConns, maxIdleConnsPerHost, maxConnsPerHost)
	logger.Printf("路由配置重载间隔: %v", reloadInterval)

	// 启动服务器
	if err := server.ListenAndServeTLS(certFile, keyFile); err != nil {
		logger.Printf("服务器启动失败: %v", err)
		log.Fatalf("服务器启动失败: %v", err)
	}
}
