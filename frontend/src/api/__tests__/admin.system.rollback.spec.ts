import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../client', () => ({
  apiClient: {
    get,
    post,
  },
}))

import { getRollbackVersions, rollback, type RollbackVersionInfo } from '@/api/admin/system'

// CAPYBARA-PATCH: 上游 35b5edb24 给 rollback 请求加了 timeout 选项但未同步本文件断言（v0.1.162~v0.1.172 持续失败）。
// system.ts 中的 UPDATE_REQUEST_TIMEOUT_MS 未导出，此处镜像其值；上游修复后本补丁可撤除。
const UPDATE_REQUEST_TIMEOUT_MS = 15 * 60 * 1000

describe('admin system rollback API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('getRollbackVersions fetches the rollback version list', async () => {
    const versions: RollbackVersionInfo[] = [
      {
        version: '0.1.146',
        published_at: '2026-07-07T00:00:00Z',
        html_url: 'https://github.com/Wei-Shaw/sub2api/releases/tag/v0.1.146'
      }
    ]
    get.mockResolvedValue({ data: { versions } })

    const result = await getRollbackVersions()

    expect(get).toHaveBeenCalledWith('/admin/system/rollback-versions')
    expect(result.versions).toEqual(versions)
  })

  it('rollback posts the target version in the request body', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    const result = await rollback('0.1.146')

    expect(post).toHaveBeenCalledWith('/admin/system/rollback', { version: '0.1.146' }, {
      timeout: UPDATE_REQUEST_TIMEOUT_MS
    })
    expect(result.need_restart).toBe(true)
  })

  it('rollback without a version posts no body (legacy backup rollback)', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    await rollback()

    expect(post).toHaveBeenCalledWith('/admin/system/rollback', undefined, {
      timeout: UPDATE_REQUEST_TIMEOUT_MS
    })
  })
})
