export function getCyberAccessProgramLabel(
  program: string | null | undefined,
  translate: (key: string) => string,
): string {
  const normalized = program?.trim().toLowerCase().replace(/-/g, '_')
  if (!normalized) return ''
  if (normalized === 'standard') return translate('usage.cyberProgramStandard')
  if (normalized === 'daybreak_blue') return translate('usage.cyberProgramDaybreakBlue')
  if (normalized === 'daybreak_red') return translate('usage.cyberProgramDaybreakRed')
  return program!.trim()
}
