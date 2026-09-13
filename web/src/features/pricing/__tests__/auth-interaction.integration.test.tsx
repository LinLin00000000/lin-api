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
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { applyAuthBundle, clearAuthentication } from '@/lib/auth-session'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

import { CurrentQuoteSection } from '../components/current-quote-section'
import { usePricingData } from '../hooks/use-pricing-data'
import { readCurrentQuote, CurrentQuoteAuthenticationExpiredError } from '../current-quote-api'

const originalAdapter = api.defaults.adapter
let client: QueryClient
const navigate = vi.fn()
function bundle(group = 'ordinary', sid = 'synthetic-session-a'): AuthBundle {
  return { user: { id: 12, username: 'fixture', role: 1, group }, access_token: `synthetic-token-${sid}`, token_type: 'Bearer', access_expires_at: 9999999999,
    session: { sid, current: true, login_method: 'password', ip: '127.0.0.1', user_agent: 'test', created_at: 1, last_active_at: 1, expires_at: 9999999999 } }
}
function response(config: InternalAxiosRequestConfig, data: unknown): AxiosResponse {
  return { config, data, status: 200, statusText: 'OK', headers: {} }
}
function unauthorized(config: InternalAxiosRequestConfig) {
  return new AxiosError('Unauthorized', 'ERR_BAD_REQUEST', config, undefined, { ...response(config, { success: false }), status: 401 })
}
function mount(node = <CurrentQuoteSection model='token' services={['default']} />) {
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}
async function selectQuote() {
  await userEvent.click(screen.getByRole('button', { name: 'Current identity/service quote' }))
  await userEvent.selectOptions(screen.getByLabelText('Quote service'), 'default')
}
function expectSecretFree() {
  const keys = JSON.stringify(client.getQueryCache().getAll().map((query) => query.queryKey))
  expect(keys).not.toContain('synthetic-token')
  expect(keys).not.toContain('synthetic-session')
}
beforeEach(() => {
  localStorage.clear()
  clearAuthentication(false)
  applyAuthBundle(bundle(), false)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  navigate.mockReset()
  const actualWindow = window
  vi.stubGlobal('window', new Proxy(actualWindow, { get(target, property) {
    if (property === 'location') return { pathname: '/pricing', search: '?model=token&service=default', replace: navigate }
    return Reflect.get(target, property)
  } }))
})
afterEach(() => {
  cleanup()
  client.clear()
  api.defaults.adapter = originalAdapter
  clearAuthentication(false)
  localStorage.clear()
  vi.unstubAllGlobals()
})

it('quote HTTP 401 preserves auth until Sign in again clears the real store and preserves the return path', async () => {
  const owner = useAuthStore.getState().auth
  const requests: InternalAxiosRequestConfig[] = []
  api.defaults.adapter = async (config) => { requests.push(config); throw unauthorized(config) }
  mount()
  await selectQuote()
  await screen.findByRole('alert')
  expect(requests).toHaveLength(1)
  expect(requests[0].url).toBe('/api/pricing/current')
  expect(requests[0].headers.Authorization).toBe(`Bearer ${owner.accessToken}`)
  expect(useAuthStore.getState().auth.user).toBe(owner.user)
  expect(useAuthStore.getState().auth.session).toBe(owner.session)
  expect(navigate).not.toHaveBeenCalled()
  expectSecretFree()
  await userEvent.click(screen.getByRole('button', { name: 'Sign in again' }))
  expect(useAuthStore.getState().auth).toMatchObject({ user: null, accessToken: null, session: null, accessExpiresAt: null, pending2FAFlowToken: null })
  expect(navigate).toHaveBeenCalledExactlyOnceWith('/sign-in?redirect=%2Fpricing%3Fmodel%3Dtoken%26service%3Ddefault')
})

