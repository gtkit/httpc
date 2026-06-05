# httpc

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://go.dev)

生产级 Go HTTP JSON 客户端，基于 [gtkit/json](https://github.com/gtkit/json) 实现可切换的 JSON 后端。

## 安装

```bash
go get github.com/gtkit/httpc
```

## 特性

- **全 HTTP 方法**: GET / POST / PUT / PATCH / DELETE / HEAD / OPTIONS + 通用 `RequestJSON`
- **响应 Header 透出**: `*WithHeader` 变体返回 `(http.Header, int, error)`，便于读取 `X-Request-Id`/`ETag` 等
- **JSON 编解码**: 使用 `github.com/gtkit/json/v2`，构建时可切换 sonic/go-json/jsoniter
- **连接池**: MaxIdleConns=100, MaxIdleConnsPerHost=10, HTTP/2, KeepAlive
- **Body drain**: 自动排空响应体确保连接复用
- **Redirect 控制**: 默认跟随 3xx，也可禁用自动跳转或自定义跳转策略
- **结构化日志**: 每次请求/响应/错误都会通过 `Logger` 接口记录
- **Context 传播**: 所有方法第一个参数都是 `context.Context`

## 使用

```go
c := httpc.New(
    httpc.WithTimeout(10*time.Second),
    httpc.WithLogger(myZapAdapter),
)

// JSON POST
var result MyResponse
status, err := c.PostJSON(ctx, url, reqBody, &result)

// JSON GET with Bearer token
status, err := c.GetJSON(ctx, url, map[string]string{
    "Authorization": "Bearer xxx",
}, &result)

// PUT / PATCH / DELETE
c.PutJSON(ctx, url, body, &result)
c.PatchJSON(ctx, url, body, &result)
c.DeleteJSON(ctx, url, body, &result)

// Raw response (多次 unmarshal)
body, status, err := c.GetRaw(ctx, url, headers)

// 通用方法（自定义 method + headers + body）
c.RequestJSON(ctx, "OPTIONS", url, headers, body, &result)

// 需要响应 Header 时，使用 *WithHeader 变体（返回 http.Header, status, err）
header, status, err := c.GetJSONWithHeader(ctx, url, nil, &result)
reqID := header.Get("X-Request-Id")
// 同样提供 Post/Put/Patch/Delete 及通用 RequestJSONWithHeader
header, status, err = c.RequestJSONWithHeader(ctx, "GET", url, headers, nil, &result)

// HEAD
resp, err := c.Head(ctx, url, nil)

// Fire-and-forget（result 传 nil）
c.PostJSON(ctx, url, body, nil)

// 禁止自动跟随 3xx，直接读取 Location / status
c = httpc.New(httpc.WithoutRedirect())
body, status, err := c.GetRaw(ctx, url, nil)
```

## Logger 集成

```go
type zapLogger struct{ l *zap.SugaredLogger }
func (z *zapLogger) Debug(msg string, kv ...any) { z.l.Debugw(msg, kv...) }
func (z *zapLogger) Info(msg string, kv ...any)  { z.l.Infow(msg, kv...)  }
func (z *zapLogger) Warn(msg string, kv ...any)  { z.l.Warnw(msg, kv...)  }
func (z *zapLogger) Error(msg string, kv ...any) { z.l.Errorw(msg, kv...) }

c := httpc.New(httpc.WithLogger(&zapLogger{l: zap.S()}))
```

日志输出示例：
```
DEBUG httpc: request  method=POST url=https://api.example.com/token
INFO  httpc: response method=POST url=https://api.example.com/token status=200
```

## JSON 后端切换

构建时指定 tag 即可切换 JSON 引擎（由 gtkit/json 提供）：

```bash
go build -tags=sonic ./...    # 使用 sonic (最快)
go build -tags=go_json ./...  # 使用 go-json
go build -tags=jsoniter ./... # 使用 jsoniter
go build ./...                # 标准库
```

## License

MIT
