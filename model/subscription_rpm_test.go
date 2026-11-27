package model

import (
	"math"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionRPMDefaultsSnapshotAndTierValidation(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&SubscriptionRPMTier{}))
	plan := SubscriptionPlan{Title: "RPM", DurationUnit: SubscriptionDurationDay, DurationValue: 1, RPMTiers: []SubscriptionRPMTier{{RPM: 5, PriceAmount: 2, Enabled: true}}}
	require.NoError(t, DB.Create(&plan).Error)
	require.NotNil(t, plan.RPMLimit)
	assert.Equal(t, 5, *plan.RPMLimit)
	sub, err := CreateUserSubscriptionFromPlanTx(DB, 101, &plan, "admin")
	require.NoError(t, err)
	require.NoError(t, DB.Model(&plan).Update("rpm_limit", 20).Error)
	var saved UserSubscription
	require.NoError(t, DB.First(&saved, sub.Id).Error)
	assert.Equal(t, 5, saved.BaseRPM)
	assert.Equal(t, 5, saved.TotalRPM)
	for _, value := range []int{0, -1, MaxSubscriptionRPM + 1} {
		p := SubscriptionPlan{RPMLimit: &value}
		assert.Error(t, p.ValidateRPM())
	}
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1), 10000} {
		p := SubscriptionPlan{RPMTiers: []SubscriptionRPMTier{{RPM: 5, PriceAmount: value}}}
		assert.Error(t, p.ValidateRPM())
	}
}

func TestSubscriptionRPMOrdersSnapshotConcurrentCompletionAndExpiry(t *testing.T) {
	truncateTables(t)
	plan := SubscriptionPlan{Title: "RPM orders", Enabled: true, DurationUnit: SubscriptionDurationDay, DurationValue: 1, RPMTiers: []SubscriptionRPMTier{{RPM: 10, PriceAmount: 2, Enabled: true}}}
	require.NoError(t, DB.Create(&plan).Error)
	sub, err := CreateUserSubscriptionFromPlanTx(DB, 171, &plan, "admin")
	require.NoError(t, err)
	order := SubscriptionOrder{UserId: 171, PlanId: plan.Id, Purpose: SubscriptionOrderRPM, UserSubscriptionId: sub.Id, RPMTierId: plan.RPMTiers[0].Id, RPMIncrement: 10, Money: 2, TradeNo: "rpm-concurrent", PaymentProvider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Status: common.TopUpStatusPending}
	require.NoError(t, order.Insert())
	require.NoError(t, DB.Model(&SubscriptionRPMTier{}).Where("id = ?", order.RPMTierId).Updates(map[string]interface{}{"rpm": 50, "price_amount": 99}).Error)
	assert.Error(t, ValidateSubscriptionRPMReceipt(order.TradeNo, PaymentProviderStripe, "0.01", "USD"))
	assert.Error(t, ValidateSubscriptionRPMReceipt(order.TradeNo, PaymentProviderStripe, "2", "CNY"))
	assert.ErrorIs(t, ValidateSubscriptionRPMReceipt(order.TradeNo, PaymentProviderCreem, "2", "USD"), ErrPaymentMethodMismatch)
	require.NoError(t, ValidateSubscriptionRPMReceipt(order.TradeNo, PaymentProviderStripe, "2", "USD"))
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- CompleteSubscriptionOrder(order.TradeNo, "paid", PaymentProviderStripe, "")
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var saved UserSubscription
	require.NoError(t, DB.First(&saved, sub.Id).Error)
	assert.Equal(t, 15, saved.TotalRPM)
	order.Id = 0
	order.TradeNo = "rpm-second"
	require.NoError(t, order.Insert())
	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, "paid", PaymentProviderStripe, ""))
	require.NoError(t, DB.First(&saved, sub.Id).Error)
	assert.Equal(t, 25, saved.TotalRPM)
	require.NoError(t, DB.Model(&saved).Update("end_time", common.GetTimestamp()-1).Error)
	order.Id = 0
	order.TradeNo = "rpm-late"
	require.NoError(t, order.Insert())
	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, "late paid", PaymentProviderStripe, ""))
	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, "duplicate", PaymentProviderStripe, ""))
	late := GetSubscriptionOrderByTradeNo(order.TradeNo)
	require.NotNil(t, late)
	assert.Equal(t, SubscriptionOrderRefundPending, late.Status)
	require.NoError(t, DB.First(&saved, sub.Id).Error)
	assert.Equal(t, 25, saved.TotalRPM)
}

