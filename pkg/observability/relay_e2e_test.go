package observability_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/observability"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ingestionFixture func(*http.Request) (*http.Response, error)

func (f ingestionFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the real controller, channel selector, HTTP parser and wallet/token
// billing. Different priorities make retries deterministic (one channel per tier).
func TestRelayTracingRetryBillingAndExportIsolation(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldPath, oldMaster := common.SQLitePath, common.IsMasterNode
	t.Setenv("SQL_DSN", "local")
	t.Setenv("LOG_SQL_DSN", "")
	common.SQLitePath, common.IsMasterNode = ":memory:", false
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	oldRedis, oldCache, oldBatch, oldConsume := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
	oldCount, oldErrorLog, oldRetry, oldStreamTimeout := constant.CountToken, constant.ErrorLogEnabled, common.RetryTimes, constant.StreamingTimeout
	mainType, logType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, false, false
	constant.CountToken, constant.ErrorLogEnabled, common.RetryTimes, constant.StreamingTimeout = false, false, 1, 30
	t.Cleanup(func() {
		// Existing billing/performance callbacks use the shared pool. Drain them
		// before restoring their database/cache fixture, without sleeps.
		wait, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for gopool.WorkerCount() > 0 {
			select {
			case <-wait.Done():
				t.Fatal("relay background callbacks did not drain")
			default:
				runtime.Gosched()
			}
		}
		observability.SetGlobalExporter(nil)
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SQLitePath, common.IsMasterNode = oldPath, oldMaster
		common.SetDatabaseTypes(mainType, logType)
		common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = oldRedis, oldCache, oldBatch, oldConsume
		constant.CountToken, constant.ErrorLogEnabled, common.RetryTimes, constant.StreamingTimeout = oldCount, oldErrorLog, oldRetry, oldStreamTimeout
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}))
	service.InitHttpClient()
	service.InitTokenEncoders()
	oldRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o-mini":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios)) })

	for _, scenario := range []string{"success", "retry-success", "all-failed", "quota-rejected", "stream"} {
		t.Run(scenario, func(t *testing.T) {
			var baseline struct {
				status                                      int
				body                                        string
				userQuota, tokenQuota, tokenUsed, used, pre int
				calls                                       int64
				flushed                                     bool
			}
			modes := []string{"disabled", "memory"}
			if scenario == "success" || scenario == "stream" {
				modes = append(modes, "blocked", "401", "429", "network")
			}
			for _, mode := range modes {
				t.Run(mode, func(t *testing.T) {
					for _, entity := range []any{&model.Ability{}, &model.Channel{}, &model.Token{}, &model.User{}} {
						require.NoError(t, db.Unscoped().Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(entity).Error)
					}
					quota := 1000000
					if scenario == "quota-rejected" {
						quota = 0
					}
					require.NoError(t, db.Create(&model.User{Id: 1, Username: "trace-fixture", Quota: quota, Group: "default"}).Error)
					require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "SECRET_CLIENT_KEY", RemainQuota: 1000000, Status: common.TokenStatusEnabled}).Error)
					var calls atomic.Int64
					var pre atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						n := calls.Add(1)
						var user model.User
						assert.NoError(t, db.First(&user, 1).Error)
						pre.Store(int64(quota - user.Quota))
						assert.Empty(t, r.Header.Get("Baggage"))
						assert.NotContains(t, r.Header.Get("Authorization"), "pk-lf-")
						w.Header().Set("Content-Type", "application/json")
						if scenario == "all-failed" || (scenario == "retry-success" && n == 1) {
							w.WriteHeader(500)
							_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"SECRET_UPSTREAM_BODY"}}`)
							return
						}
						if scenario == "stream" {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: {\"id\":\"fixed\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixed\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
							return
						}
						_, _ = io.WriteString(w, `{"id":"fixed","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
					}))
					defer server.Close()
					channels := make([]model.Channel, 2)
					for i := range channels {
						priority, autoBan, base := int64(2-i), 0, server.URL
						channels[i] = model.Channel{Id: i + 1, Type: constant.ChannelTypeOpenAI, Key: "SECRET_CHANNEL_KEY", Status: common.ChannelStatusEnabled, Name: "fixture", Models: "gpt-4o-mini", Group: "default", BaseURL: &base, Priority: &priority, AutoBan: &autoBan}
						require.NoError(t, db.Create(&channels[i]).Error)
						require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "gpt-4o-mini", ChannelId: i + 1, Enabled: true, Priority: &priority}).Error)
					}
					sink := tracetest.NewInMemoryExporter()
					entered, release := make(chan struct{}, 1), make(chan struct{})
					var exportReturned atomic.Bool
					exp := observability.NewTestExporter(observability.Config{Enabled: mode != "disabled", SampleRate: 1, Environment: "test"}, sink)
					if mode != "disabled" && mode != "memory" {
						exp, err = observability.NewTestHTTPExporter(&http.Client{Transport: ingestionFixture(func(r *http.Request) (*http.Response, error) {
							data, readErr := io.ReadAll(r.Body)
							if readErr != nil {
								return nil, readErr
							}
							var payload collector.ExportTraceServiceRequest
							assert.NoError(t, proto.Unmarshal(data, &payload))
							assert.NotContains(t, string(data), "SECRET_")
							if mode == "network" {
								return nil, errors.New("SECRET_NETWORK_ERROR")
							}
							if mode == "blocked" {
								select {
								case entered <- struct{}{}:
								default:
								}
								select {
								case <-release:
								case <-r.Context().Done():
								}
								exportReturned.Store(true)
								return nil, errors.New("fixture export interrupted")
							}
							status := 401
							if mode == "429" {
								status = 429
							}
							return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("SECRET_OTLP_BODY"))}, nil
						})})
						require.NoError(t, err)
					}
					observability.SetGlobalExporter(exp)
					defer observability.SetGlobalExporter(nil)
					if mode == "blocked" {
						// Start a real background OTLP batch before exercising the relay.
						for range 64 {
							_, root := observability.StartRootSpan(exp.Tracer("fixture"), context.Background(), observability.RootSpanStart{})
							root.End(observability.OutcomeOk, nil)
						}
						wait, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						select {
						case <-entered:
						case <-wait.Done():
							t.Fatal("export batch did not start")
						}
					}
					refunded := make(chan struct{}, 1)
					require.NoError(t, db.Callback().Update().After("gorm:update").Register("trace:refund", func(tx *gorm.DB) {
						if tx.Statement.Table == "tokens" && scenario == "all-failed" {
							if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
								if expr, ok := values["remain_quota"].(clause.Expr); ok && strings.Contains(expr.SQL, "+") {
									refunded <- struct{}{}
								}
							}
						}
					}))
					defer db.Callback().Update().Remove("trace:refund")
					writer := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(writer)
					body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"max_tokens":16}`
					if scenario == "stream" {
						body = strings.TrimSuffix(body, "}") + `,"stream":true,"stream_options":{"include_usage":true}}`
					}
					c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")
					c.Request.Header.Set("Authorization", "Bearer SECRET_CLIENT_KEY")
					c.Set(common.RequestIdKey, "relay-e2e")
					common.SetContextKey(c, constant.ContextKeyUserId, 1)
					common.SetContextKey(c, constant.ContextKeyTokenId, 1)
					common.SetContextKey(c, constant.ContextKeyTokenKey, "SECRET_CLIENT_KEY")
					common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
					common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
					common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only", QuotaWarningThreshold: 1})
					require.Nil(t, middleware.SetupContextForSelectedChannel(c, &channels[0], "gpt-4o-mini"))
					controller.Relay(c, types.RelayFormatOpenAI)
					if mode == "blocked" {
						assert.False(t, exportReturned.Load(), "relay must finish while OTLP is still blocked")
						close(release)
					}
					if scenario == "all-failed" {
						wait, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						select {
						case <-refunded:
						case <-wait.Done():
							t.Fatal("refund did not finish")
						}
					}
					var user model.User
					var token model.Token
					require.NoError(t, db.First(&user, 1).Error)
					require.NoError(t, db.First(&token, 1).Error)
					got := baseline
					got.flushed = writer.Flushed
					got.status, got.body, got.userQuota, got.tokenQuota, got.tokenUsed, got.used, got.pre, got.calls = writer.Code, writer.Body.String(), user.Quota, token.RemainQuota, token.UsedQuota, user.UsedQuota, int(pre.Load()), calls.Load()
					if mode == "disabled" {
						baseline = got
					} else {
						assert.Equal(t, baseline, got)
					}
					if scenario == "quota-rejected" {
						assert.Equal(t, 403, writer.Code)
						assert.Zero(t, calls.Load())
					} else if scenario == "all-failed" {
						assert.EqualValues(t, 2, calls.Load())
						assert.Equal(t, 500, writer.Code)
						assert.Equal(t, quota, user.Quota)
						assert.Equal(t, 1000000, token.RemainQuota)
						assert.Zero(t, token.UsedQuota)
					} else {
						wantCalls := int64(1)
						if scenario == "retry-success" {
							wantCalls = 2
						}
						assert.Equal(t, wantCalls, calls.Load())
						assert.Equal(t, 200, writer.Code)
						assert.Positive(t, pre.Load(), "fixture must exercise real pre-consumption")
						assert.Positive(t, user.UsedQuota)
						assert.Equal(t, 1000000-user.UsedQuota, user.Quota)
					}
					if mode == "memory" {
						spans := sink.GetSpans()
						require.Len(t, spans, int(calls.Load())+1)
						root := spans[len(spans)-1]
						want := codes.Ok
						if scenario == "all-failed" || scenario == "quota-rejected" {
							want = codes.Error
						}
						assert.Equal(t, want, root.Status.Code)
						for i, span := range spans[:len(spans)-1] {
							assert.Equal(t, root.SpanContext.TraceID(), span.SpanContext.TraceID())
							assert.Equal(t, root.SpanContext.SpanID(), span.Parent.SpanID())
							attrs := attribute.NewSet(span.Attributes...)
							index, _ := attrs.Value(attribute.Key(observability.AttrMetadata + ".retry_index"))
							assert.EqualValues(t, i, index.AsInt64())
							_, hasUsage := attrs.Value(attribute.Key(observability.AttrUsageDetails))
							if span.Status.Code == codes.Error {
								assert.False(t, hasUsage)
							} else {
								assert.True(t, hasUsage)
							}
						}
						for _, span := range spans {
							for _, a := range span.Attributes {
								assert.NotContains(t, a.Value.Emit(), "SECRET_")
							}
						}
					}
					shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					require.NoError(t, exp.Shutdown(shutdown))
					if mode != "memory" && mode != "disabled" {
						assert.Positive(t, exp.FailureCount())
					}
					if scenario == "stream" {
						assert.True(t, writer.Flushed)
					}
				})
			}
		})
	}
}
