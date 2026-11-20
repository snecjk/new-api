export function subscriptionBillingGroupLabel(
  group: string | null | undefined,
  t: (key: string) => string
): string {
  return group || t('All Groups')
}
