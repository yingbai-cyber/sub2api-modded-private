import { describe, expect, it } from 'vitest'
import type { UserAvailableModelSection } from '@/api/models'
import {
  filterAvailableModelRows,
  flattenAvailableModelRows,
  summarizeAvailableModels,
} from '../availableModels'

const sections: UserAvailableModelSection[] = [
  {
    group: {
      id: 1,
      name: 'default',
      platform: 'openai',
      subscription_type: 'standard',
      rate_multiplier: 1,
      is_exclusive: false,
    },
    models: [
      {
        name: 'gpt-4o',
        platform: 'openai',
        display_name: 'GPT-4o',
      },
      {
        name: 'gpt-4o-mini',
        platform: 'openai',
        display_name: 'GPT-4o Mini',
      },
    ],
  },
  {
    group: {
      id: 2,
      name: 'vip',
      platform: 'openai',
      subscription_type: 'subscription',
      rate_multiplier: 0.8,
      is_exclusive: true,
    },
    models: [
      {
        name: 'gpt-4o',
        platform: 'openai',
        display_name: 'GPT-4o',
      },
    ],
  },
]

describe('availableModels helpers', () => {
  it('flattens group sections into model rows without merging duplicate group sources', () => {
    const rows = flattenAvailableModelRows(sections)

    expect(rows).toHaveLength(3)
    expect(rows.map((row) => `${row.groups[0]?.name}:${row.model.name}`)).toEqual([
      'default:gpt-4o',
      'default:gpt-4o-mini',
      'vip:gpt-4o',
    ])
  })

  it('summarizes unique models by platform while keeping row count as detailed entries', () => {
    const summary = summarizeAvailableModels(sections)

    expect(summary).toEqual({
      groupCount: 2,
      platformCount: 1,
      modelCount: 2,
      rowCount: 3,
    })
  })

  it('filters by model, display name, platform, and group text', () => {
    const rows = flattenAvailableModelRows(sections)

    expect(filterAvailableModelRows(rows, 'mini')).toHaveLength(1)
    expect(filterAvailableModelRows(rows, 'GPT-4o')).toHaveLength(3)
    expect(filterAvailableModelRows(rows, 'openai')).toHaveLength(3)
    expect(filterAvailableModelRows(rows, 'vip')).toHaveLength(1)
  })
})
