import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import type { SystemTask } from '@/features/system-settings/types'
import { api } from '@/lib/api'

import { SystemTasksPanel } from '../system-tasks-panel'

vi.mock('@/lib/api', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}))

const clients: QueryClient[] = []
afterEach(() => clients.splice(0).forEach((client) => client.clear()))

function task(
  result: unknown,
  status: SystemTask['status'] = 'succeeded'
): SystemTask {
  return {
    id: 1,
    task_id: 'history-1',
    type: 'model_update',
    status,
    created_at: 1,
    updated_at: 1,
    state: { progress: 100 },
    result: result as SystemTask['result'],
    error: status === 'failed' ? 'SECRET raw task error' : '',
  }
}
function mount(tasks: SystemTask[]) {
  vi.mocked(api.get).mockResolvedValue({ data: { success: true, data: tasks } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <SystemTasksPanel />
    </QueryClientProvider>
  )
}
async function historyRow() {
  return screen.findByRole('row', { name: /Batch upstream model update/ })
}
function expectNotGreen(row: HTMLElement) {
  expect(row.querySelector('[data-slot="badge"]')).not.toHaveClass(
    'bg-emerald-50'
  )
  expect(within(row).getByRole('progressbar').className).not.toContain(
    'emerald'
  )
  expect(within(row).getAllByRole('cell').at(-1)).not.toHaveTextContent(
    /^\s*-\s*$/
  )
  expect(row.outerHTML).not.toContain('SECRET')
}

test.each(['failed', 'succeeded'] as const)(
  'incomplete scan with persisted %s status is not green and explains failure',
  async (status) => {
    mount([
      task(
        { scan_complete: false, checked_channels: 0, failed_channels: 0 },
        status
      ),
    ])
    const row = await historyRow()
    expectNotGreen(row)
    expect(within(row).getByText('Scan incomplete.')).toBeVisible()
  }
)

test.each([
  { scan_complete: true, checked_channels: 2, failed_channels: 1 },
  { ScanComplete: true, CheckedChannels: 2, FailedChannels: 1 },
])(
  'legacy success with failed channels displays partial completion: %j',
  async (result) => {
    mount([task(result)])
    const row = await historyRow()
    expectNotGreen(row)
    expect(within(row).getByText('Partially completed')).toBeVisible()
    expect(within(row).getByText('Failed channels: 1')).toBeVisible()
  }
)

test('committed cache-only failure shows degraded guidance; Refresh only GETs history', async () => {
  mount([
    task({
      scan_complete: true,
      checked_channels: 1,
      failed_channels: 0,
      outcomes: [
        {
          channel_id: 1,
          committed: true,
          degraded: true,
          refresh_error: 'SECRET cache URL',
          error: 'SECRET error',
        },
      ],
    }),
  ])
  const row = await historyRow()
  expectNotGreen(row)
  expect(within(row).getByText('Committed; cache degraded')).toBeVisible()
  expect(within(row).getByText('Committed changes: 1')).toBeVisible()
  expect(
    within(row).getByText(
      'Do not reapply committed writes. Only retry the cache refresh for these changes.'
    )
  ).toBeVisible()
  expect(api.get).toHaveBeenCalledTimes(1)
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: [
        task({
          scan_complete: true,
          failed_channels: 0,
          checked_channels: 1,
          outcomes: [],
        }),
      ],
    },
  })
  await userEvent.setup().click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByText('Model update completed.')).toBeVisible()
  expect(api.get).toHaveBeenCalledTimes(2)
  for (const call of vi.mocked(api.get).mock.calls) {
    expect(call).toEqual(['/api/system-task/list', { params: { limit: 20 } }])
  }
  expect(api.post).not.toHaveBeenCalled()
  expect(api.put).not.toHaveBeenCalled()
  expect(api.delete).not.toHaveBeenCalled()
})

test('mixed operation failure and committed degradation retains both facts', async () => {
  mount([
    task(
      {
        scan_complete: true,
        failed_channels: 1,
        outcomes: [
          { committed: true, degraded: true },
          { committed: false, error: 'SECRET failure' },
        ],
      },
      'failed'
    ),
  ])
  const row = await historyRow()
  expectNotGreen(row)
  expect(within(row).getByText('Partially completed')).toBeVisible()
  expect(within(row).getByText('Failed channels: 1')).toBeVisible()
  expect(within(row).getByText('Committed changes: 1')).toBeVisible()
  expect(
    within(row).getByText('Cache refresh failed for committed changes: 1')
  ).toBeVisible()
})

test.each([
  undefined,
  null,
  'SECRET malformed',
  [],
  {},
  {
    scan_complete: 'false',
    failed_channels: 'SECRET',
    outcomes: [null, 'SECRET'],
  },
])(
  'missing or malformed legacy result is safe and not confirmed success: %j',
  async (result) => {
    mount([task(result)])
    const row = await historyRow()
    expectNotGreen(row)
    expect(
      within(row).getByText('Completion details unavailable.')
    ).toBeVisible()
  }
)

test('complete healthy result preserves green success and useful summary', async () => {
  mount([
    task({
      scan_complete: true,
      checked_channels: 2,
      failed_channels: 0,
      outcomes: [{ committed: true, degraded: false }],
    }),
  ])
  const row = await historyRow()
  expect(row.querySelector('[data-slot="badge"]')).toHaveClass('bg-emerald-50')
  expect(within(row).getByText('Model update completed.')).toBeVisible()
  expect(within(row).getByText('Checked channels: 2')).toBeVisible()
})

test('legacy PascalCase incomplete scan overrides succeeded status', async () => {
  mount([task({ ScanComplete: false, FailedChannels: 0 })])
  const row = await historyRow()
  expectNotGreen(row)
  expect(within(row).getByText('Scan incomplete.')).toBeVisible()
})

test('failed task without a summary uses safe failure detail', async () => {
  mount([task(undefined, 'failed')])
  const row = await historyRow()
  expectNotGreen(row)
  expect(within(row).getByText('Model update did not fully complete.')).toBeVisible()
})

test('other task types preserve their existing status and detail', async () => {
  mount([{ ...task(undefined), type: 'log_cleanup', error: 'Existing detail' }])
  const row = await screen.findByRole('row', { name: /Log cleanup/ })
  expect(row.querySelector('[data-slot="badge"]')).toHaveClass('bg-emerald-50')
  expect(within(row).getByText('Existing detail')).toBeVisible()
})
