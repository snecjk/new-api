/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { after, test } from 'node:test'

import { Window } from 'happy-dom'

import type { PlanRecord, UserSubscription } from '../../types'

const dom = new Window({ url: 'http://localhost/' })
for (const key of [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLInputElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'localStorage',
  'sessionStorage',
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: dom[key],
  })
}
const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { SubscriptionRPMPurchase } = await import('../subscription-rpm-purchase')
const { api } = await import('@/lib/api')
const i18n = createInstance()
await i18n
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })
;(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
).IS_REACT_ACT_ENVIRONMENT = true
after(() => dom.close())

const plan: PlanRecord = {
  plan: {
    id: 1,
    title: 'RPM plan',
    price_amount: 20,
    currency: 'USD',
    duration_unit: 'month',
    duration_value: 1,
    quota_reset_period: 'never',
    enabled: true,
    sort_order: 0,
    allow_balance_pay: true,
    allow_wallet_overflow: true,
    max_purchase_per_user: 0,
    total_amount: 100,
    rpm_limit: 5,
    rpm_tiers: [
      { id: 7, rpm: 10, price_amount: 2, enabled: true },
      { id: 8, rpm: 50, price_amount: 9, enabled: false },
    ],
  },
}
const subscription: UserSubscription = {
  id: 3,
  user_id: 1,
  plan_id: 1,
  status: 'active',
  start_time: 1,
  end_time: 4102444800,
  amount_total: 100,
  amount_used: 0,
  base_rpm: 5,
  added_rpm: 10,
  total_rpm: 15,
}

test('active subscriptions confirm tier price and send only bound IDs before refreshing rights', async () => {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const adapter = api.defaults.adapter
  let payload: unknown
  let refreshed = false
  api.defaults.adapter = async (config) => {
    assert.equal(config.url, '/api/subscription/balance/pay')
    payload = JSON.parse(config.data as string)
    return {
      config,
      data: { success: true },
      status: 200,
      statusText: 'OK',
      headers: {},
    }
  }
  try {
    await act(async () =>
      root.render(
        <I18nextProvider i18n={i18n}>
          <SubscriptionRPMPurchase
            plan={plan}
            subscription={subscription}
            userQuota={10000000}
            onPurchaseSuccess={() => {
              refreshed = true
            }}
          />
        </I18nextProvider>
      )
    )
    assert.match(container.textContent ?? '', /Total RPM: 15/)
    assert.match(container.textContent ?? '', /10 RPM · \$2.00/)
    assert.doesNotMatch(container.textContent ?? '', /50 RPM/)
    const buy = [...container.querySelectorAll('button')].find(
      (button) => button.textContent === 'Buy RPM'
    )
    assert.ok(buy)
    await act(async () => buy.click())
    const dialog = document.querySelector('[role="dialog"]')
    assert.ok(dialog)
    assert.match(
      dialog.textContent ?? '',
      /RPM add-ons expire with this subscription/
    )
    assert.match(dialog.textContent ?? '', /\$2.00/)
    const pay = [...dialog.querySelectorAll('button')].find(
      (button) => button.textContent === 'Pay with Balance'
    )
    assert.ok(pay)
    await act(async () => pay.click())
    assert.deepEqual(payload, {
      plan_id: 1,
      subscription_id: 3,
      rpm_tier_id: 7,
    })
    assert.equal(refreshed, true)
  } finally {
    api.defaults.adapter = adapter
    await act(async () => root.unmount())
    container.remove()
  }
})

test('expired or cancelled subscriptions show rights without a purchase button', async () => {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  try {
    for (const sub of [
      { ...subscription, end_time: 1 },
      { ...subscription, status: 'cancelled' },
    ]) {
      await act(async () =>
        root.render(
          <I18nextProvider i18n={i18n}>
            <SubscriptionRPMPurchase plan={plan} subscription={sub} />
          </I18nextProvider>
        )
      )
      assert.match(container.textContent ?? '', /Total RPM: 15/)
      assert.equal(container.querySelector('button'), null)
    }
  } finally {
    await act(async () => root.unmount())
    container.remove()
  }
})

const { useForm } = await import('react-hook-form')
const { Form } = await import('@/components/ui/form')
const { SubscriptionRPMFields } = await import('../subscription-rpm-fields')
const { PLAN_FORM_DEFAULTS } = await import('../../lib/plan-form')

test('admin can add, disable, save and remove a priced RPM tier with a default base of 5', async () => {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  let saved: typeof PLAN_FORM_DEFAULTS | undefined
  function AdminRPMForm() {
    const form = useForm({
      defaultValues: { ...PLAN_FORM_DEFAULTS, title: 'Admin RPM' },
    })
    return (
      <Form {...form}>
        <form
          onSubmit={form.handleSubmit((values) => {
            saved = values
          })}
        >
          <SubscriptionRPMFields form={form} />
          <button type='submit'>Save</button>
        </form>
      </Form>
    )
  }
  try {
    await act(async () =>
      root.render(
        <I18nextProvider i18n={i18n}>
          <AdminRPMForm />
        </I18nextProvider>
      )
    )
    const base = container.querySelector(
      'input[type="number"]'
    ) as HTMLInputElement
    assert.equal(base.value, '5')
    const add = [...container.querySelectorAll('button')].find(
      (button) => button.textContent === 'Add RPM Tier'
    )
    assert.ok(add)
    await act(async () => add.click())
    assert.equal(container.querySelectorAll('input[type="number"]').length, 3)
    const tierInput = container.querySelectorAll('input[type="number"]')[1]
    const responsiveGrid = tierInput.closest('[class~="sm:grid-cols-2"]')
    assert.ok(responsiveGrid)
    assert.equal(responsiveGrid.classList.contains('grid-cols-1'), true)
    assert.equal(responsiveGrid.classList.contains('sm:grid-cols-2'), true)
    const enabled =
      container.querySelector<HTMLButtonElement>('[role="switch"]')
    assert.ok(enabled)
    assert.equal(enabled.getAttribute('aria-checked'), 'true')
    await act(async () => enabled.click())
    assert.equal(enabled.getAttribute('aria-checked'), 'false')
    const save = [...container.querySelectorAll('button')].find(
      (button) => button.textContent === 'Save'
    )
    assert.ok(save)
    const formElement = container.querySelector('form')
    assert.ok(formElement)
    await act(async () => {
      formElement.dispatchEvent(
        new dom.Event('submit', {
          bubbles: true,
          cancelable: true,
        }) as unknown as Event
      )
    })
    assert.equal(saved?.rpm_limit, 5)
    assert.equal(saved?.rpm_tiers.length, 1)
    assert.equal(saved?.rpm_tiers[0].rpm, 5)
    assert.equal(saved?.rpm_tiers[0].price_amount, 1)
    assert.equal(saved?.rpm_tiers[0].enabled, false)
    const remove = [...container.querySelectorAll('button')].find(
      (button) => button.textContent === 'Remove RPM Tier'
    )
    assert.ok(remove)
    await act(async () => remove.click())
    await act(async () => {
      formElement.dispatchEvent(
        new dom.Event('submit', {
          bubbles: true,
          cancelable: true,
        }) as unknown as Event
      )
    })
    assert.deepEqual(saved?.rpm_tiers, [])
  } finally {
    await act(async () => root.unmount())
    container.remove()
  }
})
