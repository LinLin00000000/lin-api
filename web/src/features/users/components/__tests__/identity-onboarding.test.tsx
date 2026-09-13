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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { AxiosAdapter } from 'axios'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { getUser, type UserIdentityOptions } from '../../api'
import type { User } from '../../types'
import { UsersMutateDrawer } from '../users-mutate-drawer'
import { UsersProvider } from '../users-provider'
import { UsersTable } from '../users-table'

// Intercept only the Axios transport. Production API functions, request
// serialization, interceptors, React form and Base UI Select all run unchanged.
const originalAdapter = api.defaults.adapter
const originalGetAnimations = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'getAnimations'
)
let options: UserIdentityOptions
let failOptions: boolean
let failUpdate: boolean
let storedUser: User
let requests: { method: string; url: string; body?: Record<string, unknown> }[]
let client: QueryClient

beforeEach(async () => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0,
    y: 0,
    width: 100,
    height: 30,
    top: 0,
    right: 100,
    bottom: 30,
    left: 0,
    toJSON: () => ({}),
  })
  Object.defineProperty(HTMLElement.prototype, 'getAnimations', {
    configurable: true,
    value: () => [],
  })
  i18next.addResourceBundle('en', 'translation', en.translation, true, true)
  i18next.addResourceBundle('zh', 'translation', zh.translation, true, true)
  await i18next.changeLanguage('en')
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  options = {
    mode: 'legacy',
    identities: ['default', 'Friend', 'Research Partner'],
  }
  failOptions = false
  failUpdate = false
  storedUser = {
    id: 7,
    username: 'friend-account',
    display_name: 'Friend Account',
    role: 1,
    status: 1,
    group: 'default',
    quota: 0,
    used_quota: 0,
    request_count: 0,
  }
  requests = []
  const adapter: AxiosAdapter = async (config) => {
    const method = config.method ?? 'get'
    const url = config.url ?? ''
    const body = config.data ? JSON.parse(config.data as string) : undefined
    requests.push({ method, url, ...(body ? { body } : {}) })
    let data: unknown
    if (method === 'get' && url === '/api/user/identity-options') {
      data = failOptions
        ? { success: false, message: 'Identity options unavailable' }
        : { success: true, data: options }
    } else if (method === 'get' && url === '/api/authz/catalog') {
      data = { success: true, data: { resources: [], roles: [] } }
    } else if (
      method === 'get' &&
      (url === '/api/user/' || url.startsWith('/api/user/search?'))
    ) {
      data = {
        success: true,
        data: { items: [storedUser], total: 1, page: 1, page_size: 20 },
      }
    } else if (method === 'get' && url === '/api/user/7') {
      data = { success: true, data: storedUser }
    } else if (method === 'put' && url === '/api/user/') {
      if (!failUpdate) storedUser = { ...storedUser, ...body }
      data = failUpdate
        ? { success: false, message: 'Update rejected' }
        : { success: true, data: storedUser }
    } else if (method === 'post' && url === '/api/user/') {
      data = { success: true, data: { ...storedUser, ...body } }
    } else {
      throw new Error(`Unexpected transport request: ${method} ${url}`)
    }
    return { data, status: 200, statusText: 'OK', headers: {}, config }
  }
  api.defaults.adapter = adapter
})

afterEach(async () => {
  cleanup()
  client.clear()
  api.defaults.adapter = originalAdapter
  if (originalGetAnimations) {
    Object.defineProperty(
      HTMLElement.prototype,
      'getAnimations',
      originalGetAnimations
    )
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, 'getAnimations')
  }
  await i18next.changeLanguage('en')
})

function mountDrawer(update = true) {
  const onOpenChange = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <UsersMutateDrawer
          open
          currentRow={update ? storedUser : undefined}
          onOpenChange={onOpenChange}
        />
      </UsersProvider>
    </QueryClientProvider>
  )
  return onOpenChange
}

