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
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { channelsQueryKeys } from '../../lib/channel-actions'
import { channelSchema } from '../../types'
import { ChannelsProvider } from '../channels-provider'
import { ChannelMutateDrawer } from '../drawers/channel-mutate-drawer'

vi.mock('@/lib/api', () => ({
  api: { get: vi.fn(), put: vi.fn(), post: vi.fn() },
  get2FAStatus: vi
    .fn()
    .mockResolvedValue({ success: true, data: { enabled: false } }),
  getPasskeyStatus: vi
    .fn()
    .mockResolvedValue({ success: true, data: { enabled: false } }),
}))
const channel = channelSchema.parse({
  id: 1,
  revision: 'revision-one',
  type: 1,
  key: '',
  name: 'Alpha',
  status: 1,
  created_time: 0,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
  models: 'gpt-4',
  group: 'default',
})
vi.setConfig({ testTimeout: 30000 })
const close = vi.fn()
beforeEach(() => {
  localStorage.clear()
  useAuthStore.setState((state) => ({
    auth: {
      ...state.auth,
      user: { id: 1, username: 'admin', role: 100 } as NonNullable<
        typeof state.auth.user
      >,
    },
  }))
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url === '/api/channel/1') {
      return { data: { success: true, data: channel } }
    }
    const data = url === '/api/channel/models' ? [{ id: 'gpt-4' }] : []
    return { data: { success: true, data } }
  })
  vi.mocked(api.put).mockResolvedValue({
    data: {
      success: true,
      committed: true,
      data: { ...channel, revision: 'revision-two' },
    },
  })
})
function mount(
  initialRow: typeof channel | null = { ...channel, revision: 'stale-list' }
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const drawer = (row: typeof channel | null, open = true) => (
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <ChannelMutateDrawer
          open={open}
          onOpenChange={close}
          currentRow={row}
        />
      </ChannelsProvider>
    </QueryClientProvider>
  )
  const view = render(drawer(initialRow))
  return {
    client,
    ...view,
    changeTarget: (row: typeof channel | null, open = true) =>
      view.rerender(drawer(row, open)),
  }
}
test('full edit sends the detail revision rather than list revision after real input and save', async () => {
  mount()
  const user = userEvent.setup()
  const name = await screen.findByDisplayValue('Alpha')
  await user.clear(name)
  await user.type(name, 'Changed')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith(
      '/api/channel/',
      expect.objectContaining({
        id: 1,
        name: 'Changed',
        revision: 'revision-one',
      }),
      expect.anything()
    )
  )
  await waitFor(() => expect(close).toHaveBeenCalledWith(false))
})
test('conflict keeps edits visible and explicit refresh reloads the new revision', async () => {
  vi.mocked(api.put).mockRejectedValue({ response: { status: 409 } })
  mount()
  const user = userEvent.setup()
  const name = await screen.findByDisplayValue('Alpha')
  await user.clear(name)
  await user.type(name, 'Unsaved')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await screen.findByText(/Channel changed. Refresh and review/)
  expect(screen.getByDisplayValue('Unsaved')).toBeInTheDocument()
  expect(close).not.toHaveBeenCalled()
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: { ...channel, name: 'Latest', revision: 'revision-two' },
    },
  })
  await user.click(
    screen.getByRole('button', { name: 'Refresh channel and discard edits' })
  )
  await screen.findByDisplayValue('Latest')
  vi.mocked(api.put).mockResolvedValue({
    data: { success: true, data: channel },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenLastCalledWith(
      '/api/channel/',
      expect.objectContaining({ revision: 'revision-two' }),
      expect.anything()
    )
  )
})
test('detail GET failure exposes refresh and never writes without a revision', async () => {
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url === '/api/channel/1') throw new Error('offline')
    return { data: { success: true, data: [] } }
  })
  mount()
  await screen.findByRole('button', {
    name: 'Refresh channel and discard edits',
  })
  expect(screen.getByRole('button', { name: 'Update Channel' })).toBeDisabled()
  expect(api.put).not.toHaveBeenCalled()
})

async function openMissingModelsConfirmation() {
  const user = userEvent.setup()
  const name = await screen.findByDisplayValue('Alpha')
  await user.clear(name)
  await user.type(name, 'Unsaved')
  await user.click(
    within(
      screen
        .getByText('Model Mapping', { selector: 'label' })
        .closest('[data-slot="form-item"]') as HTMLElement
    ).getByRole('button', { name: 'Fill Template' })
  )
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await screen.findByRole('button', { name: 'Submit directly' })
  return user
}