it('same-ID sign-in after quote 401 reloads the quote through real HTTP and removes the expired action', async () => {
  let calls = 0
  api.defaults.adapter = async (config) => {
    calls += 1
    if (calls === 1) throw unauthorized(config)
    expect(config.headers.Authorization).toBe('Bearer synthetic-token-synthetic-session-b')
    return response(config, { success: true, mode: 'legacy', preview: false })
  }
  mount()
  await selectQuote()
  await screen.findByRole('alert')
  act(() => { applyAuthBundle(bundle('ordinary', 'synthetic-session-b'), false) })
  expect(await screen.findByText('Legacy pricing is active. Identity/service pricing is not enabled.')).toBeInTheDocument()
  expect(calls).toBe(2)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
  expectSecretFree()
})

it('late old HTTP 401 after a new same-ID session cannot clear or redirect the new session', async () => {
  let rejectOld!: (reason: unknown) => void
  let oldConfig!: InternalAxiosRequestConfig
  api.defaults.adapter = (config) => {
    if (!oldConfig) {
      oldConfig = config
      return new Promise((_resolve, reject) => { rejectOld = reject })
    }
    return Promise.resolve(response(config, { success: true, mode: 'legacy', preview: false }))
  }
  mount()
  await selectQuote()
  await waitFor(() => expect(oldConfig).toBeDefined())
  const next = bundle('ordinary', 'synthetic-session-b')
  act(() => { applyAuthBundle(next, false) })
  await screen.findByText('Legacy pricing is active. Identity/service pricing is not enabled.')
  await act(async () => { rejectOld(unauthorized(oldConfig)) })
  expect(useAuthStore.getState().auth.user).toBe(next.user)
  expect(useAuthStore.getState().auth.session).toBe(next.session)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
})

it('uncancelled old quote 401 reaches the interceptor but is not an actionable expiry for a replacement session', async () => {
  let rejectOld!: (reason: unknown) => void
  let config!: InternalAxiosRequestConfig
  api.defaults.adapter = (request) => {
    config = request
    return new Promise((_resolve, reject) => { rejectOld = reject })
  }
  const pending = readCurrentQuote('token', 'default', undefined, new AbortController().signal, 'ordinary').catch((error: unknown) => error)
  await waitFor(() => expect(config).toBeDefined())
  const next = bundle('ordinary', 'synthetic-session-b')
  applyAuthBundle(next, false)
  rejectOld(unauthorized(config))
  const error = await pending
  expect(error).toBeInstanceOf(AxiosError)
  expect(error).not.toBeInstanceOf(CurrentQuoteAuthenticationExpiredError)
  expect(useAuthStore.getState().auth.session).toBe(next.session)
  expect(navigate).not.toHaveBeenCalled()
})

function Catalog() {
  const data = usePricingData()
  return <div><output aria-label='Catalog services'>{Object.keys(data.usableGroup).join(',')}</output><output aria-label='Catalog models'>{data.models.map((model) => model.model_name).join(',')}</output></div>
}
it.each(['Friend', 'ordinary'])('catalog switching to %s with a new same-ID session hides cached services until its own HTTP response', async (group) => {
  let resolveNext!: (value: AxiosResponse) => void
  let nextConfig!: InternalAxiosRequestConfig
  let calls = 0
  const catalog = (service: string) => ({ success: true, data: [{ model_name: `${service}-model` }], vendors: [], usable_group: { [service]: service }, group_ratio: {} })
  api.defaults.adapter = (config) => {
    if (config.url === '/api/status') return Promise.resolve(response(config, { success: true, data: {} }))
    expect(config.url).toBe('/api/pricing')
    calls += 1
    if (calls === 1) return Promise.resolve(response(config, catalog('old-only')))
    nextConfig = config
    return new Promise((resolve) => { resolveNext = resolve })
  }
  mount(<Catalog />)
  await waitFor(() => expect(screen.getByLabelText('Catalog services')).toHaveTextContent('old-only'))
  act(() => { applyAuthBundle(bundle(group, 'synthetic-session-b'), false) })
  expect(screen.getByLabelText('Catalog services')).toBeEmptyDOMElement()
  expect(screen.getByLabelText('Catalog models')).toBeEmptyDOMElement()
  await waitFor(() => expect(calls).toBe(2))
  expect(nextConfig.headers.Authorization).toBe('Bearer synthetic-token-synthetic-session-b')
  await act(async () => { resolveNext(response(nextConfig, catalog('new-only'))) })
  await waitFor(() => expect(screen.getByLabelText('Catalog services')).toHaveTextContent('new-only'))
  expect(screen.getByLabelText('Catalog models')).toHaveTextContent('new-only-model')
  expect(screen.queryByText('old-only')).not.toBeInTheDocument()
  expectSecretFree()
})

