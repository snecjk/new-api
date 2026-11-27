package model

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	DefaultSubscriptionRPM         = 5
	MaxSubscriptionRPM             = 1000000
	SubscriptionOrderRPM           = "rpm"
	SubscriptionOrderRefundPending = "refund_pending"
)

// ValidateSubscriptionRPMReceipt applies to add-ons only, after provider signature verification.
func ValidateSubscriptionRPMReceipt(tradeNo, provider, amount, currency string) error {
	order := GetSubscriptionOrderByTradeNo(tradeNo)
	if order == nil || order.Purpose != SubscriptionOrderRPM {
		return nil
	}
	if order.PaymentProvider != provider {
		return ErrPaymentMethodMismatch
	}
	paid, err := decimal.NewFromString(amount)
	if err != nil || !strings.EqualFold(currency, "USD") || !paid.Equal(decimal.NewFromFloat(order.Money).Round(2)) {
		return errors.New("RPM payment amount or currency mismatch")
	}
	return nil
}

type SubscriptionRPMTier struct {
	Id                    int     `json:"id"`
	PlanId                int     `json:"plan_id" gorm:"index"`
	RPM                   int     `json:"rpm"`
	PriceAmount           float64 `json:"price_amount" gorm:"not null"`
	Enabled               bool    `json:"enabled"`
	StripePriceId         string  `json:"stripe_price_id" gorm:"type:varchar(128)"`
	CreemProductId        string  `json:"creem_product_id" gorm:"type:varchar(128)"`
	WaffoPancakeProductId string  `json:"waffo_pancake_product_id" gorm:"type:varchar(128)"`
}

func (p *SubscriptionPlan) AfterCreate(tx *gorm.DB) error {
	for i := range p.RPMTiers {
		p.RPMTiers[i].Id = 0
		p.RPMTiers[i].PlanId = p.Id
	}
	if len(p.RPMTiers) == 0 {
		return nil
	}
	return tx.Create(&p.RPMTiers).Error
}

func LoadSubscriptionRPMTiers(plans []SubscriptionPlan, enabledOnly bool) error {
	if len(plans) == 0 {
		return nil
	}
	ids := make([]int, len(plans))
	for i := range plans {
		ids[i] = plans[i].Id
		plans[i].RPMTiers = []SubscriptionRPMTier{}
	}
	var tiers []SubscriptionRPMTier
	query := DB.Where("plan_id IN ?", ids)
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	if err := query.Order("id asc").Find(&tiers).Error; err != nil {
		return err
	}
	byPlan := make(map[int][]SubscriptionRPMTier)
	for _, tier := range tiers {
		byPlan[tier.PlanId] = append(byPlan[tier.PlanId], tier)
	}
	for i := range plans {
		if found := byPlan[plans[i].Id]; found != nil {
			plans[i].RPMTiers = found
		}
	}
	return nil
}

func (p *SubscriptionPlan) ValidateRPM() error {
	if p.RPMLimit != nil && (*p.RPMLimit <= 0 || *p.RPMLimit > MaxSubscriptionRPM) {
		return errors.New("RPM 必须为 1 到 1000000 的整数")
	}
	if len(p.RPMTiers) > 50 {
		return errors.New("RPM 加购档位不能超过 50 个")
	}
	for _, tier := range p.RPMTiers {
		if tier.RPM <= 0 || tier.RPM > MaxSubscriptionRPM || math.IsNaN(tier.PriceAmount) || math.IsInf(tier.PriceAmount, 0) || tier.PriceAmount < 0.01 || tier.PriceAmount > 9999 {
			return errors.New("RPM 增量必须为 1 到 1000000 的整数，价格必须为 0.01 到 9999")
		}
	}
	return nil
}

// ReplaceSubscriptionRPMTiersTx preserves tier identity for pending orders and UI.
func ReplaceSubscriptionRPMTiersTx(tx *gorm.DB, planId int, tiers []SubscriptionRPMTier) error {
	ids := make([]int, 0, len(tiers))
	for i := range tiers {
		tier := &tiers[i]
		if tier.Id > 0 {
			var existing SubscriptionRPMTier
			if err := tx.Where("id = ? AND plan_id = ?", tier.Id, planId).First(&existing).Error; err != nil {
				return err
			}
		}
		tier.PlanId = planId
		if err := tx.Save(tier).Error; err != nil {
			return err
		}
		ids = append(ids, tier.Id)
	}
	query := tx.Where("plan_id = ?", planId)
	if len(ids) > 0 {
		query = query.Where("id NOT IN ?", ids)
	}
	return query.Delete(&SubscriptionRPMTier{}).Error
}

func (s *UserSubscription) AfterFind(tx *gorm.DB) error {
	s.TotalRPM = s.BaseRPM + s.AddedRPM
	return nil
}

