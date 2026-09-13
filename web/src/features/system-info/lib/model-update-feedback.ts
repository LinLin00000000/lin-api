import type { SystemTask } from '@/features/system-settings/types'

export type ModelUpdateFeedback = {
  status: 'succeeded' | 'failed' | 'partial' | 'degraded' | 'unknown'
  scanIncomplete: boolean
  checked: number | null
  failed: number | null
  committed: number
  degraded: number
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return value as Record<string, unknown>
}

function count(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
    ? value
    : null
}

// Historical summaries are untrusted. Read only known flags and counts; never
// display error strings, channel names, URLs, or the raw result in the panel.
export function getModelUpdateFeedback(
  task: SystemTask
): ModelUpdateFeedback | null {
  if (
    task.type !== 'model_update' ||
    task.status === 'pending' ||
    task.status === 'running'
  ) {
    return null
  }
  const result = record(task.result)
  const scan = result.scan_complete ?? result.ScanComplete
  const failed = count(result.failed_channels ?? result.FailedChannels)
  const checked = count(result.checked_channels ?? result.CheckedChannels)
  const rawOutcomes = result.outcomes ?? result.Outcomes
  const outcomes = Array.isArray(rawOutcomes) ? rawOutcomes.map(record) : []
  const committed = outcomes.filter(
    (outcome) => outcome.committed === true
  ).length
  const degraded = outcomes.filter(
    (outcome) => outcome.committed === true && outcome.degraded === true
  ).length
  const operationFailed = outcomes.some(
    (outcome) =>
      outcome.committed === false &&
      typeof outcome.error === 'string' &&
      outcome.error.length > 0
  )
  let status: ModelUpdateFeedback['status'] = 'unknown'
  if (scan === false) status = 'failed'
  else if ((failed ?? 0) > 0 || operationFailed) status = 'partial'
  else if (task.status === 'failed') status = 'failed'
  else if (degraded > 0) status = 'degraded'
  else if (scan === true && failed === 0) status = 'succeeded'
  return {
    status,
    scanIncomplete: scan === false,
    checked,
    failed,
    committed,
    degraded,
  }
}
