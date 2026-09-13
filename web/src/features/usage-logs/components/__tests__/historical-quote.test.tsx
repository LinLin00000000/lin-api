import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { usageLogSchema } from '../../data/schema'
import { DetailsDialog } from '../dialogs/details-dialog'

const quote = () => ({
  ratios: {
    revision: 42,
    identity: 'Friend',
    service: 'Pro',
    physical_model: 'physical-old',
    service_factor: { value: 2, source: 'service_models/Pro/physical-old' },
    identity_factor: { value: 0.25, source: 'identity_defaults/Friend' },
  },
  billing_model: 'billing-old',
  quota_per_unit: 500000,
  rounding: 'half-away',
  components: {
    input: {
      base: {
        value: 3,
        unit: 'quota/token',
        source: 'existing synchronous calculator/input',
      },
      effective: 1.5,
    },
  },
})
let client: QueryClient
beforeEach(async () => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], {})
  client.setQueryData(['pricing', 'public'], {
    data: [{ model_name: 'physical-old', model_ratio: 99 }],
    vendors: [],
    group_ratio: { Pro: 88 },
  })
  i18next.addResourceBundle('en', 'translation', en.translation, true, true)
  i18next.addResourceBundle('zh', 'translation', zh.translation, true, true)
  await i18next.changeLanguage('en')
})
afterEach(() => client.clear())
function Harness(props: { other: object }) {
  const [open, setOpen] = useState(false)
  const log = usageLogSchema.parse({
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    model_name: 'physical-old',
    quota: 250,
    other: JSON.stringify(props.other),
  })
  return (
    <>
      <button type='button' onClick={() => setOpen(true)}>
        Open log
      </button>
      <DetailsDialog
        log={log}
        isAdmin={false}
        isRoot={false}
        open={open}
        onOpenChange={setOpen}
      />
    </>
  )
}
async function openLog(other: object) {
  render(
    <QueryClientProvider client={client}>
      <Harness other={other} />
    </QueryClientProvider>
  )
  await userEvent.click(screen.getByRole('button', { name: 'Open log' }))
  return within(
    await screen.findByRole('region', { name: 'Frozen billing quote' })
  )
}
describe('historical quote in the real DetailsDialog', () => {
  it('opens frozen fields and preserves them across current pricing changes and reopen', async () => {
    let section = await openLog({ identity_billing_quote: quote() })
    for (const value of [
      'Friend',
      'Pro',
      '42',
      'physical-old',
      'billing-old',
      '3',
      '1.5',
      'quota/token',
      'service_models/Pro/physical-old',
      'identity_defaults/Friend',
    ]) {
      expect(section.getByText(value)).toBeVisible()
    }
    await act(async () => {
      client.setQueryData(['pricing', 'public'], {
        data: [{ model_name: 'physical-old', model_ratio: 900 }],
        vendors: [],
        group_ratio: { Pro: 800 },
      })
    })
    expect(section.getByText('1.5')).toBeVisible()
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Open log' }))
    section = within(
      await screen.findByRole('region', { name: 'Frozen billing quote' })
    )
    expect(section.getByText('1.5')).toBeVisible()
    expect(section.queryByText('900')).not.toBeInTheDocument()
  })
  it('shows frozen prices with no current catalog and never requests unavailable pricing', async () => {
    client.removeQueries({ queryKey: ['pricing'] })
    const request = vi
      .spyOn(api, 'get')
      .mockRejectedValue(new Error('pricing unavailable'))
    const section = await openLog({
      identity_billing_quote: quote(),
      billing_mode: 'tiered_expr',
      expr_b64: btoa('input * 999'),
    })
    expect(section.getByText('3')).toBeVisible()
    expect(section.getByText('2')).toBeVisible()
    expect(section.getByText('0.25')).toBeVisible()
    expect(section.getByText('1.5')).toBeVisible()
    expect(section.getByText('42')).toBeVisible()
    expect(request).not.toHaveBeenCalled()
    expect(client.getQueryData(['pricing', 'public'])).toBeUndefined()
    expect(screen.queryByText('Dynamic Pricing')).not.toBeInTheDocument()
  })
  it.each(['missing', 'populated'])(
    'reads no pricing cache for a frozen tiered log with %s pricing, including reopen after catalog changes',
    async (catalog) => {
      if (catalog === 'missing') client.removeQueries({ queryKey: ['pricing'] })
      const cacheRead = vi.spyOn(client.getQueryCache(), 'get')
      const cacheBuild = vi.spyOn(client.getQueryCache(), 'build')
      const request = vi
        .spyOn(api, 'get')
        .mockRejectedValue(new Error('pricing unavailable'))
      const section = await openLog({
        identity_billing_quote: quote(),
        billing_mode: 'tiered_expr',
        expr_b64: btoa('input * 999'),
      })
      const expectNoPricingAccess = () => {
        expect(
          cacheRead.mock.calls.filter(
            ([hash]) => JSON.parse(hash)[0] === 'pricing'
          )
        ).toHaveLength(0)
        expect(
          cacheBuild.mock.calls.filter(
            ([, options]) => options.queryKey?.[0] === 'pricing'
          )
        ).toHaveLength(0)
        expect(request).not.toHaveBeenCalled()
      }
      expectNoPricingAccess()
      for (const value of [
        '3',
        '2',
        '0.25',
        '1.5',
        '42',
        'quota/token',
        'identity_defaults/Friend',
      ]) {
        expect(section.getByText(value)).toBeVisible()
      }
      expect(screen.getByText('input * 999')).toBeVisible()
      await userEvent.keyboard('{Escape}')
      await act(async () => {
        client.setQueryData(['pricing', 'public'], {
          data: [
            {
              model_name: 'physical-old',
              model_ratio: 900,
              billing_usage_schema: { invalid: 'changed-current-schema' },
            },
          ],
          vendors: [],
          group_ratio: { Pro: 800 },
        })
        // Exclude the test's deliberate cache write; measure the consumer only.
        cacheRead.mockClear()
        cacheBuild.mockClear()
      })
      await userEvent.click(screen.getByRole('button', { name: 'Open log' }))
      const reopened = within(
        await screen.findByRole('region', { name: 'Frozen billing quote' })
      )
      expect(reopened.getByText('1.5')).toBeVisible()
      expect(reopened.getByText('42')).toBeVisible()
      expect(screen.getByText('input * 999')).toBeVisible()
      expect(screen.queryByText('Dynamic Pricing')).not.toBeInTheDocument()
      expectNoPricingAccess()
    }
  )
  it('preserves explicit zero factors, base and effective values', async () => {
    const q = quote()
    q.ratios.service_factor.value = 0
    q.ratios.identity_factor.value = 0
    q.components.input.base.value = 0
    q.components.input.effective = 0
    const section = await openLog({ identity_billing_quote: q })
    expect(section.getAllByText('0')).toHaveLength(4)
    expect(
      section.queryByText('Unknown — not a free price')
    ).not.toBeInTheDocument()
  })
  it('keeps legacy billing details but does not invent a missing frozen quote', async () => {
    const section = await openLog({ model_price: 2, group_ratio: 0 })
    expect(section.getByText('Unknown — not a free price')).toBeVisible()
    expect(screen.getByText('Billing Details')).toBeVisible()
    expect(screen.getByText('0.0000x')).toBeVisible()
  })
  it('keeps legacy custom expressions readable without borrowing a current usage schema', async () => {
    const request = vi
      .spyOn(api, 'get')
      .mockRejectedValue(new Error('pricing unavailable'))
    const cacheRead = vi.spyOn(client.getQueryCache(), 'get')
    const section = await openLog({
      billing_mode: 'tiered_expr',
      expr_b64: btoa('u.video_seconds * 7'),
    })
    expect(section.getByText('Unknown — not a free price')).toBeVisible()
    expect(screen.getByText('Billing Details')).toBeVisible()
    expect(screen.getByText('Raw expression')).toBeVisible()
    expect(screen.getByText('u.video_seconds * 7')).toBeVisible()
    expect(
      cacheRead.mock.calls.filter(([hash]) => JSON.parse(hash)[0] === 'pricing')
    ).toHaveLength(0)
    expect(request).not.toHaveBeenCalled()
  })
  it.each([
    null,
    [],
    'broken',
    {},
    { ...quote(), components: null },
    {
      ...quote(),
      ratios: { ...quote().ratios, service_factor: { source: 'missing' } },
    },
    {
      ...quote(),
      ratios: { ...quote().ratios, revision: Number.MAX_SAFE_INTEGER + 1 },
    },
    {
      ...quote(),
      components: {
        input: { ...quote().components.input, effective: Infinity },
      },
    },
    {
      ...quote(),
      components: {
        input: {
          base: { value: -1, unit: 'quota/token', source: 'old' },
          effective: 0,
        },
      },
    },
    {
      ...quote(),
      components: {
        input: {
          base: { value: '0', unit: 'quota/token', source: 'old' },
          effective: 0,
        },
      },
    },
  ])('renders malformed snapshot safely as unknown (%j)', async (value) => {
    const section = await openLog({ identity_billing_quote: value })
    expect(section.getByText('Unknown — not a free price')).toBeVisible()
    expect(section.queryByText('0')).not.toBeInTheDocument()
    expect(screen.queryByText('Billing Details')).not.toBeInTheDocument()
  })
  it('shows frozen expression information, not recalculated dynamic totals', async () => {
    const q = {
      ...quote(),
      components: {
        expression_estimate: {
          base: {
            value: 12,
            unit: 'quota/request estimate; expression stored in TieredBillingSnapshot',
            source: 'existing synchronous calculator/expression_estimate',
          },
          effective: 6,
        },
      },
    }
    const section = await openLog({
      identity_billing_quote: q,
      billing_mode: 'tiered_expr',
      expr_b64: btoa('input * 77'),
    })
    expect(section.getByText('6')).toBeVisible()
    expect(
      section.getByText(
        'Frozen estimates and multipliers are not final charges.'
      )
    ).toBeVisible()
    expect(screen.getByText('input * 77')).toBeVisible()
    expect(screen.queryByText('Billing Details')).not.toBeInTheDocument()
    expect(screen.queryByText('Dynamic Pricing')).not.toBeInTheDocument()
  })
  it('updates labels when the language changes without changing frozen identifiers', async () => {
    await openLog({ identity_billing_quote: quote() })
    await act(async () => {
      await i18next.changeLanguage('zh')
    })
    const section = within(screen.getByRole('region', { name: '冻结账价' }))
    expect(section.getByText('基础价 B')).toBeVisible()
    expect(section.getByText('Friend')).toBeVisible()
    expect(section.getByText('1.5')).toBeVisible()
  })
})
