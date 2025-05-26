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
	targetURL      = "https://www.xxx.com"                    // 目标服务器地址
	listenAddr     = ":4433"                                  // 代理监听端口
	certFile       = "/etc/ca/tls.crt"                        // TLS证书路径
	keyFile        = "/etc/ca/tls.key"                        // TLS密钥路径
	bufferSize     = 64 * 1024 * 1024                         // 缓冲区大小(64MB)
	logFile        = "proxy.log"                              // 日志文件路径
	routesFilePath = "routes.json"                            // 路由配置文件路径
	reloadInterval = 10 * time.Second                         // 配置重载间隔
)

// 固定请求头设置
var fixedHeaders = map[string]string{
	"Host":       "www.xxx.com",
	"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
	"Referer":    "https://www.xxx.com",
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

	logger.Printf("Routing config reloaded. %d routes loaded.", len(routes))
	return nil
}

// startRouteReloader 启动定期重载路由配置的goroutine
func startRouteReloader(logger *proxyLogger) {
	ticker := time.NewTicker(reloadInterval)
	go func() {
		for range ticker.C {
			if err := loadRoutes(logger); err != nil {
				logger.Printf("Failed to reload route config: %v", err)
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
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer logger.Close()

	// 初始加载路由配置
	if err := loadRoutes(logger); err != nil {
		logger.Printf("Initial route config failed: %v", err)
		log.Fatalf("Initial route config failed: %v", err)
	}
	startRouteReloader(logger) // 启动定期重载

	// 解析目标URL
	target, err := url.Parse(targetURL)
	if err != nil {
		logger.Printf("Failed to parse URL: %v", err)
		log.Fatalf("Failed to parse URL: %v", err)
	}

	// 创建反向代理实例
	proxy := httputil.NewSingleHostReverseProxy(target)
	bufPool := newBufferPool(bufferSize)

	// 配置传输层参数
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          500,
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       200,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
		DisableCompression: true,
	}

	// 配置代理请求处理
	proxy.Director = func(req *http.Request) {
		// 查找路由映射
		mappedPath, remainingPath, exists := findRoute(req.URL.Path)
		if !exists {
			return // 不修改请求，后续会返回404
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

		logger.Printf("Forwarding: %s => %s", req.URL.Path, newPath)
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
				logger.Printf("Enable chunked: %s (type: %s, size: %.2fMB)",
					resp.Request.URL.Path,
					contentType,
					float64(resp.ContentLength)/(1024*1024))
			}
		}

		// 设置CORS头
		resp.Header.Set("Access-Control-Allow-Origin", "*")

		logger.Printf("Response: %s %d (%s)",
			resp.Request.Method,
			resp.StatusCode,
			resp.Request.URL.Path)
		return nil
	}

	// 自定义请求处理函数
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 唯一的路由检查点
		if _, _, exists := findRoute(r.URL.Path); !exists {
			logger.Printf("[404] %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		// 处理代理请求
		proxy.ServeHTTP(w, r)
		logger.Printf("[%s] %s %s Duration: %v",
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
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 600 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 启动信息日志
	logger.Printf("Proxy server started with path-based routing")
	logger.Printf("Listening on: %s", listenAddr)
	logger.Printf("Target server: %s", targetURL)
	logger.Printf("Buffer size: %dMB", bufferSize/1024/1024)
	logger.Printf("Connection pool: max %d global, %d per host", 500, 100)
	logger.Printf("Route reload interval: %v", reloadInterval)

	// 启动服务器
	if err := server.ListenAndServeTLS(certFile, keyFile); err != nil {
		logger.Printf("Server failed to start: %v", err)
		log.Fatalf("Server failed to start: %v", err)
	}
}
