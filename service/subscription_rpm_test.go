package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionRPMRollingWindowAndConcurrentTokens(t *testing.T) {
	w := subscriptionRPMWindow{requests: make(map[int]map[string]int64)}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	// Two tokens share one subscription with only one remaining slot.
	for _, token := range []string{"token-a", "token-b"} {
		wg.Add(1)
		go func(request string) {
			defer wg.Done()
			if w.allow(1, 1, request, 100000) {
				allowed.Add(1)
			}
		}(token)
	}
	wg.Wait()
	assert.EqualValues(t, 1, allowed.Load())
	assert.True(t, w.allow(2, 1, "other-subscription", 100000))
	assert.False(t, w.allow(1, 1, "blocked", 159999))
	assert.True(t, w.allow(1, 1, "next-window", 160000))
	assert.True(t, w.allow(1, 1, "next-window", 160001), "retry must not consume another slot")
	assert.False(t, w.allow(1, 1, "upgrade", 160001))
	assert.True(t, w.allow(1, 2, "upgrade", 160001), "new rights apply without resetting existing count")
}

func TestSubscriptionRPMRedisRollingWindowAndDeduplication(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetTime(time.Unix(100, 0))
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		got, err := subscriptionRPMScript.Run(ctx, client, []string{"rpm:1"}, 5, fmt.Sprint(i)).Int()
		require.NoError(t, err)
		assert.Equal(t, 1, got)
	}
	got, err := subscriptionRPMScript.Run(ctx, client, []string{"rpm:1"}, 5, "0").Int()
	require.NoError(t, err)
	assert.Equal(t, 1, got)
	got, err = subscriptionRPMScript.Run(ctx, client, []string{"rpm:1"}, 5, "sixth").Int()
	require.NoError(t, err)
	assert.Zero(t, got)
	got, err = subscriptionRPMScript.Run(ctx, client, []string{"rpm:1"}, 6, "sixth").Int()
	require.NoError(t, err)
	assert.Equal(t, 1, got)
	server.SetTime(time.Unix(160, 0))
	got, err = subscriptionRPMScript.Run(ctx, client, []string{"rpm:1"}, 5, "new-window").Int()
	require.NoError(t, err)
	assert.Equal(t, 1, got)
}

func TestSubscriptionRPMBillingPurchaseAndExpiryReplay(t *testing.T) {
	truncate(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	oldRedis, oldEnabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() { common.RDB, common.RedisEnabled = oldRedis, oldEnabled; require.NoError(t, client.Close()) })
	// TestMain owns a single-connection SQLite database. Create these tables once.
	for _, table := range []interface{}{&model.SubscriptionPlan{}, &model.SubscriptionRPMTier{}, &model.SubscriptionOrder{}, &model.SubscriptionPreConsumeRecord{}} {
		if !model.DB.Migrator().HasTable(table) {
			require.NoError(t, model.DB.AutoMigrate(table))
		}
	}
	t.Cleanup(func() {
		for _, table := range []interface{}{&model.SubscriptionRPMTier{}, &model.SubscriptionOrder{}, &model.SubscriptionPreConsumeRecord{}, &model.SubscriptionPlan{}} {
			require.NoError(t, model.DB.Where("1 = 1").Delete(table).Error)
		}
		subscriptionRPMMemory = subscriptionRPMWindow{requests: make(map[int]map[string]int64)}
	})
	subscriptionRPMMemory = subscriptionRPMWindow{requests: make(map[int]map[string]int64)}
	seedUser(t, 881, 10000000)
	seedToken(t, 881, 881, "rpm-token-a", 10000)
	seedToken(t, 882, 881, "rpm-token-b", 10000)
	for _, key := range []string{"rpm-token-a", "rpm-token-b"} {
		var token model.Token
		require.NoError(t, model.DB.Where(map[string]interface{}{"key": key}).First(&token).Error)
		require.NoError(t, common.RedisHSetObj("token:"+common.GenerateHMAC(key), &token, time.Hour))
	}
	plan := model.SubscriptionPlan{Title: "RPM replay", Enabled: true, PriceAmount: 1, DurationUnit: model.SubscriptionDurationDay, DurationValue: 1, TotalAmount: 10000, RPMTiers: []model.SubscriptionRPMTier{{RPM: 5, PriceAmount: 1, Enabled: true}}}
	require.NoError(t, model.DB.Create(&plan).Error)
	require.NoError(t, model.PurchaseSubscriptionWithBalance(881, plan.Id))
	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", 881).First(&sub).Error)
	assert.Equal(t, 5, sub.TotalRPM)
	var upstream int
	handler := gin.New()
	handler.POST("/relay", func(c *gin.Context) {
		tokenId, tokenKey := 881, "rpm-token-a"
		if c.GetHeader("Token") == "b" {
			tokenId, tokenKey = 882, "rpm-token-b"
		}
		info := &relaycommon.RelayInfo{UserId: 881, TokenId: tokenId, TokenKey: tokenKey, RequestId: c.GetHeader("Request-ID"), ForcePreConsume: true}
		info.UserSetting.BillingPreference = "subscription_first"
		if c.GetHeader("Wallet") == "true" {
			info.UserSetting.BillingPreference = "wallet_only"
		}
		if apiErr := PreConsumeBilling(c, 10, info); apiErr != nil {
			c.Status(apiErr.StatusCode)
			return
		}
		upstream++
		require.NoError(t, info.Billing.Settle(10))
		c.Status(http.StatusOK)
	})
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/relay", nil)
		req.Header.Set("Request-ID", fmt.Sprintf("rpm-replay-%d", i))
		if i%2 == 1 {
			req.Header.Set("Token", "b")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		expected := http.StatusOK
		if i == 5 {
			expected = http.StatusTooManyRequests
		}
		assert.Equal(t, expected, response.Code)
	}
	assert.Equal(t, 5, upstream, "429 must stop dispatch")
	require.NoError(t, model.DB.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 50, sub.AmountUsed)
	var token model.Token
	require.NoError(t, model.DB.First(&token, 882).Error)
	assert.Equal(t, 9980, token.RemainQuota, "rejected request restores token quota")
	var user model.User
	require.NoError(t, model.DB.First(&user, 881).Error)
	walletAfterPlan := user.Quota
	require.NoError(t, model.PurchaseSubscriptionRPMWithBalance(881, sub.Id, plan.RPMTiers[0].Id))
	req := httptest.NewRequest(http.MethodPost, "/relay", nil)
	req.Header.Set("Request-ID", "after-rpm-purchase")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	assert.Equal(t, http.StatusOK, response.Code)
	require.NoError(t, model.DB.First(&sub, sub.Id).Error)
	assert.Equal(t, 10, sub.TotalRPM)
	assert.EqualValues(t, 60, sub.AmountUsed)
	require.NoError(t, model.DB.First(&user, 881).Error)
	// Both purchases cost the same; rejected API requests must never fall back to wallet.
	assert.Equal(t, 2*walletAfterPlan-10000000, user.Quota)
	require.NoError(t, model.DB.Model(&sub).Update("end_time", common.GetTimestamp()-1).Error)
	info := &relaycommon.RelayInfo{UserId: 881, BillingSource: BillingSourceSubscription, SubscriptionId: sub.Id, RequestId: "expired"}
	apiErr := CheckSubscriptionRPM(context.Background(), info)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	req = httptest.NewRequest(http.MethodPost, "/relay", nil)
	req.Header.Set("Request-ID", "wallet-request")
	req.Header.Set("Wallet", "true")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	assert.Equal(t, http.StatusOK, response.Code)
}
