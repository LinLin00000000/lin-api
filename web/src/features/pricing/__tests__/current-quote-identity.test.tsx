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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

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
afterEach(() => { vi.restoreAllMocks(); useAuthStore.setState((state) => ({ auth: { ...state.auth, user: null, accessToken: null, session: null } })) })


function deferred() {
  let resolve!: (value: { data: ReturnType<typeof makeQuote> }) => void
  const promise = new Promise<{ data: ReturnType<typeof makeQuote> }>((done) => { resolve = done })
  return { promise, resolve }
}
function setGroup(group: string) {
  act(() => { useAuthStore.setState((state) => ({ auth: { ...state.auth, user: { ...user, group } } })) })
}
it('group change hides a settled quote immediately while the new identity request is pending', async () => {
  const next = deferred()
  const get = vi.spyOn(api, 'get').mockResolvedValueOnce({ data: makeQuote() }).mockReturnValueOnce(next.promise)
  render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
  await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
  await screen.findByRole('table')
  setGroup('Friend')
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
  expect(get.mock.calls[1][1]?.params).toEqual({ model: 'token', service: 'default' })
  await act(async () => { next.resolve({ data: makeQuote('token', 'default', 'Friend') }) })
  expect(await screen.findByText('Friend')).toBeInTheDocument()
})
it('pending ordinary response arriving after Friend response cannot display old identity', async () => {
  const old = deferred()
  const get = vi.spyOn(api, 'get').mockReturnValueOnce(old.promise).mockResolvedValueOnce({ data: makeQuote('token', 'default', 'Friend') })
  render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
  await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
  await waitFor(() => expect(get).toHaveBeenCalledTimes(1))
  setGroup('Friend')
  expect(await screen.findByText('Friend')).toBeInTheDocument()
  await act(async () => { old.resolve({ data: makeQuote() }) })
  expect(screen.queryByText('ordinary')).not.toBeInTheDocument()
  expect(screen.getByText('Friend')).toBeInTheDocument()
})
it('ordinary HTTP response for the wrong identity is unavailable, not a free quote', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: makeQuote('token', 'default', 'Friend') })
  render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
  await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
  expect(await screen.findByRole('alert')).toHaveTextContent('Unknown is not free')
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
})
it('same-id auth session replacement hides quotes and rejects the old pending result', async () => {
  const old = deferred()
  const get = vi.spyOn(api, 'get').mockReturnValueOnce(old.promise).mockResolvedValueOnce({ data: makeQuote() })
  render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
  await openQuote(); await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
  await waitFor(() => expect(get).toHaveBeenCalledTimes(1))
  act(() => { useAuthStore.setState((state) => ({ auth: { ...state.auth, accessToken: 'synthetic-new-session' } })) })
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
  await screen.findByRole('table')
  const stale = makeQuote(); stale.data.quote.ratios.revision = 12345
  await act(async () => { old.resolve({ data: stale }) })
  expect(screen.queryByText('12345')).not.toBeInTheDocument()
})
it('Root preview identity change and return to ordinary own identity never reuse preview results', async () => {
  useAuthStore.setState((state) => ({ auth: { ...state.auth, user: { ...user, role: ROLE.SUPER_ADMIN } } }))
  const old = deferred()
  const get = vi.spyOn(api, 'get').mockImplementation((path, options) => {
    if (path === '/api/option/identity_service') return Promise.resolve({ data: { success: true, data: { version: 1, mode: 'legacy', revision: 7, service_defaults: { default: 2 }, identity_defaults: { Friend: 0, VIP: 1 } } } })
    if (options?.params.identity === 'Friend') return old.promise
    return Promise.resolve({ data: makeQuote('token', 'default', options?.params.identity ?? 'ordinary', path !== '/api/pricing/current') })
  })
  render(wrap(<CurrentQuoteSection model='token' services={['default']} />))
  await openQuote(); await userEvent.click(screen.getByLabelText('Preview saved identity/service configuration'))
  await screen.findByRole('option', { name: 'Friend' })
  await userEvent.selectOptions(screen.getByLabelText('Preview identity'), 'Friend')
  await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
  await userEvent.selectOptions(screen.getByLabelText('Preview identity'), 'VIP')
  await screen.findByRole('table')
  expect(screen.getByText('VIP', { selector: 'dd' })).toBeInTheDocument()
  act(() => { useAuthStore.setState((state) => ({ auth: { ...state.auth, user } })) })
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
  await screen.findByText('ordinary', { selector: 'dd' })
  await act(async () => { old.resolve({ data: makeQuote('token', 'default', 'Friend', true) }) })
  expect(screen.queryByLabelText('Preview identity')).not.toBeInTheDocument()
  expect(screen.queryByText('Saved configuration preview only. Does not enable billing or grant access.')).not.toBeInTheDocument()
  expect(get).toHaveBeenLastCalledWith('/api/pricing/current', expect.objectContaining({ params: { model: 'token', service: 'default' } }))
})
