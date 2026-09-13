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
import { expect, test, vi } from 'vitest'

import { configSchema } from '../identity-service/api'

vi.mock('@/lib/api', () => ({ api: {} }))
const base = { version: 1, mode: 'legacy', revision: 7 }
const own = (value: unknown) => Object.fromEntries([['__proto__', value]])
test('schema preserves and validates every own exact key recursively without prototype writes', () => {
  const before = Object.getOwnPropertyDescriptors(Object.prototype)
  const input = {
    ...base,
    service_defaults: own(2),
    identity_defaults: own(0),
    service_models: own(own({ enabled: true, ratio: 0 })),
    identity_model_ratios: own(own(1)),
    model_identity_scopes: own({
      mode: 'restricted',
      identities: ['__proto__'],
    }),
  }
  // Exercise JSON wire serialization, not structuredClone.
  // oxlint-disable-next-line unicorn/prefer-structured-clone
  const parsed = configSchema.parse(JSON.parse(JSON.stringify(input)))
  // oxlint-disable-next-line unicorn/prefer-structured-clone
  expect(JSON.parse(JSON.stringify(parsed))).toEqual(input)
  expect(Object.hasOwn(parsed.service_defaults, '__proto__')).toBe(true)
  expect(Object.hasOwn(parsed.service_models['__proto__'], '__proto__')).toBe(
    true
  )
  expect(Object.getOwnPropertyDescriptors(Object.prototype)).toEqual(before)
})
test.each([
  ['service_defaults', own(-1)],
  ['identity_defaults', own(Infinity)],
  ['service_defaults', own('1')],
  ['identity_defaults', own(null)],
  ['service_models', own(own({ enabled: true, ratio: -1 }))],
  ['service_models', own(own({ enabled: 'true' }))],
  ['service_models', own(own({ enabled: true, extra: 1 }))],
  ['identity_model_ratios', own(own(Number.NaN))],
  ['model_identity_scopes', own({ mode: 'other', identities: [] })],
  ['model_identity_scopes', own({ mode: 'public', identities: [1] })],
  ['model_identity_scopes', own({ mode: 'public', identities: [], extra: 1 })],
  ['service_defaults', []],
  ['service_defaults', 'bad'],
  ['service_models', own([])],
  ['identity_model_ratios', own(1)],
])(
  'invalid values are rejected even under __proto__: %s %#',
  (field, value) => {
    expect(configSchema.safeParse({ ...base, [field]: value }).success).toBe(
      false
    )
  }
)
test('nullish maps and scope lists retain normalization, unknown structure and activation stay rejected', () => {
  expect(
    configSchema.parse({ ...base, service_defaults: null }).service_defaults
  ).toEqual({})
  expect(
    configSchema.parse({
      ...base,
      model_identity_scopes: own({ mode: 'restricted', identities: null }),
    }).model_identity_scopes['__proto__'].identities
  ).toEqual([])
  expect(configSchema.safeParse({ ...base, extra: 1 }).success).toBe(false)
  expect(
    configSchema.safeParse({ ...base, mode: 'identity_service' }).success
  ).toBe(false)
})
