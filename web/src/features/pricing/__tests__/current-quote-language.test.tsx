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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import en from '@/i18n/locales/en.json'
import zhCN from '@/i18n/locales/zh.json'
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
async function languageView() {
  const i18n = createInstance()
  await i18n.use(initReactI18next).init({ resources: { en, zhCN }, lng: 'zhCN', fallbackLng: 'en', nsSeparator: false })
  render(wrap(<I18nextProvider i18n={i18n}><CurrentQuoteSection model='token' services={['default']} /></I18nextProvider>))
  await userEvent.click(screen.getByRole('button', { name: '当前身份/服务报价' }))
  return i18n
}

beforeEach(() => { useAuthStore.setState((state) => ({ auth: { ...state.auth, user } })) })
afterEach(() => { vi.restoreAllMocks(); useAuthStore.setState((state) => ({ auth: { ...state.auth, user: null, accessToken: null, session: null } })) })


it('actual zhCN translation registration renders unknown-not-free in the DOM', async () => {
  vi.spyOn(api, 'get').mockRejectedValue(new Error('unknown'))
  const i18n = await languageView()
  expect(i18n.exists('Current quote unavailable. Unknown is not free.', { lng: 'zhCN', fallbackLng: false })).toBe(true)
  await userEvent.selectOptions(screen.getByLabelText('报价服务'), 'default')
  expect(await screen.findByRole('alert')).toHaveTextContent('当前无法报价；未知不代表免费。')
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
})
it('actual zhCN zero and Root saved-preview warnings render without enabling billing', async () => {
  useAuthStore.setState((state) => ({ auth: { ...state.auth, user: { ...user, role: ROLE.SUPER_ADMIN } } }))
  vi.spyOn(api, 'get').mockImplementation((path) => {
    if (path === '/api/option/identity_service') return Promise.resolve({ data: { success: true, data: { version: 1, mode: 'legacy', revision: 7, service_defaults: { default: 2 }, identity_defaults: { Friend: 0 } } } })
    return Promise.resolve({ data: makeQuote('token', 'default', 'Friend', true) })
  })
  await languageView()
  await userEvent.click(screen.getByLabelText('预览已保存的身份/服务配置'))
  await screen.findByRole('option', { name: 'Friend' })
  await userEvent.selectOptions(screen.getByLabelText('预览身份'), 'Friend')
  await userEvent.selectOptions(screen.getByLabelText('报价服务'), 'default')
  expect(await screen.findByRole('row', { name: /input 4 0 quota\/token/ })).toBeInTheDocument()
  expect(screen.getByText("仅预览已保存配置，不启用计费，也不授予调用权限。")).toBeInTheDocument()
  expect(screen.getByText("零是明确的计费项价格，不代表价格未知，也不豁免其他费用。")).toBeInTheDocument()
})
