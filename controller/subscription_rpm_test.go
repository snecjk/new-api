package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubscriptionRPMAdminConfigurationAndPublicVisibility(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	oldPayment := *operation_setting.GetPaymentSetting()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}, &model.SubscriptionRPMTier{}, &model.UserSubscription{}, &model.User{}, &model.SubscriptionOrder{}, &model.TopUp{}, &model.Log{}))
	model.DB, model.LOG_DB = db, db
	operation_setting.GetPaymentSetting().ComplianceConfirmed = true
	operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.RedisEnabled = oldRedis
		*operation_setting.GetPaymentSetting() = oldPayment
		conn, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/plans", AdminCreateSubscriptionPlan)
	router.PUT("/plans/:id", AdminUpdateSubscriptionPlan)
	router.GET("/plans", GetSubscriptionPlans)
	router.GET("/self", func(c *gin.Context) { c.Set("id", 701); GetSubscriptionSelf(c) })
	router.POST("/balance", func(c *gin.Context) { c.Set("id", 701); SubscriptionRequestBalancePay(c) })
	router.POST("/protected", middleware.AdminAuth(), AdminCreateSubscriptionPlan)
	rejected := httptest.NewRecorder()
	router.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/protected", nil))
	assert.Equal(t, http.StatusUnauthorized, rejected.Code)
	plan := model.SubscriptionPlan{Title: "RPM API", Enabled: true, RPMTiers: []model.SubscriptionRPMTier{{RPM: 10, PriceAmount: 2, Enabled: true}, {RPM: 20, PriceAmount: 3, Enabled: false}}}
	data, err := common.Marshal(SubscriptionPlanDTO{Plan: plan})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/plans", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	var response struct {
		Success bool
		Data    model.SubscriptionPlan
		Message string
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success, response.Message)
	require.NotNil(t, response.Data.RPMLimit)
	assert.Equal(t, 5, *response.Data.RPMLimit)
	assert.Len(t, response.Data.RPMTiers, 2)
	public := httptest.NewRecorder()
	router.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/plans", nil))
	var listed struct {
		Success bool
		Data    []SubscriptionPlanDTO
	}
	require.NoError(t, common.Unmarshal(public.Body.Bytes(), &listed))
	require.True(t, listed.Success)
	require.Len(t, listed.Data, 1)
	require.Len(t, listed.Data[0].Plan.RPMTiers, 1)
	assert.Equal(t, 10, listed.Data[0].Plan.RPMTiers[0].RPM)

	user := model.User{Id: 701, Username: "rpm-api-user", Quota: 10000000}
	require.NoError(t, db.Create(&user).Error)
	sub, err := model.CreateUserSubscriptionFromPlanTx(db, user.Id, &response.Data, "admin")
	require.NoError(t, err)
	// Client prices and increments never override the configured tier.
	purchase := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/balance", bytes.NewBufferString(fmt.Sprintf("{\"subscription_id\":%d,\"rpm_tier_id\":%d,\"price_amount\":0,\"rpm\":999999}", sub.Id, response.Data.RPMTiers[0].Id)))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(purchase, request)
	var purchaseResult struct{ Success bool }
	require.NoError(t, common.Unmarshal(purchase.Body.Bytes(), &purchaseResult))
	require.True(t, purchaseResult.Success, purchase.Body.String())
	var order model.SubscriptionOrder
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&order).Error)
	assert.Equal(t, 2.0, order.Money)
	assert.Equal(t, 10, order.RPMIncrement)

	for name, handler := range map[string]gin.HandlerFunc{"stripe": SubscriptionRequestStripePay, "creem": SubscriptionRequestCreemPay, "waffo": SubscriptionRequestWaffoPancakePay, "epay": SubscriptionRequestEpay} {
		router.POST("/"+name, func(c *gin.Context) { c.Set("id", 702); handler(c) })
		rejected := httptest.NewRecorder()
		body, err := common.Marshal(map[string]interface{}{"plan_id": plan.Id, "subscription_id": sub.Id, "rpm_tier_id": response.Data.RPMTiers[0].Id})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/"+name, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rejected, req)
		assert.Contains(t, rejected.Body.String(), "订阅不存在或已失效", name)
	}
	var orderCount int64
	require.NoError(t, db.Model(&model.SubscriptionOrder{}).Count(&orderCount).Error)
	assert.EqualValues(t, 1, orderCount, "foreign subscriptions never create external orders")
	self := httptest.NewRecorder()
	router.ServeHTTP(self, httptest.NewRequest(http.MethodGet, "/self", nil))
	var selfData struct {
		Data struct {
			All []model.SubscriptionSummary `json:"all_subscriptions"`
		}
	}
	require.NoError(t, common.Unmarshal(self.Body.Bytes(), &selfData))
	require.Len(t, selfData.Data.All, 1)
	assert.Equal(t, 5, selfData.Data.All[0].Subscription.BaseRPM)
	assert.Equal(t, 10, selfData.Data.All[0].Subscription.AddedRPM)
	assert.Equal(t, 15, selfData.Data.All[0].Subscription.TotalRPM)
	// Disable the tier and change base RPM without changing purchased rights.
	plan = listed.Data[0].Plan
	plan.RPMLimit = common.GetPointer(20)
	plan.RPMTiers[0].Enabled = false
	data, err = common.Marshal(SubscriptionPlanDTO{Plan: plan})
	require.NoError(t, err)
	updated := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/plans/%d", plan.Id), bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(updated, request)
	var updateResult struct{ Success bool }
	require.NoError(t, common.Unmarshal(updated.Body.Bytes(), &updateResult))
	require.True(t, updateResult.Success, updated.Body.String())
	public = httptest.NewRecorder()
	router.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/plans", nil))
	require.NoError(t, common.Unmarshal(public.Body.Bytes(), &listed))
	assert.Empty(t, listed.Data[0].Plan.RPMTiers)
	assert.Equal(t, 20, *listed.Data[0].Plan.RPMLimit)
	var saved model.UserSubscription
	require.NoError(t, db.First(&saved, sub.Id).Error)
	assert.Equal(t, 5, saved.BaseRPM)
	plan.RPMLimit = common.GetPointer(0)
	data, err = common.Marshal(SubscriptionPlanDTO{Plan: plan})
	require.NoError(t, err)
	invalid := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/plans", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(invalid, request)
	var failure struct{ Success bool }
	require.NoError(t, common.Unmarshal(invalid.Body.Bytes(), &failure))
	assert.False(t, failure.Success)
}
