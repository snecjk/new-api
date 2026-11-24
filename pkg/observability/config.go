// Package observability 管理 Langfuse Cloud 的 OTLP/HTTP 追踪导出：
// 配置加载与校验、Langfuse 属性映射、采样、有界队列/批次导出、
// 请求与尝试生命周期快照、文本捕获上限、故障隔离与限频诊断。
//
// 该包不导入 controller/relay/service， relay 层通过白名单标量与只读
// 快照传入数据，避免包循环依赖。
package observability

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Langfuse 云端追踪配置。全部来自部署环境变量，程序不内置任何密钥默认值。
type Config struct {
	Enabled          bool
	PublicKey        string
	SecretKey        string
	BaseURL          string
	Environment      string
	SampleRate       float64
	CaptureContent   bool
	MaxContentBytes  int
	ExportTimeoutSec int
	FlushTimeoutSec  int
}

// 从环境变量加载配置。启用开关为 false 时不校验密钥与地址，直接返回禁用配置。
func LoadFromEnv() Config {
	cfg := Config{
		Enabled:          getEnvOrDefaultBool("LANGFUSE_ENABLED", false),
		BaseURL:          getEnvOrDefaultString("LANGFUSE_BASE_URL", DefaultBaseURL),
		Environment:      getEnvOrDefaultString("LANGFUSE_ENVIRONMENT", DefaultEnvironment),
		SampleRate:       getEnvOrDefaultFloat("LANGFUSE_SAMPLE_RATE", DefaultSampleRate),
		CaptureContent:   getEnvOrDefaultBool("LANGFUSE_CAPTURE_CONTENT", DefaultCaptureContent),
		MaxContentBytes:  getEnvOrDefaultInt("LANGFUSE_MAX_CONTENT_BYTES", DefaultMaxContentBytes),
		ExportTimeoutSec: getEnvOrDefaultInt("LANGFUSE_EXPORT_TIMEOUT_SECONDS", DefaultExportTimeoutSec),
		FlushTimeoutSec:  getEnvOrDefaultInt("LANGFUSE_FLUSH_TIMEOUT_SECONDS", DefaultFlushTimeoutSec),
	}
	cfg.PublicKey = strings.TrimSpace(os.Getenv("LANGFUSE_PUBLIC_KEY"))
	cfg.SecretKey = strings.TrimSpace(os.Getenv("LANGFUSE_SECRET_KEY"))
	return cfg
}

// Validate 校验配置。启用时要求密钥完整、非遮罩、地址合法；失败返回错误但不中断业务。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.PublicKey == "" || c.SecretKey == "" {
		return fmt.Errorf("langfuse: enabled but LANGFUSE_PUBLIC_KEY or LANGFUSE_SECRET_KEY is missing")
	}
	if isMasked(c.PublicKey) || isMasked(c.SecretKey) {
		return fmt.Errorf("langfuse: enabled but credentials appear masked (contain \\u2022 or *)")
	}
	if len(c.PublicKey) <= len("pk-lf-") || len(c.SecretKey) <= len("sk-lf-") || !strings.HasPrefix(c.PublicKey, "pk-lf-") || !strings.HasPrefix(c.SecretKey, "sk-lf-") || strings.ContainsAny(c.PublicKey+c.SecretKey, " \t\r\n:") {
		return fmt.Errorf("langfuse: invalid credential format")
	}
	if err := validateBaseURL(c.BaseURL); err != nil {
		return fmt.Errorf("langfuse: invalid LANGFUSE_BASE_URL: %w", err)
	}
	if len(c.Environment) > 64 || strings.ContainsAny(c.Environment, "\r\n\t") {
		return fmt.Errorf("langfuse: invalid LANGFUSE_ENVIRONMENT")
	}
	if c.SampleRate < 0 || c.SampleRate > 1 || math.IsNaN(c.SampleRate) || math.IsInf(c.SampleRate, 0) {
		return fmt.Errorf("langfuse: LANGFUSE_SAMPLE_RATE must be a finite number in [0,1], got %g", c.SampleRate)
	}
	if c.MaxContentBytes < MinContentBytes || c.MaxContentBytes > MaxContentBytes {
		return fmt.Errorf("langfuse: LANGFUSE_MAX_CONTENT_BYTES must be in [%d,%d], got %d", MinContentBytes, MaxContentBytes, c.MaxContentBytes)
	}
	if c.ExportTimeoutSec < 1 || c.ExportTimeoutSec > 30 {
		return fmt.Errorf("langfuse: LANGFUSE_EXPORT_TIMEOUT_SECONDS must be in [1,30], got %d", c.ExportTimeoutSec)
	}
	if c.FlushTimeoutSec < 1 || c.FlushTimeoutSec > 30 {
		return fmt.Errorf("langfuse: LANGFUSE_FLUSH_TIMEOUT_SECONDS must be in [1,30], got %d", c.FlushTimeoutSec)
	}
	return nil
}

// IngestionURL 把基地址规范化为 Langfuse OTLP 导出端点。仅接受 origin（无 path/query），强制 HTTPS。
func (c Config) IngestionURL() (string, error) {
	if err := validateBaseURL(c.BaseURL); err != nil {
		return "", err
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", err
	}
	u.Path = IngestionPath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// BasicAuth 返回 Langfuse v4 导出所需的 Basic Auth 值 base64(public:secret)。
func (c Config) BasicAuth() string {
	return basicAuth(c.PublicKey, c.SecretKey)
}

func isMasked(s string) bool {
	return strings.Contains(s, "•") || strings.Contains(s, "*")
}

func validateBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("empty base URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("malformed URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https")
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("must be an origin only (no path)")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("must not contain query string")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("must not contain fragment")
	}
	if u.User != nil {
		return fmt.Errorf("must not contain userinfo credentials")
	}
	return nil
}

func basicAuth(public, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(public+":"+secret))
}

func getEnvOrDefaultBool(env string, defaultValue bool) bool {
	v := os.Getenv(env)
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}

func getEnvOrDefaultString(env string, defaultValue string) string {
	v := os.Getenv(env)
	if v == "" {
		return defaultValue
	}
	return v
}

func getEnvOrDefaultFloat(env string, defaultValue float64) float64 {
	v := os.Getenv(env)
	if v == "" {
		return defaultValue
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

func getEnvOrDefaultInt(env string, defaultValue int) int {
	v := os.Getenv(env)
	if v == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

// ConstantTimeCompare 使用恒等比较避免时序泄露，用于凭据格式比较等场景。
func ConstantTimeCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

const (
	DefaultBaseURL          = "https://us.cloud.langfuse.com"
	DefaultEnvironment      = "production"
	DefaultSampleRate       = 1.0
	DefaultCaptureContent   = false
	DefaultMaxContentBytes  = 8192
	DefaultExportTimeoutSec = 5
	DefaultFlushTimeoutSec  = 5

	IngestionPath    = "/api/public/otel/v1/traces"
	IngestionVersion = "4"
	MinContentBytes  = 256
	MaxContentBytes  = 32 << 20
)
