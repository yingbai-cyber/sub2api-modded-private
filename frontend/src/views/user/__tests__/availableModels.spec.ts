import { describe, expect, it } from 'vitest'
import type { UserAvailableChannel } from '@/api/channels'
import {
  filterAvailableModelRows,
  flattenAvailableModelRows,
  summarizeAvailableModels,
} from '../availableModels'

const channels: UserAvailableChannel[] = [
  {
    name: 'OpenAI 高速池',
    description: '主推 OpenAI 模型',
    platforms: [
      {
        platform: 'openai',
        groups: [
          {
            id: 1,
            name: 'default',
            platform: 'openai',
            subscription_type: 'standard',
            rate_multiplier: 1,
            is_exclusive: false,
          },
        ],
        supported_models: [
          {
            name: 'gpt-4o',
            platform: 'openai',
            pricing: null,
          },
          {
            name: 'gpt-4o-mini',
            platform: 'openai',
            pricing: null,
          },
        ],
      },
    ],
  },
  {
    name: '备用 OpenAI 池',
    description: '相同模型不同渠道',
    platforms: [
      {
        platform: 'openai',
        groups: [
          {
            id: 2,
            name: 'vip',
            platform: 'openai',
            subscription_type: 'subscription',
            rate_multiplier: 0.8,
            is_exclusive: true,
          },
        ],
        supported_models: [
          {
            name: 'gpt-4o',
            platform: 'openai',
            pricing: null,
          },
        ],
      },
    ],
  },
]

describe('availableModels helpers', () => {
  it('flattens channel sections into model rows without merging duplicate model sources', () => {
    const rows = flattenAvailableModelRows(channels)

    expect(rows).toHaveLength(3)
    expect(rows.map((row) => `${row.channelName}:${row.model.name}`)).toEqual([
      'OpenAI 高速池:gpt-4o',
      'OpenAI 高速池:gpt-4o-mini',
      '备用 OpenAI 池:gpt-4o',
    ])
  })

  it('summarizes unique models by platform while keeping row count as detailed entries', () => {
    const summary = summarizeAvailableModels(channels)

    expect(summary).toEqual({
      channelCount: 2,
      platformCount: 1,
      modelCount: 2,
      rowCount: 3,
    })
  })

  it('filters by model, channel, platform, and group text', () => {
    const rows = flattenAvailableModelRows(channels)

    expect(filterAvailableModelRows(rows, 'mini')).toHaveLength(1)
    expect(filterAvailableModelRows(rows, '备用')).toHaveLength(1)
    expect(filterAvailableModelRows(rows, 'openai')).toHaveLength(3)
    expect(filterAvailableModelRows(rows, 'vip')).toHaveLength(1)
  })
})