describe('user identity onboarding through real API transport', () => {
  test('create keeps Common User role 1 and never sets identity or issues a key', async () => {
    const user = userEvent.setup()
    const close = mountDrawer(false)
    await screen.findByText(/To onboard a friend:/)
    expect(screen.getByRole('combobox', { name: 'Role' })).toHaveTextContent(
      'Common User'
    )
    expect(
      screen.queryByRole('combobox', { name: 'Identity' })
    ).not.toBeInTheDocument()
    await user.type(screen.getByLabelText('Username'), 'new-friend')
    await user.type(screen.getByLabelText('Password'), 'test-only-pass')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(close).toHaveBeenCalledWith(false))
    const writes = requests.filter((r) => r.method !== 'get')
    expect(writes).toEqual([
      {
        method: 'post',
        url: '/api/user/',
        body: {
          username: 'new-friend',
          display_name: 'new-friend',
          password: 'test-only-pass',
          role: 1,
        },
      },
    ])
    process.stdout.write(`C4 create transport ${JSON.stringify(writes)}\n`)
  })

  test('legacy draft Friend click and submit sends group on PUT and preserves role on readback', async () => {
    const user = userEvent.setup()
    const close = mountDrawer()
    await screen.findByDisplayValue('Friend Account')
    await user.click(await screen.findByRole('combobox', { name: 'Identity' }))
    await user.click(await screen.findByRole('option', { name: 'Friend' }))
    expect(
      screen.getByRole('combobox', { name: 'Identity' })
    ).toHaveTextContent('Friend')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(close).toHaveBeenCalledWith(false))
    expect(requests.filter((r) => r.method !== 'get')).toEqual([
      {
        method: 'put',
        url: '/api/user/',
        body: {
          id: 7,
          username: 'friend-account',
          display_name: 'Friend Account',
          group: 'Friend',
        },
      },
    ])
    const readback = await getUser(7)
    expect(readback.data).toMatchObject({ id: 7, group: 'Friend', role: 1 })
    expect(requests.some((r) => r.url.startsWith('/api/group'))).toBe(false)
    process.stdout.write(
      `C4 update transport and readback (simulated server) ${JSON.stringify({ options, requests, readback })}\n`
    )
  })

  test('configured names are authoritative with no hardcoded Friend option', async () => {
    options.mode = 'identity_service'
    options.identities = ['default', 'Research Partner']
    const user = userEvent.setup()
    mountDrawer()
    await screen.findByText(/To onboard a friend:/)
    await user.click(await screen.findByRole('combobox', { name: 'Identity' }))
    expect(
      await screen.findByRole('option', { name: 'Research Partner' })
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('option', { name: 'Friend' })
    ).not.toBeInTheDocument()
  })

  test('legacy response uses Identity label and legacy choices on the same user endpoint', async () => {
    options = { mode: 'legacy', identities: ['default', 'vip'] }
    const user = userEvent.setup()
    const close = mountDrawer()
    await screen.findByDisplayValue('Friend Account')
    const select = await screen.findByRole('combobox', { name: 'Identity' })
    await waitFor(() => expect(select).not.toBeDisabled())
    await user.click(select)
    await user.click(await screen.findByRole('option', { name: 'vip' }))
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(close).toHaveBeenCalledWith(false))
    expect((await getUser(7)).data?.group).toBe('vip')
    expect(screen.getByText(/To onboard a friend:/)).toHaveTextContent(
      'Assigning an identity does not activate identity billing.'
    )
    expect(requests.some((r) => r.url.startsWith('/api/group'))).toBe(false)
  })

  test('empty identity options disable selection without inventing choices', async () => {
    options.identities = []
    mountDrawer()
    expect(
      await screen.findByRole('combobox', { name: 'Identity' })
    ).toBeDisabled()
  })

  test('failed options show a retry instruction and disable selection', async () => {
    failOptions = true
    mountDrawer()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Failed to load user identities'
    )
    expect(screen.getByRole('combobox', { name: 'Identity' })).toBeDisabled()
    expect(requests.filter((r) => r.method !== 'get')).toHaveLength(0)
  })

  test('rejected update stays open and does not change the stored identity', async () => {
    failUpdate = true
    const user = userEvent.setup()
    const close = mountDrawer()
    await screen.findByDisplayValue('Friend Account')
    await user.click(await screen.findByRole('combobox', { name: 'Identity' }))
    await user.click(await screen.findByRole('option', { name: 'Friend' }))
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() =>
      expect(requests.some((r) => r.method === 'put')).toBe(true)
    )
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Save changes' })
      ).not.toBeDisabled()
    )
    expect(close).not.toHaveBeenCalled()
    expect((await getUser(7)).data?.group).toBe('default')
  })

  test('Chinese locale explains two steps and self-service keys', async () => {
    await i18next.changeLanguage('zh')
    mountDrawer(false)
    expect(await screen.findByText(/先创建普通用户/)).toHaveTextContent(
      '管理员不得代发 Key'
    )
  })
})

function mountTable() {
  const rootRoute = createRootRoute({ component: Outlet })
  const authenticatedRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    component: Outlet,
  })
  const usersRoute = createRoute({
    getParentRoute: () => authenticatedRoute,
    path: '/users/',
    validateSearch: (search: Record<string, unknown>) => ({
      group: typeof search.group === 'string' ? search.group : '',
    }),
    component: UsersTable,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      authenticatedRoute.addChildren([usersRoute]),
    ]),
    history: createMemoryHistory({ initialEntries: ['/users/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <RouterProvider router={router} />
      </UsersProvider>
    </QueryClientProvider>
  )
  return router
}

describe('users list identity filter', () => {
  test('selecting a configured identity updates the URL and sends group search through the real client', async () => {
    const user = userEvent.setup()
    const router = mountTable()
    const select = await screen.findByRole('combobox', { name: 'Identity' })
    await screen.findByRole('option', { name: 'Research Partner' })
    await user.selectOptions(select, 'Research Partner')
    await waitFor(() =>
      expect(
        requests.some((r) => r.url.includes('group=Research+Partner'))
      ).toBe(true)
    )
    expect(router.state.location.search).toMatchObject({
      group: 'Research Partner',
    })
    expect(
      screen.getByRole('columnheader', { name: 'Identity' })
    ).toBeInTheDocument()
    await user.selectOptions(select, '')
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({ group: '' })
    )
    expect(requests.some((r) => r.url.startsWith('/api/group'))).toBe(false)
    process.stdout.write(`C4 list transport ${JSON.stringify(requests)}\n`)
  })

  test('legacy list uses Identity terminology and backend legacy options', async () => {
    options = { mode: 'legacy', identities: ['default', 'vip'] }
    const user = userEvent.setup()
    mountTable()
    const select = await screen.findByRole('combobox', { name: 'Identity' })
    await screen.findByRole('option', { name: 'vip' })
    await user.selectOptions(select, 'vip')
    await waitFor(() =>
      expect(requests.some((r) => r.url.includes('group=vip'))).toBe(true)
    )
    expect(
      screen.getByRole('columnheader', { name: 'Identity' })
    ).toBeInTheDocument()
  })
})
