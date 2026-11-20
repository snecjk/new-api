import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { describe, it } from 'node:test'

import type { SubscriptionPlan } from '../../types'
import { subscriptionBillingGroupLabel } from '../billing-group'
import {
  formValuesToPlanPayload,
  getPlanFormSchema,
  PLAN_FORM_DEFAULTS,
  planToFormValues,
} from '../plan-form'

describe('subscription plan billing group', () => {
  it('includes a restriction for a new plan without changing the upgrade group', () => {
    const values = {
      ...PLAN_FORM_DEFAULTS,
      title: 'Day pass',
      billing_group: 'default',
      upgrade_group: 'gpt-stable',
    }
    const payload = formValuesToPlanPayload(values).plan
    assert.equal(payload.billing_group, 'default')
    assert.equal(payload.upgrade_group, 'gpt-stable')
  })

  it('keeps the selected billing group through edit and submit', () => {
    const plan = {
      ...PLAN_FORM_DEFAULTS,
      id: 4,
      currency: 'USD',
      title: 'Day pass',
      billing_group: 'default',
      upgrade_group: '',
    } as SubscriptionPlan

    const values = planToFormValues(plan)
    assert.equal(values.billing_group, 'default')
    assert.equal(formValuesToPlanPayload(values).plan.billing_group, 'default')
  })

  it('keeps an unrestricted legacy plan unrestricted', () => {
    const values = planToFormValues({
      ...PLAN_FORM_DEFAULTS,
      id: 5,
      title: 'Legacy',
    } as SubscriptionPlan)
    assert.equal(values.billing_group, '')
    assert.equal(formValuesToPlanPayload(values).plan.billing_group, '')
    assert.equal(
      getPlanFormSchema(
        ((key: string) => key) as Parameters<typeof getPlanFormSchema>[0]
      ).safeParse(values).success,
      true
    )
  })

  it('renders unrestricted and default-group plans distinctly for admin and buyer views', () => {
    const t = (key: string): string => key
    assert.equal(subscriptionBillingGroupLabel('', t), 'All Groups')
    assert.equal(subscriptionBillingGroupLabel(undefined, t), 'All Groups')
    assert.equal(subscriptionBillingGroupLabel('default', t), 'default')
    assert.equal(
      `Subscription Billing Group: ${subscriptionBillingGroupLabel('default', t)}`,
      'Subscription Billing Group: default'
    )
  })

  it('has buyer-facing group wording in every supported locale', () => {
    for (const locale of ['en', 'zh', 'zh-TW', 'fr', 'ru', 'ja', 'vi']) {
      const path = new URL(
        `../../../../i18n/locales/${locale}.json`,
        import.meta.url
      )
      const catalog = JSON.parse(readFileSync(path, 'utf8')) as {
        translation: Record<string, string>
      }
      for (const key of [
        'Subscription Billing Group',
        'All Groups',
        'Limits subscription billing to this group, not API key access.',
      ]) {
        assert.ok(catalog.translation[key], `${locale} is missing ${key}`)
      }
    }
  })
})
