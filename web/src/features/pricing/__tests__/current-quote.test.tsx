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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { CurrentQuoteSection } from '../components/current-quote-section'

const user = { id: 12, username: 'ordinary', role: 1, group: 'ordinary' }
const makeQuote = (model = 'token', service = 'default', identity = 'ordinary', preview = false) => ({
  success: true, mode: 'identity_service', kind: 'current_quote', preview,
  data: { calculator: 'synchronous', final_price_known: false, conditions: [], quote: {
    ratios: { revision: 7, physical_model: model, service, identity, service_factor: { value: 2, source: 'service_defaults["default"]' }, identity_factor: { value: 0, source: 'identity_model_ratios["ordinary"]["token"]' } },
    billing_model: model, quota_per_unit: 500000, rounding: 'actual helper policy', components: { input: { base: { value: 4, unit: 'quota/token', source: 'existing synchronous calculator/input' }, effective: 0 } },
  } },
})
function wrap(node: React.ReactNode, client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return <QueryClientProvider client={client}>{node}</QueryClientProvider>
}
async function openQuote() { await userEvent.click(screen.getByRole('button', { name: 'Current identity/service quote' })) }

beforeEach(() => { useAuthStore.setState((state) => ({ auth: { ...state.auth, user } })) })
afterEach(() => { vi.restoreAllMocks(); useAuthStore.setState((state) => ({ auth: { ...state.auth, user: null } })) })

describe('current quote consumer', () => {
  it('selecting a service renders backend zero, base, source and revision without a frontend formula', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: makeQuote() })
    render(wrap(<CurrentQuoteSection model='token' services={['default', 'pro']} />))
    await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
    const row = await screen.findByRole('row', { name: /input 4 0 quota\/token/ })
    expect(within(row).getByText('0')).toBeInTheDocument()
    expect(screen.getByText('identity_model_ratios["ordinary"]["token"]')).toBeInTheDocument()
    expect(screen.getByText('7')).toBeInTheDocument()
    expect(get.mock.calls[0][1]?.params).toEqual({ model: 'token', service: 'default' })
    expect(screen.queryByLabelText('Preview identity')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Refresh current quote' }))
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
  })

  it('an API failure hides any previous quote and distinguishes unavailable from zero', async () => {
    vi.spyOn(api, 'get').mockResolvedValueOnce({ data: makeQuote() }).mockRejectedValueOnce(new Error('unavailable'))
    render(wrap(<CurrentQuoteSection model='token' services={['default', 'pro']} />))
    await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
    await screen.findByRole('table')
    await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'pro')
    expect(await screen.findByRole('alert')).toHaveTextContent('Unknown is not free')
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('a late result for the previous service or model cannot replace the current selection', async () => {
    let resolveOld!: (value: unknown) => void
    vi.spyOn(api, 'get').mockImplementation((_path, options) => {
      if (options?.params.service === 'default') return new Promise((resolve) => { resolveOld = resolve })
      return Promise.resolve({ data: makeQuote(options?.params.model, 'pro') })
    })
    const client = new QueryClient()
    const view = render(wrap(<CurrentQuoteSection model='token' services={['default', 'pro']} />, client))
    await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
    await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'pro')
    await screen.findByRole('table')
    view.rerender(wrap(<CurrentQuoteSection model='other' services={['default', 'pro']} />, client))
    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
    await act(async () => { resolveOld({ data: makeQuote('old', 'default') }) })
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Quote service')).toHaveValue('pro')
  })

  it('Root explicitly selects saved preview identity while ordinary users never fetch the configuration', async () => {
    useAuthStore.setState((state) => ({ auth: { ...state.auth, user: { ...user, role: ROLE.SUPER_ADMIN } } }))
    const get = vi.spyOn(api, 'get').mockImplementation((path, options) => {
      if (path === '/api/option/identity_service') return Promise.resolve({ data: { success: true, data: { version: 1, mode: 'legacy', revision: 7, service_defaults: { pro: 2 }, identity_defaults: { Friend: 0 } } } })
      return Promise.resolve({ data: makeQuote('token', options?.params.service, options?.params.identity, true) })
    })
    render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
    await openQuote(); expect(get).not.toHaveBeenCalled()
    await userEvent.click(screen.getByLabelText('Preview saved identity/service configuration'))
    await screen.findByRole('option', { name: 'Friend' })
    await userEvent.selectOptions(screen.getByLabelText('Preview identity'), 'Friend')
    await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'pro')
    await screen.findByText('Saved configuration preview only. Does not enable billing or grant access.')
    expect(get).toHaveBeenLastCalledWith('/api/option/identity_service/quote', expect.objectContaining({ params: { model: 'token', service: 'pro', identity: 'Friend' } }))
    act(() => { useAuthStore.setState((state) => ({ auth: { ...state.auth, user } })) })
    expect(screen.queryByLabelText('Preview identity')).not.toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('expression quotes show conditional unknown totals instead of invented input prices', async () => {
    const response = { ...makeQuote(), data: { ...makeQuote().data, calculator: 'expression', expression: 'p * 4 + c * 8' } }
    vi.spyOn(api, 'get').mockResolvedValue({ data: response })
    render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
    await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
    await screen.findByText('Final price unknown until actual request and usage are available.')
    expect(screen.getByText('p * 4 + c * 8')).toBeInTheDocument()
  })

  it('legacy responses and malformed quote responses never masquerade as free', async () => {
    vi.spyOn(api, 'get').mockResolvedValueOnce({ data: { success: true, mode: 'legacy', preview: false } }).mockResolvedValueOnce({ data: { success: true, mode: 'identity_service', data: {} } })
    render(wrap(<CurrentQuoteSection model='token' services={['default', 'pro']} />))
    await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
    await screen.findByText('Legacy pricing is active. Identity/service pricing is not enabled.')
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'pro')
    await screen.findByRole('alert')
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })
})

it('same-id/role identity group refresh cannot keep previous identity quote', async () => {
 const get = vi.spyOn(api, 'get').mockResolvedValue({ data: makeQuote() })
 render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
 await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
 await screen.findByRole('table')
 get.mockResolvedValue({ data: makeQuote('token', 'default', 'Friend') })
 act(() => { useAuthStore.setState((state) => ({ auth: { ...state.auth, user: { ...user, group: 'Friend' } } })) })
 await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
 expect(await screen.findByText('Friend')).toBeInTheDocument()
})
