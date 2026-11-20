package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionBillingGroupSelectsEligiblePlanAndTracksCurrentEdit(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&SubscriptionPreConsumeRecord{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM subscription_pre_consume_records") })

	now := GetDBTimestamp()
	day := &SubscriptionPlan{Title: "Default day", BillingGroup: "default", DurationUnit: SubscriptionDurationDay, DurationValue: 1, TotalAmount: 0}
	stable := &SubscriptionPlan{Title: "Stable plan", BillingGroup: "gpt-stable", DurationUnit: SubscriptionDurationDay, DurationValue: 1, TotalAmount: 100}
	require.NoError(t, DB.Create(day).Error)
	require.NoError(t, DB.Create(stable).Error)
	daySub := &UserSubscription{UserId: 9876, PlanId: day.Id, Status: "active", StartTime: now - 10, EndTime: now + 1000, AmountTotal: 0}
	stableSub := &UserSubscription{UserId: 9876, PlanId: stable.Id, Status: "active", StartTime: now - 10, EndTime: now + 2000, AmountTotal: 100}
	require.NoError(t, DB.Create(daySub).Error)
	require.NoError(t, DB.Create(stableSub).Error)

	eligible, err := HasEligibleUserSubscription(9876, "gpt-stable")
	require.NoError(t, err)
	assert.True(t, eligible)
	charged, err := PreConsumeUserSubscription("group-stable-1", 9876, "test-model", 0, 10, "gpt-stable")
	require.NoError(t, err)
	assert.Equal(t, stableSub.Id, charged.UserSubscriptionId)
	require.NoError(t, DB.First(daySub, daySub.Id).Error)
	assert.Zero(t, daySub.AmountUsed)

	require.NoError(t, DB.Model(stable).Update("billing_group", "other").Error)
	InvalidateSubscriptionPlanCache(stable.Id)
	eligible, err = HasEligibleUserSubscription(9876, "gpt-stable")
	require.NoError(t, err)
	assert.False(t, eligible)
	_, err = PreConsumeUserSubscription("group-stable-2", 9876, "test-model", 0, 10, "gpt-stable")
	assert.ErrorIs(t, err, ErrNoEligibleSubscription)
	require.NoError(t, DB.First(stableSub, stableSub.Id).Error)
	assert.EqualValues(t, 10, stableSub.AmountUsed)
}

func TestSubscriptionBillingGroupUnrestrictedLegacyPlanAndReplay(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&SubscriptionPreConsumeRecord{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM subscription_pre_consume_records") })

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{Title: "Legacy", DurationUnit: SubscriptionDurationDay, DurationValue: 1, TotalAmount: 0}
	require.NoError(t, DB.Create(plan).Error)
	sub := &UserSubscription{UserId: 9877, PlanId: plan.Id, Status: "active", StartTime: now - 10, EndTime: now + 1000, AmountTotal: 0}
	require.NoError(t, DB.Create(sub).Error)
	charged, err := PreConsumeUserSubscription("group-legacy", 9877, "test-model", 0, 5, "gpt-stable")
	require.NoError(t, err)
	assert.Equal(t, sub.Id, charged.UserSubscriptionId)

	require.NoError(t, DB.Model(plan).Update("billing_group", "default").Error)
	InvalidateSubscriptionPlanCache(plan.Id)
	_, err = PreConsumeUserSubscription("group-legacy", 9877, "test-model", 0, 5, "gpt-stable")
	assert.True(t, errors.Is(err, ErrNoEligibleSubscription))
}
