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
import { StrictMode } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import type { IdentityServiceConfig } from '@/features/system-settings/billing/identity-service/api'
import { IdentityServiceSection } from '@/features/system-settings/billing/identity-service/identity-service-section'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

vi.mock('@/lib/api', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn() },
}))
const fixture: IdentityServiceConfig = {
  version: 1,
  mode: 'legacy',
  revision: 7,
  migration_source_digest: '',
  service_defaults: { Default: 1, Pro: 2 },
  identity_defaults: { Friend: 0, VIP: 1 },
  service_models: {
    Default: { m: { enabled: true } },
    Pro: { m: { enabled: false, ratio: 1 } },
  },
  identity_model_ratios: { Friend: { m: 1 } },
  model_identity_scopes: { m: { mode: 'restricted', identities: ['Friend'] } },
}
const valid = {
  success: true,
  config_valid: true,
  activation_ready: false,
  activation_blockers: ['synthetic activation gate'],
  pending_verification: ['synthetic production gate'],
}
function mount(role = 100) {
  useAuthStore.getState().auth.setUser({ id: 123, username: 'synthetic', role })
  return render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: {
            queries: { retry: false },
            mutations: { retry: false },
          },
        })
      }
    >
      <IdentityServiceSection />
    </QueryClientProvider>
  )
}
function section(name: string) {
  return within(screen.getByRole('region', { name }))
}
async function load() {
  mount()
  await screen.findByText(/^Configuration revision/)
  return userEvent.setup()
}
beforeEach(() => {
  useAuthStore.getState().auth.reset()
  vi.mocked(api.get)
    .mockReset()
    .mockResolvedValue({
      data: {
        success: true,
        data: structuredClone(fixture),
        activation_ready: false,
      },
    })
  vi.mocked(api.post).mockReset().mockResolvedValue({ data: valid })
  vi.mocked(api.put)
    .mockReset()
    .mockResolvedValue({
      data: { success: true, data: { ...fixture, revision: 8 } },
    })
})

test('quality: pending validation must not PUT old draft after Root account replacement', async () => {
  let resolve!: (v: unknown) => void
  vi.mocked(api.post).mockImplementationOnce(
    () =>
      new Promise((r) => {
        resolve = r
      }) as never
  )
  const user = await load()
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1))
  await act(async () =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 999, username: 'new-root', role: 100 })
  )
  await screen.findByText(/^Configuration revision/)
  await act(async () => resolve({ data: valid }))
  expect(api.put).not.toHaveBeenCalled()
})
test('quality: pending validation must not PUT after editor unmount', async () => {
  let resolve!: (v: unknown) => void
  vi.mocked(api.post).mockImplementationOnce(
    () =>
      new Promise((r) => {
        resolve = r
      }) as never
  )
  const view = mount()
  await screen.findByText(/^Configuration revision/)
  await userEvent
    .setup()
    .click(
      screen.getByRole('button', { name: 'Validate and save legacy draft' })
    )
  await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1))
  view.unmount()
  await act(async () => resolve({ data: valid }))
  expect(api.put).not.toHaveBeenCalled()
})
test('quality: ordinary rapid doubleclick emits one mutation and fields stay locked', async () => {
  let resolve!: (v: unknown) => void
  vi.mocked(api.post).mockImplementationOnce(
    () =>
      new Promise((r) => {
        resolve = r
      }) as never
  )
  const user = await load()
  await user.dblClick(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  expect(api.post).toHaveBeenCalledTimes(1)
  expect(
    section('Service defaults (S)').getAllByLabelText('Ratio')[0]
  ).toBeDisabled()
  await act(async () => resolve({ data: valid }))
  await screen.findByText(
    'Saved legacy configuration and verified server readback.'
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

function deferredResponse() {
  let resolve!: (value: never) => void
  const promise = new Promise<never>((done) => {
    resolve = done
  })
  return { promise, release: (data: unknown) => resolve({ data } as never) }
}
const invalidate = {
  token: () =>
    useAuthStore.setState((s) => ({
      auth: { ...s.auth, accessToken: 'synthetic-replacement' },
    })),
  session: () =>
    useAuthStore.setState((s) => ({
      auth: { ...s.auth, session: { sid: 'synthetic-session' } as never },
    })),
  logout: () => useAuthStore.getState().auth.reset(),
  permission: () =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 123, username: 'synthetic', role: 10 }),
  root: () =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 999, username: 'new-root', role: 100 }),
  aba: () => {
    const auth = useAuthStore.getState().auth
    useAuthStore.getState().auth.reset()
    useAuthStore.setState({ auth })
  },
}
test.each(['token', 'session', 'logout', 'permission', 'aba'] as const)(
  'pending validate then %s invalidation never starts a PUT',
  async (kind) => {
    const pending = deferredResponse()
    vi.mocked(api.post).mockReturnValueOnce(pending.promise)
    const user = await load()
    await user.click(
      screen.getByRole('button', { name: 'Validate and save legacy draft' })
    )
    expect(api.post).toHaveBeenCalledTimes(1)
    await act(async () => invalidate[kind]())
    await act(async () => pending.release(valid))
    expect(api.put).not.toHaveBeenCalled()
    expect(
      screen.queryByText(
        'Saved legacy configuration and verified server readback.'
      )
    ).not.toBeInTheDocument()
  }
)
test.each([
  'unmount',
  'root',
  'token',
  'session',
  'logout',
  'permission',
] as const)(
  'pending PUT then %s invalidation never starts readback or replaces current draft',
  async (kind) => {
    const pending = deferredResponse()
    vi.mocked(api.put).mockReturnValueOnce(pending.promise)
    const view = mount()
    await screen.findByText(/^Configuration revision/)
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Validate and save legacy draft' })
    )
    await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
    if (kind === 'unmount') view.unmount()
    else await act(async () => invalidate[kind]())
    expect(vi.mocked(api.put).mock.calls[0][2]).toMatchObject({
      skipAuthRefresh: true,
      signal: expect.any(AbortSignal),
    })
    expect(vi.mocked(api.put).mock.calls[0][2]?.signal?.aborted).toBe(true)
    const visible = kind === 'root' || kind === 'token' || kind === 'session'
    if (visible) {
      await screen.findByText(/^Configuration revision/)
      const ratio = section('Service defaults (S)').getAllByLabelText(
        'Ratio'
      )[0]
      await user.clear(ratio)
      await user.type(ratio, '3')
    }
    const reads = vi.mocked(api.get).mock.calls.length
    await act(async () =>
      pending.release({ success: true, data: { ...fixture, revision: 8 } })
    )
    expect(api.get).toHaveBeenCalledTimes(reads)
    expect(api.put).toHaveBeenCalledTimes(1)
    expect(
      screen.queryByText(
        'Saved legacy configuration and verified server readback.'
      )
    ).not.toBeInTheDocument()
    if (visible) {
      expect(
        section('Service defaults (S)').getAllByLabelText('Ratio')[0]
      ).toHaveValue('3')
    }
  }
)
test('late save readback after session replacement cannot overwrite the new draft', async () => {
  const pending = deferredResponse()
  const user = await load()
  vi.mocked(api.get).mockReturnValueOnce(pending.promise)
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await waitFor(() => expect(api.get).toHaveBeenCalledTimes(2))
  await act(async () => invalidate.token())
  await screen.findByText(/^Configuration revision/)
  const ratio = section('Service defaults (S)').getAllByLabelText('Ratio')[0]
  await user.clear(ratio)
  await user.type(ratio, '3')
  await act(async () =>
    pending.release({ success: true, data: { ...fixture, revision: 99 } })
  )
  expect(ratio).toHaveValue('3')
  expect(screen.getByText(/^Configuration revision/)).toHaveTextContent('7')
  expect(api.get).toHaveBeenCalledTimes(3)
  expect(
    screen.queryByText(
      'Saved legacy configuration and verified server readback.'
    )
  ).not.toBeInTheDocument()
})
test('uninterrupted save uses CAS and a fresh GET, not the PUT response', async () => {
  const user = await load()
  vi.mocked(api.get).mockResolvedValueOnce({
    data: { success: true, data: { ...fixture, revision: 9 } },
  })
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText(
    'Saved legacy configuration and verified server readback.'
  )
  expect(api.put).toHaveBeenCalledTimes(1)
  expect(vi.mocked(api.put).mock.calls[0][1]).toMatchObject({
    expected_revision: 7,
    config: { mode: 'legacy' },
  })
  expect(api.get).toHaveBeenCalledTimes(2)
  expect(screen.getByText(/^Configuration revision/)).toHaveTextContent('9')
})

