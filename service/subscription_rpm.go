package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/go-redis/redis/v8"
)

const subscriptionRPMErrorCode types.ErrorCode = "subscription_rpm_limit"

var subscriptionRPMScript = redis.NewScript(`
local tm = redis.call('TIME')
local now = tonumber(tm[1]) * 1000 + math.floor(tonumber(tm[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - 60000)
if redis.call('ZSCORE', KEYS[1], ARGV[2]) then return 1 end
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[1], now, ARGV[2])
redis.call('PEXPIRE', KEYS[1], 60000)
return 1
`)

type subscriptionRPMWindow struct {
	mu          sync.Mutex
	requests    map[int]map[string]int64
	lastCleanup int64
}

var subscriptionRPMMemory = subscriptionRPMWindow{requests: make(map[int]map[string]int64)}

func (w *subscriptionRPMWindow) allow(id, limit int, requestId string, now int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if now-w.lastCleanup >= 60000 {
		for key, entries := range w.requests {
			for request, at := range entries {
				if at <= now-60000 {
					delete(entries, request)
				}
			}
			if len(entries) == 0 {
				delete(w.requests, key)
			}
		}
		w.lastCleanup = now
	}
	entries := w.requests[id]
	if entries == nil {
		entries = make(map[string]int64)
		w.requests[id] = entries
	}
	for request, at := range entries {
		if at <= now-60000 {
			delete(entries, request)
		}
	}
	if _, found := entries[requestId]; found {
		return true
	}
	if len(entries) >= limit {
		return false
	}
	entries[requestId] = now
	return true
}

// CheckSubscriptionRPM reads live rights so a paid upgrade applies on the next request.
func CheckSubscriptionRPM(ctx context.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if info.BillingSource != BillingSourceSubscription || info.SubscriptionId <= 0 {
		return nil
	}
	var sub model.UserSubscription
	now := model.GetDBTimestamp()
	if err := model.DB.Where("id = ? AND user_id = ? AND status = ? AND start_time <= ? AND end_time > ?", info.SubscriptionId, info.UserId, "active", now, now).First(&sub).Error; err != nil {
		return types.NewErrorWithStatusCode(fmt.Errorf("订阅不存在或已失效"), types.ErrorCodeAccessDenied, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	if sub.BaseRPM < 1 || sub.AddedRPM < 0 || sub.AddedRPM > model.MaxSubscriptionRPM-sub.BaseRPM {
		return types.NewError(fmt.Errorf("invalid subscription RPM"), types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	allowed := false
	if common.RedisEnabled {
		if common.RDB == nil {
			return types.NewError(fmt.Errorf("RPM Redis unavailable"), types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		result, err := subscriptionRPMScript.Run(ctx, common.RDB, []string{fmt.Sprintf("subscription:rpm:%d", sub.Id)}, sub.TotalRPM, info.RequestId).Int()
		if err != nil {
			return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		allowed = result == 1
	} else {
		allowed = subscriptionRPMMemory.allow(sub.Id, sub.TotalRPM, info.RequestId, time.Now().UnixMilli())
	}
	if !allowed {
		return types.NewErrorWithStatusCode(fmt.Errorf("订阅 RPM 限制：每分钟最多 %d 次请求", sub.TotalRPM), subscriptionRPMErrorCode, http.StatusTooManyRequests, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return nil
}
