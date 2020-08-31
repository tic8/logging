package logging

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GetGinTraceIDFromHeader 从 gin 的 request header 中获取 key 为 TraceIDKeyname 的值作为 traceid
func GetGinTraceIDFromHeader(c *gin.Context) string {
	return c.Request.Header.Get(string(TraceIDKeyname))
}

// GetGinTraceIDFromQueryString 从 gin 的 querystring 中获取 key 为 TraceIDKeyname 的值作为 traceid
func GetGinTraceIDFromQueryString(c *gin.Context) string {
	return c.Query(string(TraceIDKeyname))
}

// GetGinTraceIDFromPostForm 从 gin 的 postform 中获取 key 为 TraceIDKeyname 的值作为 traceid
func GetGinTraceIDFromPostForm(c *gin.Context) string {
	return c.PostForm(string(TraceIDKeyname))
}

// GinLogMsg gin 日志中间件记录的信息
type GinLogMsg struct {
	// 日志打印时间
	Timestamp time.Time `json:"timestamp"`
	// 请求方法
	Method string `json:"method"`
	// 请求 Path
	Path string `json:"path"`
	// 请求 RawQuery
	Query string `json:"query"`
	// http 协议版本
	Proto string `json:"proto"`
	// 请求内容长度
	ContentLength int `json:"content_length"`
	// 请求的 host host:port
	Host string `json:"host"`
	// 请求 remote addr  host:port
	RemoteAddr string `json:"remote_addr"`
	// uri
	RequestURI string `json:"request_uri"`
	// referer
	Referer string `json:"referer"`
	// user agent
	UserAgent string `json:"user_agent"`
	// 真实客户端 ip
	ClientIP string `json:"client_ip"`
	// content type
	ContentType string `json:"content_type"`
	// handler name
	HandlerName string `json:"handler_name"`
	// http 状态码
	StatusCode int `json:"status_code"`
	// 响应 body 字节数
	BodySize int `json:"body_size"`
	// 请求处理耗时 (秒)
	Latency float64 `json:"latency"`
	// Context 中的 Keys
	ContextKeys map[string]interface{} `json:"context_keys,omitempty"`
	// RequestBody 请求 body
	RequestBody string `json:"request_body,omitempty"`
	// ResponseBody 响应 Body
	ResponseBody string `json:"response_body,omitempty"`
}

// GinLoggerConfig GinLogger 支持的配置项字段定义
type GinLoggerConfig struct {
	// Optional. Default value is logging.defaultGinLogFormatter
	Formatter func(GinLogMsg) string
	// SkipPaths is a url path array which logs are not written.
	// Optional.
	SkipPaths []string
	// DisableDetails 是否关闭输出 details 字段信息
	// Optional.
	DisableDetails bool
	// DetailsWithContextKeys 打印 details 时，是否实例 context keys ，只在 DisableDetails 为 false 时 生效
	DetailsWithContextKeys bool
	// DetailsWithBody 打印 details 时，是否记录请求 body 和 响应 body ，只在 DisableDetails 为 false 时生效
	// 开启后对性能影响严重，适用于接口调试，慎用。
	// Optional.
	DetailsWithBody bool
	// TraceIDFunc 获取或生成 trace id 的函数
	// Optional.
	TraceIDFunc func(*gin.Context) string
}

// GinLogger 以默认配置生成 gin 的 Logger 中间件
func GinLogger() gin.HandlerFunc {
	return GinLoggerWithConfig(GinLoggerConfig{})
}

// gin 访问日志中 msg 字段的输出格式
func defaultGinLogFormatter(m GinLogMsg) string {
	_, shortHandlerName := path.Split(m.HandlerName)
	msg := fmt.Sprintf("%v|%s|%s|%s%s|%s|%d|%f",
		m.Timestamp.Format("2006-01-02 15:04:05.999999999"),
		m.ClientIP,
		m.Method,
		m.Host,
		m.RequestURI,
		shortHandlerName,
		m.StatusCode,
		m.Latency,
	)
	return msg
}

func defaultGinTraceIDFunc(c *gin.Context) (traceID string) {
	traceID = GetGinTraceIDFromHeader(c)
	if traceID != "" {
		return
	}
	traceID = GetGinTraceIDFromPostForm(c)
	if traceID != "" {
		return
	}
	traceID = GetGinTraceIDFromQueryString(c)
	if traceID != "" {
		return
	}
	traceID = CtxTraceID(c)
	return
}

