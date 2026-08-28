import fs from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const featureDir = path.resolve(__dirname, '..')
const read = (relative: string) => fs.readFileSync(path.resolve(featureDir, relative), 'utf8')

describe('session archive integration surface', () => {
  it('registers a fixed admin route and sidebar entry', () => {
    const router = read('../../router/index.ts')
    const sidebar = read('../../components/layout/AppSidebar.vue')
    expect(router).toContain("path: '/admin/session-archive'")
    expect(router).toContain("titleKey: 'admin.sessionArchive.title'")
    const entry = sidebar.slice(sidebar.indexOf("path: '/admin/session-archive'"), sidebar.indexOf("path: '/admin/session-archive'") + 180)
    expect(entry).toContain("t('nav.sessionArchive')")
    expect(entry).not.toContain('featureFlag')
  })

  it('ships English and Chinese locale namespaces', () => {
    expect(read('../../i18n/locales/en/admin/sessionArchive.ts')).toContain("sessionArchive: {")
    expect(read('../../i18n/locales/zh/admin/sessionArchive.ts')).toContain("sessionArchive: {")
  })

  it('uses inert text and native ticket download without browser-side file aggregation', () => {
    const sources = fs.readdirSync(featureDir)
      .filter((name) => name.endsWith('.vue') || name.endsWith('.ts'))
      .map((name) => fs.readFileSync(path.join(featureDir, name), 'utf8'))
      .join('\n')
    expect(sources).not.toMatch(/\bv-html\b/)
    expect(sources).not.toMatch(/\bnew\s+Blob\b|createObjectURL|responseType\s*:\s*['"]blob['"]/)
    expect(read('SessionArchiveView.vue')).toContain('archive-native-download')
    expect(read('SessionArchiveView.vue')).toContain('downloadAnchor.value?.click()')
  })

  it('exposes correlation links in Usage, Prompt Audit, and Ops', () => {
    expect(read('../../components/admin/usage/UsageTable.vue')).toContain("path: '/admin/session-archive'")
    expect(read('../prompt-audit/components/EventDetailDialog.vue')).toContain("path: '/admin/session-archive'")
    expect(read('../../views/admin/ops/components/OpsErrorDetailModal.vue')).toContain("path: '/admin/session-archive'")
    expect(read('../../views/admin/UsageView.vue')).toContain('route.query.correlation_request_id')
    expect(read('../prompt-audit/PromptAuditView.vue')).toContain('route.query.correlation_request_id')
    expect(read('../../views/admin/ops/OpsDashboard.vue')).toContain("correlationRequestId: 'correlation_request_id'")
  })
})