func TestSubscriptionRPMBalanceRejectsInsufficientDisabledAndExpired(t *testing.T) {
	truncateTables(t)
	user := User{Username: "rpm-insufficient", Quota: 1}
	require.NoError(t, DB.Create(&user).Error)
	plan := SubscriptionPlan{Title: "RPM validation", Enabled: true, DurationUnit: SubscriptionDurationDay, DurationValue: 1, RPMTiers: []SubscriptionRPMTier{{RPM: 5, PriceAmount: 1, Enabled: true}}}
	require.NoError(t, DB.Create(&plan).Error)
	sub, err := CreateUserSubscriptionFromPlanTx(DB, user.Id, &plan, "admin")
	require.NoError(t, err)
	assert.Error(t, PurchaseSubscriptionRPMWithBalance(user.Id, sub.Id, plan.RPMTiers[0].Id))
	require.NoError(t, DB.Model(&plan.RPMTiers[0]).Update("enabled", false).Error)
	_, _, err = GetSubscriptionRPMPurchase(user.Id, sub.Id, plan.RPMTiers[0].Id)
	assert.Error(t, err)
	require.NoError(t, DB.Model(&sub).Update("end_time", common.GetTimestamp()-1).Error)
	assert.Error(t, PurchaseSubscriptionRPMWithBalance(user.Id, sub.Id, plan.RPMTiers[0].Id))
	var saved User
	require.NoError(t, DB.First(&saved, user.Id).Error)
	assert.Equal(t, 1, saved.Quota)
	var count int64
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
}

func TestSubscriptionRPMMigrationIsRepeatable(t *testing.T) {
	truncateTables(t)
	for i := 0; i < 2; i++ {
		require.NoError(t, ensureSubscriptionPlanTableSQLite())
		require.NoError(t, DB.AutoMigrate(&SubscriptionRPMTier{}, &UserSubscription{}))
	}
	plan := SubscriptionPlan{Title: "Migrated"}
	require.NoError(t, DB.Create(&plan).Error)
	var stored SubscriptionPlan
	require.NoError(t, DB.First(&stored, plan.Id).Error)
	require.NotNil(t, stored.RPMLimit)
	assert.Equal(t, 5, *stored.RPMLimit)
}

func TestSubscriptionRPMBalancePurchaseAndIdempotentCompletion(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&SubscriptionRPMTier{}))
	user := User{Username: "rpm-user", Quota: 10000000}
	require.NoError(t, DB.Create(&user).Error)
	plan := SubscriptionPlan{Title: "RPM balance", Enabled: true, DurationUnit: SubscriptionDurationDay, DurationValue: 1, RPMTiers: []SubscriptionRPMTier{{RPM: 10, PriceAmount: 1, Enabled: true}}}
	require.NoError(t, DB.Create(&plan).Error)
	sub, err := CreateUserSubscriptionFromPlanTx(DB, user.Id, &plan, "admin")
	require.NoError(t, err)
	tier := plan.RPMTiers[0]
	require.Error(t, PurchaseSubscriptionRPMWithBalance(user.Id+1, sub.Id, tier.Id))
	require.NoError(t, PurchaseSubscriptionRPMWithBalance(user.Id, sub.Id, tier.Id))
	var order SubscriptionOrder
	require.NoError(t, DB.Where("purpose = ? AND user_id = ?", SubscriptionOrderRPM, user.Id).First(&order).Error)
	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, "repeat", PaymentProviderBalance, ""))
	var saved UserSubscription
	require.NoError(t, DB.First(&saved, sub.Id).Error)
	assert.Equal(t, 15, saved.TotalRPM)
	assert.Equal(t, sub.EndTime, saved.EndTime)
	charge, err := calcSubscriptionBalanceQuota(1)
	require.NoError(t, err)
	var savedUser User
	require.NoError(t, DB.First(&savedUser, user.Id).Error)
	assert.Equal(t, user.Quota-charge, savedUser.Quota)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
}