test('StrictMode lifecycle setup still permits a normal single CAS save', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 123, username: 'synthetic', role: 100 })
  render(
    <StrictMode>
      <QueryClientProvider client={new QueryClient()}>
        <IdentityServiceSection />
      </QueryClientProvider>
    </StrictMode>
  )
  await screen.findByText(/^Configuration revision/)
  await userEvent
    .setup()
    .click(
      screen.getByRole('button', { name: 'Validate and save legacy draft' })
    )
  await screen.findByText(
    'Saved legacy configuration and verified server readback.'
  )
  expect(api.put).toHaveBeenCalledTimes(1)
  expect(vi.mocked(api.put).mock.calls[0][2]?.signal?.aborted).toBe(false)
})
test('pending explicit reload then token replacement cannot reset the new draft', async () => {
  const user = await load()
  const pending = deferredResponse()
  vi.mocked(api.get).mockReturnValueOnce(pending.promise)
  await user.click(
    screen.getByRole('button', { name: 'Discard draft and reload' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Confirm discard and reload' })
  )
  await waitFor(() => expect(api.get).toHaveBeenCalledTimes(2))
  await act(async () => invalidate.token())
  await screen.findByText(/^Configuration revision/)
  const ratio = section('Service defaults (S)').getAllByLabelText('Ratio')[0]
  await user.clear(ratio)
  await user.type(ratio, '3')
  await act(async () =>
    pending.release({ success: true, data: { ...fixture, revision: 99 } })
  )
  expect(ratio).toHaveValue('3')
  expect(screen.getByText(/^Configuration revision/)).toHaveTextContent('7')
  expect(api.put).not.toHaveBeenCalled()
})
test('pending initial GET then same-ID token replacement never exposes the old snapshot', async () => {
  const pending = deferredResponse()
  vi.mocked(api.get).mockReturnValueOnce(pending.promise)
  mount()
  await waitFor(() => expect(api.get).toHaveBeenCalledTimes(1))
  await act(async () => invalidate.token())
  await screen.findByText(/^Configuration revision/)
  await act(async () =>
    pending.release({ success: true, data: { ...fixture, revision: 99 } })
  )
  expect(screen.getByText(/^Configuration revision/)).toHaveTextContent('7')
  expect(api.get).toHaveBeenCalledTimes(2)
})
