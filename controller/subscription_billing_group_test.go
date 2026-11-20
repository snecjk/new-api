package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubscriptionPlanBillingGroupAdminValidationAndEdit(t *testing.T) {
	oldDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}))

	payment := operation_setting.GetPaymentSetting()
	oldPayment := *payment
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() { *payment = oldPayment })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/plans", AdminCreateSubscriptionPlan)
	router.PUT("/plans/:id", AdminUpdateSubscriptionPlan)
	send := func(method, path, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code)
		var response map[string]any
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		return response
	}

	invalidCreate := send(http.MethodPost, "/plans", `{"plan":{"title":"Day","billing_group":"missing"}}`)
	assert.Equal(t, false, invalidCreate["success"])
	var count int64
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Count(&count).Error)
	assert.Zero(t, count)

	created := send(http.MethodPost, "/plans", `{"plan":{"title":"Day","billing_group":"default"}}`)
	require.Equal(t, true, created["success"])
	var plan model.SubscriptionPlan
	require.NoError(t, db.First(&plan).Error)
	assert.Equal(t, "default", plan.BillingGroup)
	_, err = model.GetSubscriptionPlanById(plan.Id)
	require.NoError(t, err)

	path := "/plans/" + strconv.Itoa(plan.Id)
	invalidEdit := send(http.MethodPut, path, `{"plan":{"title":"Day","billing_group":"missing"}}`)
	assert.Equal(t, false, invalidEdit["success"])
	require.NoError(t, db.First(&plan, plan.Id).Error)
	assert.Equal(t, "default", plan.BillingGroup)

	omitted := send(http.MethodPut, path, `{"plan":{"title":"Renamed"}}`)
	assert.Equal(t, true, omitted["success"])
	cached, err := model.GetSubscriptionPlanById(plan.Id)
	require.NoError(t, err)
	assert.Equal(t, "default", cached.BillingGroup)
	assert.Equal(t, "Renamed", cached.Title)

	cleared := send(http.MethodPut, path, `{"plan":{"title":"Renamed","billing_group":""}}`)
	assert.Equal(t, true, cleared["success"])
	cached, err = model.GetSubscriptionPlanById(plan.Id)
	require.NoError(t, err)
	assert.Empty(t, cached.BillingGroup)
}
