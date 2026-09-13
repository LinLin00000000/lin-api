import { toast } from 'sonner'
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
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  handleChannelCommitResult,
  updateChannel,
  updateChannelFields,
  updateChannelStatus,
  batchUpdateChannelStatus,
  manageMultiKeys,
} from '../../api'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToUpdatePayload,
} from '../channel-form'

vi.mock('@/lib/api', () => ({
  api: { get: vi.fn(), put: vi.fn(), post: vi.fn() },
}))
beforeEach(() => {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: { id: 1, models: 'm', revision: 'revision-a' },
    },
  })
  vi.mocked(api.put).mockResolvedValue({
    data: { success: true, data: { id: 1, revision: 'revision-b' } },
  })
})
test('partial committed degraded batch preserves failure and outcomes without retry', () => {
 const input = {success:false,committed:true,degraded:true,partial:true,data:{failed_channel_ids:[2],outcomes:[{channel_id:1,committed:true,degraded:true},{channel_id:2,committed:false,degraded:false}]}}
 expect(handleChannelCommitResult(input)).toEqual(input)
 expect(api.post).not.toHaveBeenCalled()
})

test('missing full-edit revision never sends PUT or silently fetches a new revision', async () => {
  await expect(
    updateChannel(1, { name: 'edit', revision: '' })
  ).rejects.toThrow(/revision missing/)
  expect(api.put).not.toHaveBeenCalled()
  expect(api.get).not.toHaveBeenCalled()
})
test('small field edit reads latest revision and preserves returned revision', async () => {
  const result = await updateChannelFields(1, { weight: 0 })
  expect(api.put).toHaveBeenCalledWith(
    '/api/channel/',
    { id: 1, weight: 0, revision: 'revision-a' },
    expect.anything()
  )
  expect(result.data?.revision).toBe('revision-b')
})
test('changed model snapshot refuses a replacement before PUT', async () => {
  await expect(
    updateChannelFields(1, { models: 'replacement' }, 'old')
  ).rejects.toThrow(/Channel changed/)
  expect(api.put).not.toHaveBeenCalled()
})
test('small edit GET failure does not submit without CAS', async () => {
  vi.mocked(api.get).mockRejectedValue(new Error('offline'))
  await expect(updateChannelFields(1, { weight: 1 })).rejects.toThrow('offline')
  expect(api.put).not.toHaveBeenCalled()
})
test('explicit default priority zero remains zero in full update payload', () => {
  expect(
    transformFormDataToUpdatePayload(
      { ...CHANNEL_FORM_DEFAULT_VALUES, priority: 0 },
      1
    ).priority
  ).toBe(0)
})
test.each([
  ['single status', () => updateChannelStatus(1, 1), '/api/channel/1/status'],
  [
    'batch status',
    () => batchUpdateChannelStatus([1], 1),
    '/api/channel/status/batch',
  ],
  [
    'multi-key',
    () => manageMultiKeys({ channel_id: 1, action: 'enable_all_keys' }),
    '/api/channel/multi_key/manage',
  ],
] as const)(
  'committed degraded %s shows a warning and keeps legacy success compatibility',
  async (_name, invoke, path) => {
    const warning = vi.spyOn(toast, 'warning')
    vi.mocked(api.post).mockResolvedValue({
      data: {
        success: false,
        committed: true,
        degraded: true,
        refresh_error: 'cache unavailable',
      },
    })
    const result = await invoke()
    expect(result.success).toBe(true)
    expect(warning).toHaveBeenCalledWith(
      expect.stringContaining('Do not resend')
    )
    expect(api.post).toHaveBeenCalledWith(
      path,
      expect.anything(),
      expect.anything()
    )
    expect(api.put).not.toHaveBeenCalled()
    expect(api.get).not.toHaveBeenCalled()
  }
)
