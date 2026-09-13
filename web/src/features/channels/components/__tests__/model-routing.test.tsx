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

import { ModelRouting } from '../model-routing'

vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), put: vi.fn() } }))
const rows = [
  {
    channel_id: 1,
    name: 'Alpha',
    tag: 'primary',
    status: 1,
    priority: 5,
    weight: 10,
  },
  {
    channel_id: 2,
    name: 'Paused',
    tag: 'backup',
    status: 2,
    priority: -1,
    weight: 0,
  },
]
function mount(canWrite = true) {
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
      <ModelRouting canWrite={canWrite} />
    </QueryClientProvider>
  )
}
beforeEach(() => {
  vi.mocked(api.get).mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/options')
        ? [
            { group: 'Default', model: 'm exact ' },
            { group: 'Pro', model: 'm exact ' },
            { group: 'Default', model: 'other' },
          ]
        : rows,
    },
  }))
  vi.mocked(api.put).mockResolvedValue({
    data: { success: true, committed: true },
  })
})
async function selectDefault() {
  const user = userEvent.setup()
  await screen.findByRole('option', { name: 'Default' })
  await user.selectOptions(screen.getByLabelText('Group'), 'Default')
  await user.selectOptions(screen.getByLabelText('Physical model'), 'm exact ')
  await screen.findByText('Alpha')
  return user
}
test('edits exact route priority, saves four fields and reads back disabled channels', async () => {
  mount()
  const user = await selectDefault()
  const row = screen.getByRole('row', { name: /Alpha/ })
  await user.click(
    within(row).getByRole('button', { name: 'Priority for Alpha' })
  )
  await user.clear(within(row).getByRole('textbox'))
  await user.type(within(row).getByRole('textbox'), '-7')
  await user.keyboard('{Enter}')
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: [{ ...rows[0], priority: -7 }, rows[1]] },
  })
  await user.click(within(row).getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith(
      '/api/channel/model_priority',
      { group: 'Default', model: 'm exact ', channel_id: 1, priority: -7 },
      expect.anything()
    )
  )
  await screen.findByText('Saved and read back')
  expect(
    screen.getByRole('button', { name: 'Priority for Alpha' })
  ).toHaveTextContent('-7')
  expect(api.get).toHaveBeenLastCalledWith(
    '/api/channel/model_priority',
    expect.objectContaining({ params: { group: 'Default', model: 'm exact ' } })
  )
  expect(screen.getByText('Paused')).toBeInTheDocument()
  expect(screen.getByText('Disabled')).toBeInTheDocument()
})
test('switching group clears old rows and displays the new empty result', async () => {
  mount()
  const user = await selectDefault()
  await user.selectOptions(screen.getByLabelText('Group'), 'Pro')
  expect(screen.queryByText('Alpha')).not.toBeInTheDocument()
  vi.mocked(api.get).mockResolvedValue({ data: { success: true, data: [] } })
  await user.selectOptions(screen.getByLabelText('Physical model'), 'm exact ')
  await screen.findByText('No candidate channels for this group and model')
})
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

test('pending Default PUT cannot overwrite Pro rows or saving state after group switch', async () => {
  mount()
  const user = await selectDefault()
  const pending = deferred<{ data: { success: boolean; committed: boolean } }>()
  vi.mocked(api.put).mockReturnValue(pending.promise)
  await user.click(
    screen.getByRole('button', { name: 'Increase Priority for Alpha' })
  )
  await user.click(
    within(screen.getByRole('row', { name: /Alpha/ })).getByRole('button', {
      name: 'Save',
    })
  )
  expect(
    screen.getByRole('button', { name: 'Priority for Alpha' })
  ).toBeDisabled()
  vi.mocked(api.get).mockImplementation(async (_url, config) => ({
    data: {
      success: true,
      data:
        config?.params.group === 'Pro'
          ? [{ ...rows[0], name: 'Pro channel', priority: 20 }]
          : [{ ...rows[0], priority: 6 }],
    },
  }))
  await user.selectOptions(screen.getByLabelText('Group'), 'Pro')
  await user.selectOptions(screen.getByLabelText('Physical model'), 'm exact ')
  await screen.findByText('Pro channel')
  await act(async () =>
    pending.resolve({ data: { success: true, committed: true } })
  )
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/channel/model_priority',
      expect.objectContaining({
        params: { group: 'Default', model: 'm exact ' },
      })
    )
  )
  expect(screen.queryByText('Alpha')).not.toBeInTheDocument()
  expect(screen.queryByText('Saved and read back')).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Priority for Pro channel' })
  ).toHaveTextContent('20')
  await user.click(
    screen.getByRole('button', { name: 'Increase Priority for Pro channel' })
  )
  expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
})

test('late GET from previous model cannot replace the selected model', async () => {
  mount()
  const user = await selectDefault()
  const pending = deferred<{ data: { success: boolean; data: typeof rows } }>()
  vi.mocked(api.get).mockReturnValueOnce(pending.promise)
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: [{ ...rows[1], name: 'Other model route' }] },
  })
  await user.selectOptions(screen.getByLabelText('Physical model'), 'other')
  await screen.findByText('Other model route')
  await act(async () =>
    pending.resolve({ data: { success: true, data: rows } })
  )
  expect(screen.queryByText('Alpha')).not.toBeInTheDocument()
  expect(screen.getByText('Other model route')).toBeInTheDocument()
  expect(screen.getByText('No enabled candidate channels')).toBeInTheDocument()
})

test('read-only user sees disabled candidates but no save action', async () => {
  mount(false)
  await selectDefault()
  expect(
    screen.getByRole('button', { name: 'Priority for Alpha' })
  ).toBeDisabled()
  expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
  expect(screen.getByText('Paused')).toBeInTheDocument()
})

test('failed PUT does not show success', async () => {
  vi.mocked(api.put).mockResolvedValue({
    data: { success: false, message: 'Rejected' },
  })
  mount()
  const user = await selectDefault()
  const row = screen.getByRole('row', { name: /Alpha/ })
  await user.click(
    within(row).getByRole('button', { name: 'Increase Priority for Alpha' })
  )
  await user.click(within(row).getByRole('button', { name: 'Save' }))
  await screen.findByText('Rejected')
  expect(screen.queryByText('Saved and read back')).not.toBeInTheDocument()
})
test('committed degraded write warns without offering to resend when readback fails', async () => {
  mount()
  const user = await selectDefault()
  vi.mocked(api.put).mockResolvedValue({
    data: {
      success: true,
      committed: true,
      degraded: true,
      refresh_error: 'cache unavailable',
    },
  })
  vi.mocked(api.get).mockRejectedValue(new Error('offline'))
  const row = screen.getByRole('row', { name: /Alpha/ })
  await user.click(
    within(row).getByRole('button', { name: 'Increase Priority for Alpha' })
  )
  await user.click(within(row).getByRole('button', { name: 'Save' }))
  await screen.findByText(/Saved, but cache refresh failed/)
  await screen.findByText(/Unable to read routes/)
  expect(api.put).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
})
