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

import { api } from '@/lib/api'

const ratio = z.number().finite().nonnegative()
// Zod record silently skips __proto__. Validate own entries as tuples instead:
// Object.fromEntries defines data properties (including __proto__), never setters.
// Only JSON objects/nullish maps are accepted; every value still uses its schema.
const map = <T>(value: z.ZodType<T>) =>
  z
    .custom<Record<string, unknown>>(
      (v) =>
        typeof v === 'object' &&
        v !== null &&
        (Object.getPrototypeOf(v) === Object.prototype ||
          Object.getPrototypeOf(v) === null),
      'Expected a JSON object'
    )
    .nullish()
    .transform((v) => Object.entries(v ?? {}))
    .pipe(z.array(z.tuple([z.string(), value])))
    .transform((entries): Record<string, T> => Object.fromEntries(entries))
export const configSchema = z
  .object({
    version: z.literal(1),
    mode: z.literal('legacy'),
    revision: z
      .number()
      .int()
      .nonnegative()
      .max(Number.MAX_SAFE_INTEGER - 1),
    service_defaults: map(ratio),
    identity_defaults: map(ratio),
    service_models: map(
      map(z.object({ enabled: z.boolean(), ratio: ratio.optional() }).strict())
    ),
    identity_model_ratios: map(map(ratio)),
    model_identity_scopes: map(
      z
        .object({
          mode: z.enum(['public', 'restricted']),
          identities: z
            .array(z.string())
            .nullish()
            .transform((v) => v ?? []),
        })
        .strict()
    ),
    migration_source_digest: z.string().optional(),
  })
  .strict()
export type IdentityServiceConfig = z.infer<typeof configSchema>
export type ValidationReport = {
  success: boolean
  config_valid?: boolean
  message?: string
  activation_ready?: boolean
  activation_blockers?: string[]
  pending_verification?: string[]
}
export const identityServicePath = '/api/option/identity_service'
const options = {
  skipErrorHandler: true,
  skipBusinessError: true,
  disableDuplicate: true,
}

function ownedOptions(signal?: AbortSignal) {
  return signal ? { ...options, signal, skipAuthRefresh: true } : options
}

export async function readIdentityService(signal?: AbortSignal) {
  const response = await api.get(identityServicePath, ownedOptions(signal))
  if (response.data?.success !== true) {
    throw new Error(response.data?.message || 'Identity/service request failed')
  }
  return configSchema.parse(response.data.data)
}
export async function validateIdentityService(
  config: IdentityServiceConfig,
  signal?: AbortSignal
): Promise<ValidationReport> {
  const response = await api.post(
    `${identityServicePath}/validate`,
    { config: { ...config, mode: 'legacy' } },
    ownedOptions(signal)
  )
  return response.data
}
export async function saveIdentityService(
  config: IdentityServiceConfig,
  assertOwner: () => void = () => {},
  signal?: AbortSignal
) {
  assertOwner()
  const response = await api.put(
    identityServicePath,
    {
      expected_revision: config.revision,
      config: { ...config, mode: 'legacy' },
    },
    ownedOptions(signal)
  )
  if (response.data?.success !== true) {
    throw new Error(response.data?.message || 'Identity/service request failed')
  }
  // Invalidation cannot roll back an already sent PUT. It must stop new GETs.
  assertOwner()
  // The PUT response is not the readback oracle. Always make a fresh GET.
  try {
    const config = await readIdentityService(signal)
    assertOwner()
    return config
  } catch (cause) {
    throw new Error(
      'Save may have committed; readback failed. Draft kept. Reload before saving again.',
      { cause }
    )
  }
}