// GetSubscriptionRPMPurchase reads live configuration; clients supply identifiers only.
func GetSubscriptionRPMPurchase(userId, subscriptionId, tierId int) (*SubscriptionPlan, *SubscriptionRPMTier, error) {
	var sub UserSubscription
	if err := DB.Where("id = ? AND user_id = ? AND status = ? AND start_time <= ? AND end_time > ?", subscriptionId, userId, "active", GetDBTimestamp(), GetDBTimestamp()).First(&sub).Error; err != nil {
		return nil, nil, errors.New("订阅不存在或已失效")
	}
	var tier SubscriptionRPMTier
	if err := DB.Where("id = ? AND plan_id = ? AND enabled = ?", tierId, sub.PlanId, true).First(&tier).Error; err != nil {
		return nil, nil, errors.New("RPM 加购档位不存在或未启用")
	}
	var plan SubscriptionPlan
	if err := DB.First(&plan, sub.PlanId).Error; err != nil {
		return nil, nil, err
	}
	plan.NormalizeDefaults()
	if !plan.Enabled {
		return nil, nil, errors.New("套餐未启用")
	}
	if err := (&SubscriptionPlan{RPMTiers: []SubscriptionRPMTier{tier}}).ValidateRPM(); err != nil {
		return nil, nil, err
	}
	if sub.BaseRPM < 1 || sub.AddedRPM < 0 || tier.RPM > MaxSubscriptionRPM-sub.BaseRPM-sub.AddedRPM {
		return nil, nil, errors.New("加购后 RPM 超过上限")
	}
	return &plan, &tier, nil
}

func completeSubscriptionRPMOrderTx(tx *gorm.DB, order *SubscriptionOrder, payload, method string) error {
	if order.RPMIncrement < 1 || order.RPMIncrement > MaxSubscriptionRPM {
		return errors.New("invalid RPM increment")
	}
	var sub UserSubscription
	err := lockForUpdate(tx).Where("id = ? AND user_id = ? AND plan_id = ?", order.UserSubscriptionId, order.UserId, order.PlanId).First(&sub).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	order.Status = common.TopUpStatusSuccess
	if err != nil || sub.Status != "active" || sub.StartTime > getDBTimestampTx(tx) || sub.EndTime <= getDBTimestampTx(tx) || sub.BaseRPM < 1 || sub.AddedRPM < 0 || order.RPMIncrement > MaxSubscriptionRPM-sub.BaseRPM-sub.AddedRPM {
		order.Status = SubscriptionOrderRefundPending
	} else {
		result := tx.Model(&UserSubscription{}).Where("id = ? AND added_rpm = ? AND status = ? AND end_time > ?", sub.Id, sub.AddedRPM, "active", getDBTimestampTx(tx)).Update("added_rpm", gorm.Expr("added_rpm + ?", order.RPMIncrement))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("subscription changed during RPM purchase")
		}
	}
	if err := upsertSubscriptionTopUpTx(tx, order); err != nil {
		return err
	}
	order.CompleteTime = common.GetTimestamp()
	order.ProviderPayload = payload
	if method != "" {
		order.PaymentMethod = method
	}
	return tx.Save(order).Error
}

// PurchaseSubscriptionRPMWithBalance charges and grants in one transaction.
func PurchaseSubscriptionRPMWithBalance(userId, subscriptionId, tierId int) error {
	plan, tier, err := GetSubscriptionRPMPurchase(userId, subscriptionId, tierId)
	if err != nil {
		return err
	}
	if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
		return errors.New("该套餐不允许使用余额兑换")
	}
	chargedQuota := 0
	err = DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userId).Error; err != nil {
			return err
		}
		required, err := calcSubscriptionBalanceQuota(tier.PriceAmount)
		if err != nil {
			return err
		}
		if required <= 0 || user.Quota < required {
			return errors.New("余额不足")
		}
		chargedQuota = required
		result := tx.Model(&User{}).Where("id = ? AND quota >= ?", userId, required).Update("quota", gorm.Expr("quota - ?", required))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("余额不足")
		}
		order := SubscriptionOrder{UserId: userId, PlanId: plan.Id, Purpose: SubscriptionOrderRPM, UserSubscriptionId: subscriptionId, RPMTierId: tier.Id, RPMIncrement: tier.RPM, Money: tier.PriceAmount, TradeNo: "SUBRPMBAL-" + common.GetUUID(), PaymentMethod: PaymentMethodBalance, PaymentProvider: PaymentProviderBalance, Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp()}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		if err := completeSubscriptionRPMOrderTx(tx, &order, "balance", PaymentMethodBalance); err != nil {
			return err
		}
		if order.Status != common.TopUpStatusSuccess {
			return errors.New("订阅已失效或 RPM 超过上限")
		}
		return nil
	})
	if err == nil {
		if cacheErr := cacheDecrUserQuota(userId, int64(chargedQuota)); cacheErr != nil {
			common.SysError(cacheErr.Error())
		}
		RecordLog(userId, LogTypeTopup, fmt.Sprintf("订阅 RPM 加购成功，订阅 ID: %d，增加 %d RPM，金额 %.2f", subscriptionId, tier.RPM, tier.PriceAmount))
	}
	return err
}
