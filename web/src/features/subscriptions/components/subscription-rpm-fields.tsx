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
import { useFieldArray, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { SideDrawerSection } from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import type { PlanFormValues } from '../lib/plan-form'

export function SubscriptionRPMFields({
  form,
}: {
  form: UseFormReturn<PlanFormValues>
}) {
  const { t } = useTranslation()
  const { fields, append, remove } = useFieldArray({
    control: form.control,
    name: 'rpm_tiers',
    keyName: 'fieldKey',
  })
  return (
    <SideDrawerSection>
      <h3 className='text-sm font-medium'>{t('RPM Limit')}</h3>
      <FormField
        control={form.control}
        name='rpm_limit'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Base RPM')}</FormLabel>
            <FormControl>
              <Input {...field} type='number' min={1} max={1000000} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      <h3 className='text-sm font-medium'>{t('RPM Add-ons')}</h3>
      {fields.map((tier, index) => (
        <div key={tier.fieldKey} className='space-y-3 rounded-md border p-3'>
          <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name={`rpm_tiers.${index}.rpm` as 'rpm_tiers.0.rpm'}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Additional RPM')}</FormLabel>
                  <FormControl>
                    <Input {...field} type='number' min={1} max={1000000} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name={
                `rpm_tiers.${index}.price_amount` as 'rpm_tiers.0.price_amount'
              }
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Price')} (USD)</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='number'
                      min={0.01}
                      max={9999}
                      step={0.01}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
          {(
            [
              'stripe_price_id',
              'creem_product_id',
              'waffo_pancake_product_id',
            ] as const
          ).map((name) => {
            const labels = {
              stripe_price_id: 'Stripe Price ID',
              creem_product_id: 'Creem Product ID',
              waffo_pancake_product_id: 'Waffo Pancake Product ID',
            }
            return (
              <FormField
                key={name}
                control={form.control}
                name={
                  `rpm_tiers.${index}.${name}` as 'rpm_tiers.0.stripe_price_id'
                }
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{labels[name]}</FormLabel>
                    <FormControl>
                      <Input {...field} value={field.value ?? ''} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )
          })}
          <div className='flex items-center justify-between gap-2'>
            <FormField
              control={form.control}
              name={`rpm_tiers.${index}.enabled` as 'rpm_tiers.0.enabled'}
              render={({ field }) => (
                <FormItem className='flex items-center gap-2'>
                  <FormLabel>{t('Enable')}</FormLabel>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </FormItem>
              )}
            />
            <Button
              type='button'
              variant='outline'
              onClick={() => remove(index)}
            >
              {t('Remove RPM Tier')}
            </Button>
          </div>
        </div>
      ))}
      <Button
        type='button'
        variant='outline'
        disabled={fields.length >= 50}
        onClick={() => append({ rpm: 5, price_amount: 1, enabled: true })}
      >
        {t('Add RPM Tier')}
      </Button>
      <p className='text-muted-foreground text-xs'>
        {t('RPM add-ons expire with this subscription.')}
      </p>
    </SideDrawerSection>
  )
}
