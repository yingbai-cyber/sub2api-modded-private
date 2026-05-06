import type {
  UserAvailableModel,
  UserAvailableModelGroup,
  UserAvailableModelSection,
} from '@/api/models'

export interface AvailableModelRow {
  id: string
  platform: string
  model: UserAvailableModel
  groups: UserAvailableModelGroup[]
}

export interface AvailableModelSummary {
  groupCount: number
  platformCount: number
  modelCount: number
  rowCount: number
}

function normalizeText(value: string | null | undefined): string {
  return (value || '').trim().toLowerCase()
}

function rowSearchText(row: AvailableModelRow): string {
  return [
    row.platform,
    row.model.name,
    row.model.display_name,
    ...row.groups.map((group) => group.name),
    ...row.groups.map((group) => group.platform),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

export function flattenAvailableModelRows(sections: UserAvailableModelSection[]): AvailableModelRow[] {
  return sections.flatMap((section) =>
    section.models.map((model) => ({
      id: `${section.group.id}:${model.platform || section.group.platform}:${model.name}`,
      platform: model.platform || section.group.platform,
      model: {
        ...model,
        platform: model.platform || section.group.platform,
        display_name: model.display_name || model.name,
      },
      groups: [section.group],
    })),
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

export function summarizeAvailableModels(sections: UserAvailableModelSection[]): AvailableModelSummary {
  const platforms = new Set<string>()
  const models = new Set<string>()
  let rowCount = 0

  for (const section of sections) {
    if (section.group.platform) {
      platforms.add(section.group.platform)
    }
    for (const model of section.models) {
      const platform = model.platform || section.group.platform
      if (platform) {
        platforms.add(platform)
      }
      models.add(`${platform}:${model.name}`)
      rowCount += 1
    }
  }

  return {
    groupCount: sections.length,
    platformCount: platforms.size,
    modelCount: models.size,
    rowCount,
  }
}