test('detail refresh while confirmation waits explicitly cancels the old full edit instead of writing old models with R2', async () => {
  const { client } = mount()
  const user = await openMissingModelsConfirmation()
  vi.mocked(api.get).mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/channel/1'
          ? {
              ...channel,
              revision: 'revision-two',
              models: 'gpt-4,concurrent-model',
              name: 'Concurrent',
            }
          : [],
    },
  }))
  // The real query/GET delivers the same detail update as a reconnect refetch.
  await act(async () => {
    await client.refetchQueries({ queryKey: channelsQueryKeys.detail(1) })
  })
  await screen.findByDisplayValue('Concurrent')
  // A stale confirmation must not remain actionable. On the unfixed code,
  // accept it to expose the actual unsafe HTTP body rather than only a UI failure.
  const staleConfirm = screen.queryByRole('button', { name: 'Submit directly' })
  if (staleConfirm) await user.click(staleConfirm)
  expect(api.put).not.toHaveBeenCalled()
  expect(
    screen.getByText(
      'Channel changed. Pending submission cancelled; review the refreshed values before saving.'
    )
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Submit directly' })
  ).not.toBeInTheDocument()
})

test('confirmation cancel preserves edit inputs and sends no write', async () => {
  mount()
  const user = await openMissingModelsConfirmation()
  await user.click(screen.getByRole('button', { name: 'Go back and edit' }))
  expect(screen.getByDisplayValue('Unsaved')).toBeInTheDocument()
  expect(api.put).not.toHaveBeenCalled()
})

test.each(['close', 'switch', 'unmount'] as const)(
  'pending edit confirmation is safe on %s',
  async (action) => {
    const view = mount()
    await openMissingModelsConfirmation()
    if (action === 'unmount') view.unmount()
    else if (action === 'close') view.changeTarget(channel, false)
    else {
      const next = {
        ...channel,
        id: 2,
        name: 'Other',
        revision: 'other-revision',
      }
      vi.mocked(api.get).mockImplementation(async (url) => ({
        data: { success: true, data: url === '/api/channel/2' ? next : [] },
      }))
      view.client.setQueryData(channelsQueryKeys.detail(2), {
        success: true,
        data: next,
      })
      view.changeTarget(next)
      await screen.findByDisplayValue('Other')
    }
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Submit directly' })
      ).not.toBeInTheDocument()
    )
    expect(api.put).not.toHaveBeenCalled()
    expect(api.post).not.toHaveBeenCalled()
  }
)

async function openCreateConfirmation() {
  const user = userEvent.setup()
  await user.type(
    document.querySelector('input[name="name"]') as HTMLInputElement,
    'Created'
  )
  await user.type(screen.getByLabelText('API Key *'), 'test-only-key')
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Fill All Models' })
    ).toBeEnabled()
  )
  await user.click(screen.getByRole('button', { name: 'Fill All Models' }))
  await user.click(
    within(
      screen
        .getByText('Model Mapping', { selector: 'label' })
        .closest('[data-slot="form-item"]') as HTMLElement
    ).getByRole('button', { name: 'Fill Template' })
  )
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() => {
    const errors = [
      ...document.querySelectorAll('[data-slot="form-message"]'),
    ].map((node) => node.textContent)
    expect(errors).toEqual([])
    expect(
      screen.getByRole('button', { name: 'Add and submit' })
    ).toBeInTheDocument()
  })
  return user
}

test('create confirmation adds selected models and posts without edit revision', async () => {
  vi.mocked(api.post).mockResolvedValue({ data: { success: true } })
  mount(null)
  const user = await openCreateConfirmation()
  await user.click(screen.getByRole('button', { name: 'Add and submit' }))
  await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1))
  expect(api.put).not.toHaveBeenCalled()
  const payload = vi.mocked(api.post).mock.calls[0][1]
  expect(JSON.stringify(payload)).toContain('Created')
  expect(JSON.stringify(payload)).toContain('gpt-3.5-turbo')
  expect(payload).not.toHaveProperty('revision')
})

test('create confirmation cannot write after switching into edit', async () => {
  const view = mount(null)
  await openCreateConfirmation()
  view.changeTarget(channel)
  await screen.findByDisplayValue('Alpha')
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'Add and submit' })
    ).not.toBeInTheDocument()
  )
  expect(api.put).not.toHaveBeenCalled()
  expect(api.post).not.toHaveBeenCalled()
})
