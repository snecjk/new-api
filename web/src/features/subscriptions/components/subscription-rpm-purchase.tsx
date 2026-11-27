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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import type { PlanRecord, UserSubscription } from '../types'
import {
  SubscriptionPurchaseDialog,
  type SubscriptionPurchaseDialogProps,
} from './dialogs/subscription-purchase-dialog'

type Props = Pick<
  SubscriptionPurchaseDialogProps,
  | 'enableStripe'
  | 'enableCreem'
  | 'enableWaffoPancake'
  | 'enableOnlineTopUp'
  | 'epayMethods'
  | 'userQuota'
  | 'onPurchaseSuccess'
> & {
  plan?: PlanRecord
  subscription: UserSubscription
}

export function SubscriptionRPMPurchase({
  plan,
  subscription,
  ...paymentProps
}: Props) {
  const { t } = useTranslation()
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [open, setOpen] = useState(false)
  const tiers = (plan?.plan.rpm_tiers ?? []).filter(
    (tier) => tier.enabled && tier.id
  )
  const tier = tiers.find((item) => item.id === selectedId) ?? tiers[0]
  const now = Date.now() / 1000
  const canBuy =
    subscription.status === 'active' &&
    subscription.start_time <= now &&
    subscription.end_time > now &&
    !!tier &&
    plan?.plan.enabled
  const base = subscription.base_rpm ?? 5
  const added = subscription.added_rpm ?? 0
  let purchasePlan: PlanRecord | null = null
  if (plan && tier) {
    purchasePlan = {
      plan: {
        ...plan.plan,
        price_amount: tier.price_amount,
        stripe_price_id: tier.stripe_price_id,
        creem_product_id: tier.creem_product_id,
        waffo_pancake_product_id: tier.waffo_pancake_product_id,
      },
    }
  }
  return (
    <div className='mt-3 space-y-2 border-t pt-2'>
      <div className='text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs'>
        <span>
          {t('Base RPM')}: {base}
        </span>
        <span>
          {t('Additional RPM')}: {added}
        </span>
        <span className='text-foreground font-medium'>
          {t('Total RPM')}: {subscription.total_rpm ?? base + added}
        </span>
      </div>
      {canBuy && (
        <>
          <div className='flex flex-wrap gap-2'>
            <Select
              items={tiers.map((item) => ({
                value: String(item.id),
                label: `+${item.rpm} RPM · $${item.price_amount.toFixed(2)}`,
              }))}
              value={String(tier.id)}
              onValueChange={(value) => setSelectedId(Number(value))}
            >
              <SelectTrigger
                aria-label={t('RPM Add-ons')}
                className='min-w-0 flex-1'
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {tiers.map((item) => (
                  <SelectItem key={item.id} value={String(item.id)}>
                    +{item.rpm} RPM · ${item.price_amount.toFixed(2)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button size='sm' variant='outline' onClick={() => setOpen(true)}>
              {t('Buy RPM')}
            </Button>
          </div>
          <p className='text-muted-foreground text-xs'>
            {t('RPM add-ons expire with this subscription.')}
          </p>
          <SubscriptionPurchaseDialog
            {...paymentProps}
            open={open}
            onOpenChange={setOpen}
            plan={purchasePlan}
            rpmPurchase={{
              subscriptionId: subscription.id,
              tierId: tier.id ?? 0,
              rpm: tier.rpm,
              endTime: subscription.end_time,
            }}
          />
        </>
      )}
    </div>
  )
}
