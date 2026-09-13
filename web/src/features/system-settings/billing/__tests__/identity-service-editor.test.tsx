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
import { AxiosError } from 'axios'
import i18next from 'i18next'
import { beforeEach, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import {
  identityServicePath,
  type IdentityServiceConfig,
} from '../identity-service/api'
import { IdentityServiceSection } from '../identity-service/identity-service-section'

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
function httpError(status: number, message: string) {
  return new AxiosError(message, undefined, undefined, undefined, {
    status,
    data: {
      success: false,
      message,
      activation_ready: false,
      activation_blockers: ['synthetic activation gate'],
    },
    statusText: '',
    headers: {},
    config: { headers: {} },
  } as never)
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
async function selectGroup(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
  group: string
) {
  const input = section(name).getByLabelText('Selected exact group')
  await user.clear(input)
  await user.type(input, group)
  return section(name)
}
const service = 'Service model availability and S overrides'
const identity = 'Identity model D overrides'
beforeEach(() => {
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

test.each([0, 1, 10])(
  'non-root role %s has no editor and no dedicated request',
  (role) => {
    const view = mount(role)
    expect(view.container).toBeEmptyDOMElement()
    expect(api.get).not.toHaveBeenCalled()
  }
)

test('Root edits zero, one and inheritance; group drafts stay isolated and save is validate PUT GET', async () => {
  const user = await load()
  const s = await selectGroup(user, service, 'Default')
  expect(s.getByLabelText('Ratio')).toHaveValue('')
  expect(s.getAllByText('Inherit default').length).toBeGreaterThan(0)
  await user.type(s.getByLabelText('Ratio'), '0')
  await selectGroup(user, service, 'Pro')
  expect(s.getByLabelText('Ratio')).toHaveValue('1')
  expect(s.getByLabelText('Explicitly open model')).not.toBeChecked()
  await user.click(s.getByLabelText('Explicitly open model'))
  await selectGroup(user, service, 'Default')
  expect(s.getByLabelText('Ratio')).toHaveValue('0')
  const d = await selectGroup(user, identity, 'Friend')
  await user.click(d.getByRole('button', { name: 'Remove override / inherit' }))
  expect(d.getByLabelText('Ratio')).toHaveValue('')
  vi.mocked(api.get).mockResolvedValueOnce({
    data: {
      success: true,
      data: {
        ...fixture,
        revision: 9,
        service_defaults: { ServerReadback: 3 },
      },
    },
  })
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText(
    'Saved legacy configuration and verified server readback.'
  )
  const payload = vi.mocked(api.put).mock.calls[0][1] as {
    config: IdentityServiceConfig
    expected_revision: number
  }
  expect(payload.expected_revision).toBe(7)
  expect(payload.config.mode).toBe('legacy')
  expect(payload.config.revision).toBe(7)
  expect(payload.config.service_models.Default.m).toEqual({
    enabled: true,
    ratio: 0,
  })
  expect(payload.config.service_models.Pro.m).toEqual({
    enabled: true,
    ratio: 1,
  })
  expect(payload.config.identity_defaults.Friend).toBe(0)
  expect(payload.config.identity_model_ratios.Friend).toBeUndefined()
  expect(
    section('Service defaults (S)').getByLabelText('Exact key')
  ).toHaveValue('ServerReadback')
  expect(api.get).toHaveBeenCalledTimes(2)
  expect(api.post).toHaveBeenCalledWith(
    `${identityServicePath}/validate`,
    { config: payload.config },
    expect.anything()
  )
  expect(vi.mocked(api.post).mock.invocationCallOrder[0]).toBeLessThan(
    vi.mocked(api.put).mock.invocationCallOrder[0]
  )
  expect(vi.mocked(api.put).mock.invocationCallOrder[0]).toBeLessThan(
    vi.mocked(api.get).mock.invocationCallOrder[1]
  )
  expect(
    screen.queryByRole('button', { name: /activate|enable identity/i })
  ).not.toBeInTheDocument()
})

test('add exact unknown service/model, delete S override and scope identity; validator sees unchanged keys', async () => {
  const user = await load()
  const s = await selectGroup(user, service, ' New service ')
  await user.click(s.getByRole('button', { name: 'Add row' }))
  await user.type(s.getByLabelText('Exact key'), ' Model.X ')
  await user.type(s.getByLabelText('Ratio'), '1')
  await user.click(s.getByRole('button', { name: 'Remove override / inherit' }))
  await user.click(s.getByLabelText('Explicitly open model'))
  const scopes = section('Model identity eligibility')
  await user.click(scopes.getByRole('button', { name: 'Remove identity' }))
  await user.selectOptions(scopes.getByLabelText('Eligibility mode'), 'public')
  vi.mocked(api.post).mockRejectedValueOnce(
    httpError(400, 'invalid exact key: New service')
  )
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText('invalid exact key: New service')
  const cfg = (
    vi.mocked(api.post).mock.calls[0][1] as { config: IdentityServiceConfig }
  ).config
  expect(cfg.service_models[' New service '][' Model.X ']).toEqual({
    enabled: true,
  })
  expect(cfg.model_identity_scopes.m).toEqual({
    mode: 'public',
    identities: [],
  })
  expect(api.put).not.toHaveBeenCalled()
  expect(s.getByLabelText('Exact key')).toHaveValue(' Model.X ')
})

test('defaults and eligibility rows support add, edit and delete without fixed enums', async () => {
  const user = await load()
  const defaults = section('Identity defaults (D)')
  await user.click(defaults.getByRole('button', { name: 'Add row' }))
  const row = within(
    defaults.getAllByRole('group', { name: 'Configuration row' })[2]
  )
  await user.type(row.getByLabelText('Exact key'), 'New identity')
  await user.type(row.getByLabelText('Ratio'), '0')
  const scopes = section('Model identity eligibility')
  await user.click(scopes.getByRole('button', { name: 'Add identity' }))
  await user.type(scopes.getAllByLabelText('Exact identity')[1], 'New identity')
  await user.click(scopes.getByRole('button', { name: 'Add row' }))
  const extra = within(
    scopes.getAllByRole('group', { name: 'Configuration row' })[1]
  )
  await user.type(extra.getByLabelText('Exact key'), 'unreleased')
  await user.click(extra.getByRole('button', { name: 'Delete row' }))
  await user.click(screen.getByRole('button', { name: 'Validate draft' }))
  await screen.findByText('Structure valid. Activation remains blocked.')
  const cfg = (
    vi.mocked(api.post).mock.calls[0][1] as { config: IdentityServiceConfig }
  ).config
  expect(cfg.identity_defaults['New identity']).toBe(0)
  expect(cfg.model_identity_scopes.m.identities).toEqual([
    'Friend',
    'New identity',
  ])
  expect(cfg.model_identity_scopes.unreleased).toBeUndefined()
  expect(api.put).not.toHaveBeenCalled()
  expect(screen.getByText('synthetic activation gate')).toBeVisible()
})

test('409 keeps draft and old revision, blocks retry, and reload requires explicit discard', async () => {
  const user = await load()
  const s = await selectGroup(user, service, 'Default')
  await user.type(s.getByLabelText('Ratio'), '0')
  vi.mocked(api.put).mockRejectedValueOnce(httpError(409, 'revision conflict'))
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText(/Revision conflict. Draft kept/)
  expect(s.getByLabelText('Ratio')).toHaveValue('0')
  expect(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  ).toBeDisabled()
  expect(api.get).toHaveBeenCalledTimes(1)
  expect(api.put).toHaveBeenCalledTimes(1)
  await user.click(
    screen.getByRole('button', { name: 'Discard draft and reload' })
  )
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(s.getByLabelText('Ratio')).toHaveValue('0')
  await user.click(
    screen.getByRole('button', { name: 'Discard draft and reload' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Confirm discard and reload' })
  )
  await waitFor(() => expect(s.getByLabelText('Ratio')).toHaveValue(''))
  expect(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  ).toBeEnabled()
})

test.each([400, 422])(
  'validate HTTP %s is failure, not activation permission or save success',
  async (status) => {
    const user = await load()
    vi.mocked(api.post).mockRejectedValueOnce(
      httpError(status, 'synthetic validation refusal')
    )
    await user.click(
      screen.getByRole('button', { name: 'Validate and save legacy draft' })
    )
    await screen.findByText('synthetic validation refusal')
    expect(api.put).not.toHaveBeenCalled()
    expect(
      screen.queryByText(
        'Saved legacy configuration and verified server readback.'
      )
    ).not.toBeInTheDocument()
    expect(screen.getByText('synthetic activation gate')).toBeVisible()
  }
)

test.each(['-1', 'Infinity', 'NaN', '1e400', '1e-400', '-1e-400', ' '])(
  'invalid ratio %s cannot silently become free or reach API',
  async (text) => {
    const user = await load()
    const s = await selectGroup(user, service, 'Default')
    await user.type(s.getByLabelText('Ratio'), text)
    await user.click(screen.getByRole('button', { name: 'Validate draft' }))
    await screen.findByText('Ratios must be finite nonnegative numbers')
    expect(api.post).not.toHaveBeenCalled()
    expect(api.put).not.toHaveBeenCalled()
    expect(s.getByLabelText('Ratio')).toHaveValue(text)
  }
)

test('readback failure retains draft and never reports a completed save', async () => {
  const user = await load()
  const s = await selectGroup(user, service, 'Default')
  await user.type(s.getByLabelText('Ratio'), '0')
  vi.mocked(api.get).mockRejectedValueOnce(
    new Error('synthetic network failure')
  )
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText(
    'Save may have committed; readback failed. Draft kept. Reload before saving again.'
  )
  expect(s.getByLabelText('Ratio')).toHaveValue('0')
  expect(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  ).toBeDisabled()
  expect(
    screen.queryByText(
      'Saved legacy configuration and verified server readback.'
    )
  ).not.toBeInTheDocument()
})

test('pending save locks edits and group selection until real GET completes', async () => {
  const user = await load()
  const s = await selectGroup(user, service, 'Pro')
  let resolve!: (value: unknown) => void
  vi.mocked(api.get).mockImplementationOnce(
    () =>
      new Promise((r) => {
        resolve = r
      }) as never
  )
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await waitFor(() => expect(api.get).toHaveBeenCalledTimes(2))
  expect(s.getByLabelText('Selected exact group')).toBeDisabled()
  expect(s.getByLabelText('Ratio')).toBeDisabled()
  expect(
    screen.queryByText(
      'Saved legacy configuration and verified server readback.'
    )
  ).not.toBeInTheDocument()
  await act(async () =>
    resolve({ data: { success: true, data: { ...fixture, revision: 8 } } })
  )
  await screen.findByText(
    'Saved legacy configuration and verified server readback.'
  )
  expect(s.getByLabelText('Selected exact group')).toHaveValue('Pro')
  expect(s.getByLabelText('Ratio')).toHaveValue('1')
})

test('initial load failure exposes retry rather than an editable empty config', async () => {
  vi.mocked(api.get).mockRejectedValueOnce(new Error('synthetic load failure'))
  mount()
  await screen.findByText('Could not load identity/service settings.')
  expect(
    screen.queryByRole('button', { name: 'Validate draft' })
  ).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
  await screen.findByText(/^Configuration revision/)
})

test.each([400, 422, 503])(
  'PUT HTTP %s keeps draft, cannot report success or auto retry',
  async (status) => {
    const user = await load()
    vi.mocked(api.put).mockRejectedValueOnce(
      httpError(status, 'synthetic save refusal')
    )
    await user.click(
      screen.getByRole('button', { name: 'Validate and save legacy draft' })
    )
    await screen.findByText('synthetic save refusal')
    expect(api.put).toHaveBeenCalledTimes(1)
    expect(api.get).toHaveBeenCalledTimes(1)
    expect(
      screen.getByRole('button', { name: 'Validate and save legacy draft' })
    ).toBeDisabled()
  }
)

test('transport failure after PUT is uncertain and cannot be retried without reload', async () => {
  const user = await load()
  vi.mocked(api.put).mockRejectedValueOnce(new Error('synthetic disconnected'))
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText('synthetic disconnected')
  expect(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  ).toBeDisabled()
  expect(api.get).toHaveBeenCalledTimes(1)
})

test('HTTP 200 validation failure is not treated as structural success', async () => {
  const user = await load()
  vi.mocked(api.post).mockResolvedValueOnce({
    data: { success: false, message: 'synthetic envelope failure' },
  })
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText('synthetic envelope failure')
  expect(api.put).not.toHaveBeenCalled()
})

test('HTTP 200 PUT failure is not treated as saved and does not trigger readback', async () => {
  const user = await load()
  vi.mocked(api.put).mockResolvedValueOnce({
    data: { success: false, message: 'synthetic envelope failure' },
  })
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText('synthetic envelope failure')
  expect(api.get).toHaveBeenCalledTimes(1)
})

test('duplicate exact keys do not silently overwrite existing defaults', async () => {
  const user = await load()
  const s = section('Service defaults (S)')
  await user.click(s.getByRole('button', { name: 'Add row' }))
  const row = within(s.getAllByRole('group', { name: 'Configuration row' })[2])
  await user.type(row.getByLabelText('Exact key'), 'Default')
  await user.type(row.getByLabelText('Ratio'), '0')
  await user.click(screen.getByRole('button', { name: 'Validate draft' }))
  await screen.findByText('Duplicate exact keys must be resolved')
  expect(api.post).not.toHaveBeenCalled()
})

test('migration digest is preserved until the user explicitly discards provenance', async () => {
  vi.mocked(api.get).mockResolvedValueOnce({
    data: {
      success: true,
      data: { ...fixture, migration_source_digest: 'a'.repeat(64) },
    },
  })
  vi.mocked(api.post).mockRejectedValueOnce(
    httpError(
      400,
      'migration_source_digest requires its explicit migration source'
    )
  )
  const user = await load()
  await user.click(screen.getByRole('button', { name: 'Validate draft' }))
  await screen.findByText(
    'migration_source_digest requires its explicit migration source'
  )
  expect(
    (vi.mocked(api.post).mock.calls[0][1] as { config: IdentityServiceConfig })
      .config.migration_source_digest
  ).toBe('a'.repeat(64))
  await user.click(
    screen.getByRole('button', {
      name: 'Discard migration proof for manual draft',
    })
  )
  await user.click(screen.getByRole('button', { name: 'Validate draft' }))
  await waitFor(() => expect(api.post).toHaveBeenCalledTimes(2))
  expect(
    (vi.mocked(api.post).mock.calls[1][1] as { config: IdentityServiceConfig })
      .config.migration_source_digest
  ).toBe('')
})

test('a late initial GET after Root loses authorization cannot expose the editor', async () => {
  let resolve!: (value: unknown) => void
  vi.mocked(api.get).mockImplementationOnce(
    () =>
      new Promise((r) => {
        resolve = r
      }) as never
  )
  const view = mount()
  await screen.findByText('Loading settings...')
  await act(async () =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 124, username: 'ordinary', role: 1 })
  )
  await act(async () => resolve({ data: { success: true, data: fixture } }))
  expect(view.container).toBeEmptyDOMElement()
})

test('Chinese and English retain the same zero and inheritance semantics', async () => {
  i18next.addResourceBundle('en', 'translation', en.translation, true, true)
  i18next.addResourceBundle('zh', 'translation', zh.translation, true, true)
  try {
    await i18next.changeLanguage('zh')
    mount()
    await screen.findByText('身份与服务集中配置')
    expect(
      screen.getByText(
        '覆盖缺失时继承默认值。0 为免费，1 为显式不折扣；删除覆盖才能恢复继承。'
      )
    ).toBeVisible()
    await act(async () => {
      await i18next.changeLanguage('en')
    })
    expect(screen.getByText('Identity and service configuration')).toBeVisible()
  } finally {
    await act(async () => {
      await i18next.changeLanguage('en')
    })
  }
})

// JSON wire copies deliberately preserve own __proto__, unlike object literals.
const exactKeys = [
  '__proto__',
  'constructor',
  'prototype',
  '模型.é',
  '模型.e\u0301',
  '模型\u200dX',
]
// Wire serialization is the tested HTTP boundary, not an in-memory clone.
// oxlint-disable-next-line unicorn/prefer-structured-clone
const wire = <T,>(value: T): T => JSON.parse(JSON.stringify(value))
function exactConfig(): IdentityServiceConfig {
  return {
    ...wire(fixture),
    service_defaults: Object.fromEntries([
      ['Default', 1],
      ...exactKeys.map((key) => [key, 2]),
    ]),
    identity_defaults: Object.fromEntries(exactKeys.map((key) => [key, 0])),
    service_models: Object.fromEntries(
      exactKeys.map((key) => [
        key,
        Object.fromEntries(
          exactKeys.map((model) => [model, { enabled: true, ratio: 0 }])
        ),
      ])
    ),
    identity_model_ratios: Object.fromEntries(
      exactKeys.map((key) => [
        key,
        Object.fromEntries(exactKeys.map((model) => [model, 1])),
      ])
    ),
    model_identity_scopes: Object.fromEntries(
      exactKeys.map((key) => [
        key,
        { mode: 'restricted', identities: [...exactKeys] },
      ])
    ),
  }
}
function storedBoundary(initial: IdentityServiceConfig) {
  let stored = wire(initial)
  vi.mocked(api.get).mockImplementation(async () => ({
    data: { success: true, data: wire(stored) },
  }))
  vi.mocked(api.put).mockImplementation(async (_path, body) => {
    const payload = wire(body) as {
      expected_revision: number
      config: IdentityServiceConfig
    }
    expect(payload.expected_revision).toBe(stored.revision)
    stored = { ...payload.config, revision: stored.revision + 1 }
    return { data: { success: true } }
  })
  return () => wire(stored)
}
function exactRow(name: string, key: string) {
  const rows = section(name).getAllByRole('group', {
    name: 'Configuration row',
  })
  const found = rows.find(
    (row) =>
      (within(row).getByLabelText('Exact key') as HTMLInputElement).value ===
      key
  )
  if (!found) throw new Error(`Missing exact row: ${key}`)
  return within(found)
}
async function saveExact(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    screen.getByRole('button', { name: 'Validate and save legacy draft' })
  )
  await screen.findByText(
    'Saved legacy configuration and verified server readback.'
  )
}
test('all five maps preserve own dangerous and distinct Unicode keys through unrelated edit, validate, CAS PUT and GET', async () => {
  const prototype = Object.getOwnPropertyDescriptors(Object.prototype)
  const initial = exactConfig()
  const stored = storedBoundary(initial)
  const user = await load()
  const ratio = exactRow('Service defaults (S)', 'Default').getByLabelText(
    'Ratio'
  )
  await user.clear(ratio)
  await user.type(ratio, '3')
  await saveExact(user)
  const expected = wire(initial)
  expected.service_defaults.Default = 3
  expected.revision = 8
  expect(stored()).toEqual(expected)
  expect(
    (vi.mocked(api.post).mock.calls[0][1] as { config: IdentityServiceConfig })
      .config
  ).toEqual({ ...expected, revision: 7 })
  expect(api.get).toHaveBeenCalledTimes(2)
  for (const key of exactKeys) {
    expect(
      exactRow('Service defaults (S)', key).getByLabelText('Ratio')
    ).toHaveValue('2')
    expect(
      exactRow('Identity defaults (D)', key).getByLabelText('Ratio')
    ).toHaveValue('0')
    await selectGroup(user, service, key)
    expect(exactRow(service, key).getByLabelText('Ratio')).toHaveValue('0')
    await selectGroup(user, identity, key)
    expect(exactRow(identity, key).getByLabelText('Ratio')).toHaveValue('1')
    expect(
      exactRow('Model identity eligibility', key)
        .getAllByLabelText('Exact identity')
        .map((node) => (node as HTMLInputElement).value)
    ).toEqual(exactKeys)
  }
  expect(Object.getOwnPropertyDescriptors(Object.prototype)).toEqual(prototype)
})
test.each([
  ['service_defaults', 'Service defaults (S)', ''],
  ['identity_defaults', 'Identity defaults (D)', ''],
  ['service_models', service, 'Default'],
  ['identity_model_ratios', identity, 'Friend'],
  ['model_identity_scopes', 'Model identity eligibility', ''],
] as const)(
  'explicit __proto__ add edit delete survives readback in %s',
  async (field, name, group) => {
    const prototype = Object.getOwnPropertyDescriptors(Object.prototype)
    const stored = storedBoundary(fixture)
    const user = await load()
    if (group) await selectGroup(user, name, group)
    await user.click(section(name).getByRole('button', { name: 'Add row' }))
    const rows = section(name).getAllByRole('group', {
      name: 'Configuration row',
    })
    const last = rows.at(-1)
    if (!last) throw new Error('Missing new row')
    const row = within(last)
    await user.type(row.getByLabelText('Exact key'), '__proto__')
    if (field !== 'model_identity_scopes') {
      await user.type(row.getByLabelText('Ratio'), '0')
    }
    await saveExact(user)
    const value = () => {
      const map = stored()[field] as Record<string, unknown>
      return group ? (map[group] as Record<string, unknown>) : map
    }
    expect(Object.hasOwn(value(), '__proto__')).toBe(true)
    const added = exactRow(name, '__proto__')
    if (field === 'model_identity_scopes') {
      await user.selectOptions(
        added.getByLabelText('Eligibility mode'),
        'public'
      )
    } else {
      await user.clear(added.getByLabelText('Ratio'))
      await user.type(added.getByLabelText('Ratio'), '1')
    }
    await saveExact(user)
    let expected: unknown = 1
    if (field === 'model_identity_scopes') {
      expected = { mode: 'public', identities: [] }
    }
    if (field === 'service_models') expected = { enabled: false, ratio: 1 }
    expect(value()['__proto__']).toEqual(expected)
    await user.click(
      exactRow(name, '__proto__').getByRole('button', { name: 'Delete row' })
    )
    await saveExact(user)
    expect(Object.hasOwn(value(), '__proto__')).toBe(false)
    expect(api.get).toHaveBeenCalledTimes(4)
    expect(Object.getOwnPropertyDescriptors(Object.prototype)).toEqual(
      prototype
    )
  }
)
