package model

import (
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSubscriptionBillingGroupMigratesExistingPlans(t *testing.T) {
	cases := []struct {
		name   string
		open   func() (*gorm.DB, error)
		sqlite bool
	}{
		{name: "sqlite", sqlite: true, open: func() (*gorm.DB, error) { return gorm.Open(sqlite.Open(":memory:"), &gorm.Config{}) }},
	}
	if dsn := os.Getenv("MIGRATION_MYSQL_DSN"); dsn != "" {
		cases = append(cases, struct {
			name   string
			open   func() (*gorm.DB, error)
			sqlite bool
		}{name: "mysql", open: func() (*gorm.DB, error) { return gorm.Open(mysql.Open(dsn), &gorm.Config{}) }})
	}
	if dsn := os.Getenv("MIGRATION_POSTGRES_DSN"); dsn != "" {
		cases = append(cases, struct {
			name   string
			open   func() (*gorm.DB, error)
			sqlite bool
		}{name: "postgres", open: func() (*gorm.DB, error) { return gorm.Open(postgres.Open(dsn), &gorm.Config{}) }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := tc.open()
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.Migrator().DropTable(&SubscriptionPlan{}))
			require.NoError(t, db.AutoMigrate(&SubscriptionPlan{}))
			if tc.sqlite {
				require.NoError(t, db.Exec("ALTER TABLE subscription_plans DROP COLUMN billing_group").Error)
			} else {
				require.NoError(t, db.Migrator().DropColumn(&SubscriptionPlan{}, "billing_group"))
			}
			require.NoError(t, db.Exec("INSERT INTO subscription_plans (id, title, price_amount, currency, duration_unit, duration_value, total_amount) VALUES (?, ?, ?, ?, ?, ?, ?)", 1001, "Legacy pass", 1, "USD", "day", 1, 0).Error)
			if tc.sqlite {
				oldDB := DB
				DB = db
				t.Cleanup(func() { DB = oldDB })
				require.NoError(t, ensureSubscriptionPlanTableSQLite())
			}
			if !tc.sqlite {
				require.NoError(t, db.AutoMigrate(&SubscriptionPlan{}))
			}
			assert.True(t, db.Migrator().HasColumn(&SubscriptionPlan{}, "billing_group"))
			var plan SubscriptionPlan
			require.NoError(t, db.First(&plan, 1001).Error)
			assert.Empty(t, plan.BillingGroup)
			require.NoError(t, db.Model(&plan).Update("billing_group", "default").Error)
			require.NoError(t, db.First(&plan, 1001).Error)
			assert.Equal(t, "default", plan.BillingGroup)
			require.NoError(t, db.Migrator().DropTable(&SubscriptionPlan{}))
		})
	}
}
