import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../UserDashboardQuickActions.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('UserDashboardQuickActions available models entry', () => {
  it('links users to the dedicated available models page', () => {
    expect(componentSource).toContain("router.push('/available-models')")
    expect(componentSource).toContain("dashboard.viewAvailableModels")
    expect(componentSource).toContain("dashboard.checkModelsAndPricing")
  })
})
