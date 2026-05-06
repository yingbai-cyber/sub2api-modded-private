import type {
  UserAvailableChannel,
  UserAvailableGroup,
  UserChannelPlatformSection,
  UserSupportedModel,
} from '@/api/channels'

export interface AvailableModelRow {
  id: string
  channelName: string
  channelDescription: string
  platform: string
  model: UserSupportedModel
  groups: UserAvailableGroup[]
}

export interface AvailableModelSummary {
  channelCount: number
  platformCount: number
  modelCount: number
  rowCount: number
}

function normalizeText(value: string | null | undefined): string {
  return (value || '').trim().toLowerCase()
}

function rowSearchText(row: AvailableModelRow): string {
  return [
    row.channelName,
    row.channelDescription,
    row.platform,
    row.model.name,
    row.model.platform,
    ...row.groups.map((group) => group.name),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

export function flattenAvailableModelRows(channels: UserAvailableChannel[]): AvailableModelRow[] {
  return channels.flatMap((channel) =>
    channel.platforms.flatMap((section: UserChannelPlatformSection) => {
      if (section.supported_models.length === 0) {
        return [
          {
            id: `${channel.name}:${section.platform}:__empty__`,
            channelName: channel.name,
            channelDescription: channel.description || '',
            platform: section.platform,
            model: {
              name: '',
              platform: section.platform,
              pricing: null,
            },
            groups: section.groups,
          },
        ]
      }

      return section.supported_models.map((model) => ({
        id: `${channel.name}:${section.platform}:${model.name}`,
        channelName: channel.name,
        channelDescription: channel.description || '',
        platform: section.platform,
        model: {
          ...model,
          platform: model.platform || section.platform,
        },
        groups: section.groups,
      }))
    }),
  )
}

export function filterAvailableModelRows(
  rows: AvailableModelRow[],
  searchQuery: string,
): AvailableModelRow[] {
  const q = normalizeText(searchQuery)
  if (!q) return rows
  return rows.filter((row) => rowSearchText(row).includes(q))
}

export function summarizeAvailableModels(channels: UserAvailableChannel[]): AvailableModelSummary {
  const platforms = new Set<string>()
  const models = new Set<string>()
  let rowCount = 0

  for (const channel of channels) {
    for (const section of channel.platforms) {
      platforms.add(section.platform)
      for (const model of section.supported_models) {
        models.add(`${section.platform}:${model.name}`)
        rowCount += 1
      }
    }
  }

  return {
    channelCount: channels.length,
    platformCount: platforms.size,
    modelCount: models.size,
    rowCount,
  }
}