// GinLoggerWithConfig 根据配置信息生成 gin 的 Logger 中间件
// 中间件会记录访问信息，根据状态码确定日志级别， 500 以上为 Error ， 400-500 默认为 Warn ， 400 以下默认为 Info
// api 请求进来的 context 的函数无需在其中打印 err ，使用 c.Error(err)会在请求完成时自动打印 error
// context 中有 error 则日志忽略返回码始终使用 error 级别
func GinLoggerWithConfig(conf GinLoggerConfig) gin.HandlerFunc {
	formatter := conf.Formatter
	if formatter == nil {
		formatter = defaultGinLogFormatter
	}
	getTraceID := conf.TraceIDFunc
	if getTraceID == nil {
		getTraceID = defaultGinTraceIDFunc
	}

	var skip map[string]struct{}
	if length := len(conf.SkipPaths); length > 0 {
		skip = make(map[string]struct{}, length)
		for _, path := range conf.SkipPaths {
			skip[path] = struct{}{}
		}
	}
	return func(c *gin.Context) {
		traceID := getTraceID(c)
		// 设置 trace id 到 request header 中
		c.Request.Header.Set(string(TraceIDKeyname), traceID)
		// 设置 trace id 到 response header 中
		c.Writer.Header().Set(string(TraceIDKeyname), traceID)
		// 设置 trace id 和 ctxLogger 到 context 中
		Context(c, CloneLogger("gin"), traceID)

		start := time.Now()

		// 获取请求信息
		msg := GinLogMsg{
			Method:        c.Request.Method,
			Path:          c.Request.URL.Path,
			Query:         c.Request.URL.RawQuery,
			Proto:         c.Request.Proto,
			ContentLength: int(c.Request.ContentLength),
			Host:          c.Request.Host,
			RemoteAddr:    c.Request.RemoteAddr,
			RequestURI:    c.Request.RequestURI,
			Referer:       c.Request.Referer(),
			UserAgent:     c.Request.UserAgent(),

			ClientIP:    c.ClientIP(),
			ContentType: c.ContentType(),
			HandlerName: c.HandlerName(),
		}

		// 开启记录响应 body 时，保存 body 到 rbw.body 中
		rbw := &responseBodyWriter{body: bytes.NewBufferString(""), ResponseWriter: c.Writer}
		if !conf.DisableDetails && conf.DetailsWithBody {
			// 获取并保存请求 body
			msg.RequestBody = string(GetRequestBody(c))
			c.Writer = rbw
		}

		c.Next()

		if _, exists := skip[msg.Path]; !exists {
			// 获取响应信息
			msg.StatusCode = c.Writer.Status()
			msg.BodySize = c.Writer.Size()
			msg.Timestamp = time.Now()
			msg.Latency = msg.Timestamp.Sub(start).Seconds()

			// 判断是否打印 context keys
			if !conf.DisableDetails && conf.DetailsWithContextKeys {
				msg.ContextKeys = c.Keys
			}
			// 获取并保存响应 body
			if !conf.DisableDetails && conf.DetailsWithBody {
				msg.ResponseBody = rbw.body.String()
			}

			// msg 设置完毕 创建 logger 进行打印
			accessLogger := CtxLogger(c).Named("access_logger")
			// handler 中使用 c.Error(err) 后，会打印到 context_errors 字段中
			if len(c.Errors) > 0 {
				accessLogger = accessLogger.With(zap.String("context_errors", c.Errors.String()))
			}

			// details logger 打印 details msg 字段
			detailsLogger := accessLogger.Named("details").With(zap.Any("details", msg))

			logger := detailsLogger
			// 是否不打印 details 字段
			if conf.DisableDetails {
				logger = accessLogger
			}

			// 打印访问日志，根据状态码确定日志打印级别
			log := logger.Info
			if msg.StatusCode >= http.StatusInternalServerError {
				// 500+ 始终打印带 details 的 error 级别日志，并附带请求信息
				requestDumps, _ := httputil.DumpRequest(c.Request, true)
				log = detailsLogger.With(zap.String("request", string(requestDumps))).Error
			} else if msg.StatusCode >= http.StatusBadRequest {
				// 400+ 默认使用 warn 级别。如果有 errors 则使用 error 级别
				log = logger.Warn
				if len(c.Errors) > 0 {
					log = logger.Error
				}
			} else if len(c.Errors) > 0 {
				log = logger.Error
			}
			log(formatter(msg))
		}
	}
}

// GetRequestBody 获取请求 body
func GetRequestBody(c *gin.Context) []byte {
	// 获取请求 body
	var requestBody []byte
	if c.Request.Body != nil {
		body, err := ioutil.ReadAll(c.Request.Body)
		if err != nil {
			c.Error(err)
		} else {
			requestBody = body
			// body 被 read 、 bind 之后会被置空，需要重置
			c.Request.Body = ioutil.NopCloser(bytes.NewBuffer(body))
		}
	}
	return requestBody
}

// 用于记录响应 body
type responseBodyWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

// 覆盖 ResponseWriter 接口的 Write 方法，将 body 保存到 responseBodyWriter.body 中
func (w responseBodyWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// GinRecovery gin recovery 中间件
// save err in context and abort 500 with do errhandler
func GinRecovery(errHandler ...func(*gin.Context, ...interface{})) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Check for a broken connection, as it is not really a
				// condition that warrants a panic stack trace.
				var brokenPipe bool
				if ne, ok := err.(*net.OpError); ok {
					if se, ok := ne.Err.(*os.SyscallError); ok {
						if strings.Contains(strings.ToLower(se.Error()), "broken pipe") || strings.Contains(strings.ToLower(se.Error()), "connection reset by peer") {
							brokenPipe = true
						}
					}
				}
				if brokenPipe {
					// save err in context
					c.Error(errors.New(fmt.Sprint("Broken pipe:", err, "\n", string(debug.Stack()))))
					if len(errHandler) > 0 {
						errHandler[0](c)
					}
					c.AbortWithStatus(http.StatusInternalServerError)
					return
				}

				// save err in context
				c.Error(errors.New(fmt.Sprint("Recovery from panic:", err, "\n", string(debug.Stack()))))
				if len(errHandler) > 0 {
					errHandler[0](c)
				}
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
		}()
		c.Next()
	}
}
