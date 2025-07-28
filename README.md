# 高性能Go语言HTTPS视频代理服务

- 本项目实现了一个高性能的 Go HTTPS 反向代理服务器，支持路径映射、TLS 加密、大文件分片转发、跨域控制、日志记录和动态路由加载。适用于视频流转发场景。
- 已经测试: x86, ARM, Risc-Misp 都可以正常运行， 甚至在Mt7621路由器OpenWRT上跑过，转发效率还是很不错的。
---

## ✨ 功能特性

- 🚀 支持 HTTPS 与 TLS 证书配置  
- 🔁 动态路由映射，支持定时热重载
- 🧱 支持固定请求头注入（Host、User-Agent、Referer）  
- 🧠 内置缓冲池，自动内存回收，优化大文件转发性能  
- 📄 控制台与文件日志并行输出  
- ⚡ 优化 HTTP/2 传输与连接复用  
---

## 🗂️ 项目结构

```
 ├── main.go           # 主程序：反向代理实现
 ├── routes.json       # 路由配置：路径映射规则
 ├── proxy.log         # 日志文件：自动生成
 └── README.md         # 当前说明文档
```

------

## ⚙️ 参数配置（`main.go` 中）

| 参数名           | 说明                                   |
| ---------------- | -------------------------------------- |
| `targetURL`      | 目标服务器主地址（默认含域名）         |
| `listenAddr`     | 本地监听地址（默认为 `:4433`）         |
| `certFile`       | TLS 证书路径                           |
| `keyFile`        | TLS 私钥路径（已解密）                 |
| `bufferSize`     | 缓冲区大小（默认 64MB）                |
| `routesFilePath` | 路由配置文件路径（默认 `routes.json`） |
| `reloadInterval` | 路由热重载周期（单位秒）               |
| `logFile`        | 日志保存路径（默认 `proxy.log`）       |



------

## 🧪 快速运行

### 1️⃣ 准备 TLS 证书

将以下证书放置在指定路径或者更改头部源代码：

```
/etc/ca/tls.crt
/etc/ca/tls.key
```

### 2️⃣ 配置路由规则

编辑当前目录下的 `routes.json` 文件，根据实际需求映射路径。

```json
{
  "routes": {
    "api": "backend/api",
    "static": "assets/static",
    "media": "cdn/media"
  }
}
```

表示以下映射：

| 本地路径前缀  | 目标路径追加前缀    | 最终转发到                              |
| ------------- | ------------------- | --------------------------------------- |
| `/api/...`    | `backend/api/...`   | `https://www.xxx.com/backend/api/...`   |
| `/static/...` | `assets/static/...` | `https://www.xxx.com/assets/static/...` |
| `/media/...`  | `cdn/media/...`     | `https://www.xxx.com/cdn/media/...`     |

### 3️⃣缓冲区控制计算

本项目使用缓冲池（`sync.Pool`）与可配置的内存块大小，优化大文件、视频流等大流量请求的处理。

------

#### 📌 默认配置

```go
const bufferSize = 16 * 1024 * 1024 // 16MB
```

表示每次请求会分配一个 **16MB** 的缓冲区来读写流式数据。

------

#### 💡 内存占用估算公式

> **总内存 ≈ bufferSize × 并发请求数**

例如：

| 并发数 | bufferSize | 总内存估算 |
| ------ | ---------- | ---------- |
| 1      | 16MB       | 16MB       |
| 5      | 16MB       | 80MB      |
| 20     | 16MB       | 320GB     |

------

#### ⚙️ 配置建议

- 小型请求或接口代理：可设为 `4MB` ~ `16MB`
- 视频、文件代理：推荐 `32MB` ~ `128MB`
- 内存受限环境：建议监控实际并发数并酌情降低 bufferSize

------

###  启动服务

```
go run main.go
```

或编译二进制文件：

```
go build -o proxy
./proxy
```

------

## 🔍 示例访问

```
https://localhost:4433/api/test
https://localhost:4433/static/js/app.js
https://localhost:4433/media/video.mp4
```

> 如使用自签名证书，请在浏览器或 curl 中忽略安全验证，或添加信任。

------

## 📝 日志说明

- 控制台输出实时请求日志
- 同时将日志写入 `proxy.log` 文件

------

## ⚠️ 生产环境安全提示

- 当前使用了 `InsecureSkipVerify: true` 跳过目标证书验证，仅适用于开发测试环境。
- 在生产环境中请使用合法证书并开启完整验证。

------

## 📄 License

```
MIT License

Copyright (c) 2025 Huatin-Nice

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights  
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell      
copies of the Software, and to permit persons to whom the Software is         
furnished to do so, subject to the following conditions:                       

The above copyright notice and this permission notice shall be included in    
all copies or substantial portions of the Software.                           

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR    
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,      
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE   
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER        
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, 
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN     
THE SOFTWARE.


```

