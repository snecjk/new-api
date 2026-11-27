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
import { test } from 'node:test'

import type { TFunction } from 'i18next'

import type { SubscriptionPlan } from '../../types'
import {
  formValuesToPlanPayload,
  getPlanFormSchema,
  PLAN_FORM_DEFAULTS,
  planToFormValues,
} from '../plan-form'

const t = ((key: string) => key) as TFunction

test('new plans default to 5 RPM and saved tiers retain prices and identity', () => {
  assert.equal(PLAN_FORM_DEFAULTS.rpm_limit, 5)
  const values = {
    ...PLAN_FORM_DEFAULTS,
    title: 'RPM',
    rpm_limit: 10,
    rpm_tiers: [{ id: 7, rpm: 5, price_amount: 2, enabled: false }],
  }
  const parsed = getPlanFormSchema(t).parse(values)
  const payload = formValuesToPlanPayload(parsed)
  const restored = planToFormValues({
    id: 1,
    ...payload.plan,
  } as SubscriptionPlan)
  assert.equal(restored.rpm_limit, 10)
  assert.deepEqual(restored.rpm_tiers, values.rpm_tiers)
})

test('RPM config rejects fractional limits and invalid tier prices', () => {
  const schema = getPlanFormSchema(t)
  for (const rpm_limit of [0, -1, 1.5, 1000001]) {
    assert.equal(
      schema.safeParse({ ...PLAN_FORM_DEFAULTS, title: 'RPM', rpm_limit })
        .success,
      false
    )
  }
  for (const price_amount of [0, -1, 10000, Infinity, Number.NaN]) {
    assert.equal(
      schema.safeParse({
        ...PLAN_FORM_DEFAULTS,
        title: 'RPM',
        rpm_tiers: [{ rpm: 5, price_amount, enabled: true }],
      }).success,
      false
    )
  }
})
