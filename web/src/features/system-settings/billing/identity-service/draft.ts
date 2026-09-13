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
import { z } from 'zod'

import type { IdentityServiceConfig } from './api'

export type Section =
  | 'service_defaults'
  | 'identity_defaults'
  | 'service_models'
  | 'identity_model_ratios'
  | 'model_identity_scopes'
export type DraftRow = {
  id: number
  key: string
  group: string
  ratio: string
  enabled: boolean
  mode: 'public' | 'restricted'
  identities: { id: number; value: string }[]
}
export type Draft = Record<Section, DraftRow[]>
let nextId = 0
export function newIdentity(value = '') {
  return { id: nextId++, value }
}
export function newRow(group = ''): DraftRow {
  return {
    id: nextId++,
    key: '',
    group,
    ratio: '',
    enabled: false,
    mode: 'restricted',
    identities: [],
  }
}
export function toDraft(c: IdentityServiceConfig): Draft {
  const defaults = (values: Record<string, number>) =>
    Object.entries(values).map(([key, ratio]) => ({
      ...newRow(),
      key,
      ratio: String(ratio),
    }))
  return {
    service_defaults: defaults(c.service_defaults),
    identity_defaults: defaults(c.identity_defaults),
    service_models: Object.entries(c.service_models).flatMap(
      ([group, models]) =>
        Object.entries(models).map(([key, row]) => ({
          ...newRow(group),
          key,
          enabled: row.enabled,
          ratio: row.ratio === undefined ? '' : String(row.ratio),
        }))
    ),
    identity_model_ratios: Object.entries(c.identity_model_ratios).flatMap(
      ([group, models]) => defaults(models).map((row) => ({ ...row, group }))
    ),
    model_identity_scopes: Object.entries(c.model_identity_scopes).map(
      ([key, row]) => ({
        ...newRow(),
        key,
        mode: row.mode,
        identities: row.identities.map(newIdentity),
      })
    ),
  }
}
const numericText = z
  .string()
  .regex(/^-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/)
  .refine((text) => {
    const value = Number(text)
    const nonzero = /[1-9]/.test(text.split(/[eE]/)[0])
    return (
      Number.isFinite(value) &&
      value >= 0 &&
      !(nonzero && value === 0) &&
      !(nonzero && text.startsWith('-'))
    )
  }, 'Ratios must be finite nonnegative numbers')
  .transform(Number)
function number(text: string): number {
  const result = numericText.safeParse(text)
  if (!result.success) {
    throw new Error('Ratios must be finite nonnegative numbers')
  }
  return result.data
}
// Arrays keep exact keys editable. Never normalize them or silently merge duplicates.
function entries<T>(
  rows: DraftRow[],
  value: (row: DraftRow) => T
): Record<string, T> {
  if (new Set(rows.map((r) => r.key)).size !== rows.length) {
    throw new Error('Duplicate exact keys must be resolved')
  }
  return Object.fromEntries(rows.map((row) => [row.key, value(row)]))
}
function grouped<T>(
  rows: DraftRow[],
  value: (row: DraftRow) => T
): Record<string, Record<string, T>> {
  return Object.fromEntries(
    [...new Set(rows.map((r) => r.group))].map((group) => [
      group,
      entries(
        rows.filter((r) => r.group === group),
        value
      ),
    ])
  )
}
export function toConfig(
  base: IdentityServiceConfig,
  draft: Draft
): IdentityServiceConfig {
  return {
    ...base,
    mode: 'legacy',
    service_defaults: entries(draft.service_defaults, (r) => number(r.ratio)),
    identity_defaults: entries(draft.identity_defaults, (r) => number(r.ratio)),
    service_models: grouped(draft.service_models, (r) => ({
      enabled: r.enabled,
      ...(r.ratio === '' ? {} : { ratio: number(r.ratio) }),
    })),
    identity_model_ratios: grouped(
      draft.identity_model_ratios.filter((r) => r.ratio !== ''),
      (r) => number(r.ratio)
    ),
    model_identity_scopes: entries(draft.model_identity_scopes, (r) => ({
      mode: r.mode,
      identities: r.identities.map((identity) => identity.value),
    })),
  }
}
