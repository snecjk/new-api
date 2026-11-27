package controller

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/model"
)

// resolveSubscriptionCheckout reuses checkout providers with a server-priced RPM product.
func resolveSubscriptionCheckout(userId, planId, subscriptionId, tierId int) (*model.SubscriptionPlan, *model.SubscriptionRPMTier, error) {
	if tierId < 0 || subscriptionId < 0 || (tierId == 0 && subscriptionId != 0) {
		return nil, nil, errors.New("invalid RPM purchase identifiers")
	}
	if tierId == 0 {
		plan, err := model.GetSubscriptionPlanById(planId)
		return plan, nil, err
	}
	plan, tier, err := model.GetSubscriptionRPMPurchase(userId, subscriptionId, tierId)
	if err != nil {
		return nil, nil, err
	}
	plan.Title = fmt.Sprintf("%s +%d RPM", plan.Title, tier.RPM)
	plan.PriceAmount = tier.PriceAmount
	plan.MaxPurchasePerUser = 0
	plan.StripePriceId = tier.StripePriceId
	plan.CreemProductId = tier.CreemProductId
	plan.WaffoPancakeProductId = tier.WaffoPancakeProductId
	return plan, tier, nil
}

func setSubscriptionRPMOrder(order *model.SubscriptionOrder, subscriptionId int, tier *model.SubscriptionRPMTier) {
	if tier == nil {
		return
	}
	order.Purpose = model.SubscriptionOrderRPM
	order.UserSubscriptionId = subscriptionId
	order.RPMTierId = tier.Id
	order.RPMIncrement = tier.RPM
}
