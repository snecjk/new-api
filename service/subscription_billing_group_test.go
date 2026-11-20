package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionBillingGroupMismatchUsesWalletAndMatchingGroupUsesPass(t *testing.T) {
	truncate(t)
	ensureServiceSubscriptionBillingTables(t)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.DB.Exec("DELETE FROM subscription_plans")
	})
	user := &model.User{Username: "billing-group-user", Quota: 50, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(user).Error)
	plan := &model.SubscriptionPlan{Title: "Default day", BillingGroup: "default", DurationUnit: model.SubscriptionDurationDay, DurationValue: 1, TotalAmount: 0}
	require.NoError(t, model.DB.Create(plan).Error)
	now := model.GetDBTimestamp()
	sub := &model.UserSubscription{UserId: user.Id, PlanId: plan.Id, Status: "active", StartTime: now - 10, EndTime: now + 1000, AmountTotal: 0}
	require.NoError(t, model.DB.Create(sub).Error)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	stable := &relaycommon.RelayInfo{RequestId: "billing-group-wallet", UserId: user.Id, UsingGroup: "gpt-stable", IsPlayground: true, ForcePreConsume: true}
	stable.UserSetting.BillingPreference = "subscription_only"
	session, apiErr := NewBillingSession(c, stable, 10)
	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.Equal(t, BillingSourceWallet, stable.BillingSource)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 40, user.Quota)
	require.NoError(t, model.DB.First(sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)

	regular := &relaycommon.RelayInfo{RequestId: "billing-group-pass", UserId: user.Id, UsingGroup: "default", IsPlayground: true, ForcePreConsume: true}
	regular.UserSetting.BillingPreference = "subscription_first"
	session, apiErr = NewBillingSession(c, regular, 10)
	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.Equal(t, BillingSourceSubscription, regular.BillingSource)
	require.NoError(t, model.DB.First(sub, sub.Id).Error)
	assert.EqualValues(t, 10, sub.AmountUsed)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 40, user.Quota)

	require.NoError(t, model.DB.Model(user).Update("quota", 0).Error)
	noWallet := &relaycommon.RelayInfo{RequestId: "billing-group-no-wallet", UserId: user.Id, UsingGroup: "gpt-stable", IsPlayground: true, ForcePreConsume: true}
	noWallet.UserSetting.BillingPreference = "subscription_first"
	session, apiErr = NewBillingSession(c, noWallet, 10)
	require.Nil(t, session)
	require.NotNil(t, apiErr)
	require.NoError(t, model.DB.First(sub, sub.Id).Error)
	assert.EqualValues(t, 10, sub.AmountUsed)
}

func TestSubscriptionBillingGroupRetryRebindsFundingToConcreteGroup(t *testing.T) {
	truncate(t)
	ensureServiceSubscriptionBillingTables(t)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.DB.Exec("DELETE FROM subscription_plans")
	})
	user := &model.User{Username: "billing-group-retry-user", Quota: 50, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(user).Error)
	plan := &model.SubscriptionPlan{Title: "Default day", BillingGroup: "default", DurationUnit: model.SubscriptionDurationDay, DurationValue: 1}
	require.NoError(t, model.DB.Create(plan).Error)
	now := model.GetDBTimestamp()
	sub := &model.UserSubscription{UserId: user.Id, PlanId: plan.Id, Status: "active", StartTime: now - 10, EndTime: now + 1000}
	require.NoError(t, model.DB.Create(sub).Error)
	c, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{RequestId: "billing-group-retry", UserId: user.Id, UsingGroup: "default", IsPlayground: true, ForcePreConsume: true}
	info.UserSetting.BillingPreference = "subscription_first"
	require.Nil(t, PreConsumeBilling(c, 10, info))
	assert.Equal(t, BillingSourceSubscription, info.BillingSource)

	info.UsingGroup = "gpt-stable"
	require.Nil(t, RebindBillingForGroupRetry(c, info, 10))
	assert.Equal(t, BillingSourceWallet, info.BillingSource)
	assert.Equal(t, "billing-group-retry", info.RequestId)
	require.NoError(t, model.DB.First(sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 40, user.Quota)

	info.UsingGroup = "default"
	require.Nil(t, RebindBillingForGroupRetry(c, info, 10))
	assert.Equal(t, BillingSourceSubscription, info.BillingSource)
	require.NoError(t, model.DB.First(sub, sub.Id).Error)
	assert.EqualValues(t, 10, sub.AmountUsed)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 50, user.Quota)
}

func TestSubscriptionBillingGroupUnresolvedAutoNeverUsesRestrictedPlan(t *testing.T) {
	truncate(t)
	ensureServiceSubscriptionBillingTables(t)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.DB.Exec("DELETE FROM subscription_plans")
	})
	user := &model.User{Username: "billing-group-auto-user", Quota: 10, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(user).Error)
	plan := &model.SubscriptionPlan{Title: "Default day", BillingGroup: "default", DurationUnit: model.SubscriptionDurationDay, DurationValue: 1}
	require.NoError(t, model.DB.Create(plan).Error)
	now := model.GetDBTimestamp()
	sub := &model.UserSubscription{UserId: user.Id, PlanId: plan.Id, Status: "active", StartTime: now - 10, EndTime: now + 1000}
	require.NoError(t, model.DB.Create(sub).Error)
	c, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{RequestId: "billing-group-auto", UserId: user.Id, UsingGroup: "auto", IsPlayground: true, ForcePreConsume: true}
	info.UserSetting.BillingPreference = "subscription_only"
	require.NotNil(t, PreConsumeBilling(c, 5, info))
	assert.Empty(t, info.BillingSource)
	require.NoError(t, model.DB.First(sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Equal(t, 10, user.Quota)
}

func ensureServiceSubscriptionBillingTables(t *testing.T) {
	t.Helper()
	if !model.DB.Migrator().HasTable(&model.SubscriptionPlan{}) {
		require.NoError(t, model.DB.Migrator().CreateTable(&model.SubscriptionPlan{}))
	}
	if !model.DB.Migrator().HasTable(&model.SubscriptionPreConsumeRecord{}) {
		require.NoError(t, model.DB.Migrator().CreateTable(&model.SubscriptionPreConsumeRecord{}))
	}
}
