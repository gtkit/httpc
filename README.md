# httpc

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://go.dev)

生产级 Go HTTP JSON 客户端，基于 [gtkit/json](https://github.com/gtkit/json) 实现可切换的 JSON 后端。

## 安装

```bash
go get github.com/gtkit/httpc
```

## 特性

- **全 HTTP 方法**: GET / POST / PUT / PATCH / DELETE / HEAD / OPTIONS + 通用 `RequestJSON`
- **BaseURL 与默认 Header**: `WithBaseURL` 调用处只传路径；`WithDefaultHeaders` 公共头设一次，单次调用可覆盖
- **响应 Header 透出**: JSON 与 Raw 方法均提供 `*WithHeader` 变体，便于读取 `X-Request-Id`/`ETag` 等
- **响应体限流**: 默认上限 10 MiB（`WithMaxResponseBytes` 可调），防止超大/恶意响应打爆内存
- **JSON 编解码**: 使用 `github.com/gtkit/json/v2`，构建时可切换 sonic/go-json/jsoniter
- **连接池**: MaxIdleConns=100, MaxIdleConnsPerHost=10（`WithMaxIdleConnsPerHost` 可调）, HTTP/2, KeepAlive
- **代理支持**: 默认读取 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 环境变量
- **安全 Body drain**: 限量排空（≤4 KiB）以复用连接，避免被恶意 body 拖垮
- **Redirect 控制**: 默认跟随 3xx，也可禁用自动跳转或自定义跳转策略
- **无内置日志**: 状态码/Header/错误全部回传，由调用方在业务层记录（错误信息自动屏蔽 URL 中的 userinfo 密码）
- **Context 传播**: 所有方法第一个参数都是 `context.Context`

## 使用

```go
c := httpc.New(
    httpc.WithTimeout(10 * time.Second),
)

// BaseURL + 全局默认 Header：调用处只传路径，公共头只设一次
c = httpc.New(
    httpc.WithBaseURL("https://api.example.com"),
    httpc.WithDefaultHeaders(map[string]string{
        "Authorization": "Bearer xxx",
        "User-Agent":    "my-service/1.0",
    }),
)
status, err := c.GetJSON(ctx, "/v1/users?id=1", nil, &result) // 实际请求 https://api.example.com/v1/users?id=1
// 单次调用传同名 header 可覆盖默认值；URL 自带 scheme 时绕过 BaseURL 直接使用

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
// Raw 路径也有对称变体（返回 http.Header, []byte, status, err）
header, body, status, err = c.GetRawWithHeader(ctx, url, nil)

// 限制响应体大小（默认 10 MiB），超限返回 errors.Is(err, httpc.ErrResponseTooLarge)
c = httpc.New(httpc.WithMaxResponseBytes(5 << 20)) // 5 MiB
// 传 0 关闭限制（仅在完全可信的内网场景使用）
c = httpc.New(httpc.WithMaxResponseBytes(0))

// HEAD
resp, err := c.Head(ctx, url, nil)

// Fire-and-forget（result 传 nil）
c.PostJSON(ctx, url, body, nil)

// 禁止自动跟随 3xx，直接读取 Location / status
c = httpc.New(httpc.WithoutRedirect())
body, status, err := c.GetRaw(ctx, url, nil)

// 主要请求单一上游时，调大每 host 的空闲连接数（默认 10）。
// 该选项会先克隆 transport 再修改，不会改写共享的 transport（代价是独立连接池）
c = httpc.New(httpc.WithMaxIdleConnsPerHost(50))

// 自定义 http.Client：会浅拷贝，后续选项不会改写你传入的 client；
// 反过来，构造之后你再修改自己的 client 也不会影响 httpc。
// 注意 WithHTTPClient 要放在 WithTimeout 等修改型选项之前。
c = httpc.New(httpc.WithHTTPClient(myClient), httpc.WithTimeout(5*time.Second))
```

行为说明：body 传 `nil`（包括存进 `any` 的 typed-nil 指针/map/slice）时不发送请求体、不设置 `Content-Type`，而不是发送 JSON 字面量 `null`。JSON 便捷方法不提供 raw JSON body 入口；如需显式发送原始 `null`，请使用低层 `Do` 自行构造 `*http.Request`。

注意：重试有意不内置 —— 只有调用方知道哪些请求幂等、该用什么退避策略。请求体支持重放（`GetBody` 已设置），调用方可安全地重发。

## 日志（由调用方负责）

httpc **不内置日志**。状态码、Header、错误都通过返回值给到调用方 —— 库内再记一遍只是重复，且缺少业务上下文（trace-id 等）。请在你自己的调用层记录：

```go
status, err := c.GetJSON(ctx, url, headers, &result)
if err != nil {
    log.Errorw("upstream call failed", "url", url, "status", status, "err", err)
    return err
}
```

错误信息已自动屏蔽 URL 中的 userinfo 密码（`user:xxxxx@host`）；但 query 中的 token 不会脱敏 —— **token 请放 header，勿放 query**。

需要连接级追踪（DNS / TLS 握手 / 连接复用）时，给请求 context 挂 [`net/http/httptrace.ClientTrace`](https://pkg.go.dev/net/http/httptrace) 即可，无需库内日志。

## 空 body 与状态码

JSON 便捷方法遇到空（或纯空白）响应体且传了 `result` 时，返回 `httpc.ErrEmptyBody`（用 `errors.Is` 判断），以区分「无内容」与「坏 JSON」。需要自行掌控状态码语义、错误信封或拿 Header 时，用 Raw 变体（不解码）：

```go
header, body, status, err := c.GetRawWithHeader(ctx, url, nil)
if err != nil { return err }            // 仅传输/读 body 错误
if status >= 400 { /* 自行处理错误信封 */ }
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