it('concurrent catalog consumers in one auth generation share a React Query request', async () => {
  let resolveCatalog!: (value: AxiosResponse) => void
  let catalogConfig!: InternalAxiosRequestConfig
  let calls = 0
  api.defaults.adapter = (config) => {
    if (config.url === '/api/status') return Promise.resolve(response(config, { success: true, data: {} }))
    calls += 1
    catalogConfig = config
    return new Promise((resolve) => { resolveCatalog = resolve })
  }
  mount(<><Catalog /><Catalog /></>)
  await waitFor(() => expect(calls).toBe(1))
  await act(async () => { resolveCatalog(response(catalogConfig, { success: true, data: [], vendors: [], usable_group: { shared: 'shared' }, group_ratio: {} })) })
  await waitFor(() => {
    for (const output of screen.getAllByLabelText('Catalog services')) expect(output).toHaveTextContent('shared')
  })
  expect(calls).toBe(1)
})

it('unrelated GETs retain the HTTP client in-flight deduplication', async () => {
  let resolveRequest!: (value: AxiosResponse) => void
  let requestConfig!: InternalAxiosRequestConfig
  let calls = 0
  api.defaults.adapter = (config) => {
    calls += 1
    requestConfig = config
    return new Promise((resolve) => { resolveRequest = resolve })
  }
  const first = api.get('/api/status')
  const second = api.get('/api/status')
  expect(second).toBe(first)
  await waitFor(() => expect(calls).toBe(1))
  resolveRequest(response(requestConfig, { success: true, data: {} }))
  await Promise.all([first, second])
  expect(calls).toBe(1)
})

// Identity refresh does not replace the session SID, even for the same group.
it.each(['Friend', 'ordinary'])('same-session refresh to %s cannot consume an in-flight old catalog', async (group) => {
  let oldConfig!: InternalAxiosRequestConfig
  let resolveOld!: (value: AxiosResponse) => void
  let calls = 0
  api.defaults.adapter = (config) => {
    if (config.url === '/api/status') return Promise.resolve(response(config, { success: true, data: {} }))
    calls += 1
    if (calls === 1) { oldConfig = config; return new Promise((resolve) => { resolveOld = resolve }) }
    return Promise.resolve(response(config, { success: true, data: [{ model_name: 'new-model' }], vendors: [], usable_group: { 'new-only': 'new' }, group_ratio: {} }))
  }
  mount(<Catalog />)
  await waitFor(() => expect(calls).toBe(1))
  const session = useAuthStore.getState().auth.session
  act(() => {
    const auth = useAuthStore.getState().auth
    if (!auth.user) throw new Error('Expected authenticated catalog owner')
    auth.setUser({ ...auth.user, group })
  })
  expect(useAuthStore.getState().auth.session).toBe(session)
  await act(async () => { resolveOld(response(oldConfig, { success: true, data: [{ model_name: 'old-model' }], vendors: [], usable_group: { 'old-only': 'old' }, group_ratio: {} })) })
  await waitFor(() => expect(screen.getByLabelText('Catalog services')).toHaveTextContent('new-only'))
  expect(screen.getByLabelText('Catalog models')).toHaveTextContent('new-model')
  expect(screen.getByLabelText('Catalog services')).not.toHaveTextContent('old-only')
  expect(screen.getByLabelText('Catalog models')).not.toHaveTextContent('old-model')
  expect(calls).toBe(2)
  expectSecretFree()
})
