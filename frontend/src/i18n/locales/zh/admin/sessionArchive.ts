export default {
  sessionArchive: {
    title: '会话归档',
    description: '按网关实际可见内容重建 Session、Turn、Request、Attempt、工具活动与有序模型输出，正文不进入用量日志。',
    tabs: { sessions: '会话', config: '配置' },
    actions: {
      loadContent: '加载敏感正文', exportArchive: '导出 Archive', exportSft: '导出 SFT',
      deleteFiltered: '删除筛选结果', newPolicy: '新建策略', download: '下载', viewArchive: '查看归档',
    },
    workspace: {
      title: '归档会话', description: '先检索窄元数据；请求、响应、Tool 与 Raw 正文只会在显式强认证后按需加载。',
      empty: '没有符合条件的归档会话。', truncated: '存在截断', controlPlaneOnly: '仅控制面',
    },
    fields: {
      correlationRequestId: '关联请求 ID', userId: '用户 ID', apiKeyId: 'API Key ID', groupId: '分组 ID',
      model: '模型', client: '客户端', status: '状态', startAt: '开始时间', endAt: '结束时间',
      lastActivity: '最后活动', identity: '用户 / API Key / 分组', route: '协议 / 模型', turnsRequests: 'Turn / Request',
      expiresAt: '过期时间', endpoint: '入口', billingRequestId: '计费请求 ID', upstreamRequestId: '上游请求 ID',
      createdAt: '创建时间', account: '账号', upstreamStatus: '上游状态', transform: '转换', latency: '耗时',
    },
    status: {
      active: '活动中', completed: '已完成', failed: '失败', deleting: '删除中', pending: '等待中', running: '运行中',
      canceled: '已取消', disabled: '未启用', degraded: '降级', error: '错误', healthy: '健康', ready: '就绪',
    },
    detail: {
      title: '会话归档详情', timeline: 'Turn 时间线', turn: 'Turn {sequence}', request: 'Request {sequence}',
      attempt: 'Attempt {sequence}', finalAttempt: '最终尝试', noRequests: '该会话没有 Request 窄投影。',
      sensitiveHint: '此内容敏感且绝不预取，也可能不可用或已截断。', contentNotLoaded: '点击加载，经二次验证后读取该正文。',
      contentIncomplete: '观测不完整：观测 {observed}，留存 {stored}，原因 {reason}。',
      tabs: { attempts: '尝试记录', request: '请求', upstream: '上游请求', response: '响应', tool: 'Tool', attachment: '附件', raw: 'Raw 诊断' },
    },
    config: {
      title: '运行状态与采集策略', description: '在私有存储与持久加密密钥通过服务端检查前，模块保持全局关闭。',
      process: '进程状态', collecting: '已有生效策略开启采集', defaultOff: '全局默认关闭', queue: '队列事件 / 字节',
      delivery: '已留存观测', deliveryDetail: '丢弃 {dropped} · 失败 {failed} · 截断 {truncated}', storage: '私有存储',
      policies: '作用域策略', precedence: '解析顺序：API Key > 用户 > 分组 > 全局；最具体的显式 on/off 生效。',
      scope: '作用域', scopeId: '作用域 ID', policyState: '状态', retentionDays: '保留天数', bodyLimit: '单正文上限（字节）',
      capture: '采集内容', capture_request: '原始请求', capture_response: '客户端可见响应',
      capture_transformed_request: '转换后上游请求', capture_tools: 'Tool 结构', capture_attachments: '附件引用',
      sensitiveBoundary: '归档提示词和工具参数可能含用户主动提交的秘密。安全边界由加密、二次验证、保留期与删除构成，而不是改写正文。',
    },
    scope: { global: '全局', group: '分组', user: '用户', api_key: 'API Key' },
    policyState: { inherit: '继承', on: '开启', off: '关闭' },
    deletion: {
      title: '删除任务', progress: '已处理 {processed} / {total}；失败 {failed}', confirmTitle: '创建归档删除任务？',
      confirmSession: '会话会立即变为不可读，元数据与私有 CAS 对象随后异步删除，且无法恢复。',
      confirmFiltered: '当前规范化筛选条件将固化到异步删除任务，命中会话会立即变为不可读。', confirm: '创建删除任务',
    },
    export: {
      preflightTitle: '确认归档导出',
      preflightSummary: '命中 {matched} 个会话；可导出 {eligible} 个样本；跳过 {skipped} 个样本。',
      preflightReasons: '跳过原因：{reasons}',
      confirm: '签发票据并下载',
    },
    links: { usage: '用量', promptAudit: '提示词审计', ops: '运维' },
    messages: { policySaved: '归档策略已保存。', policyDeleted: '归档策略已删除。', exportStarted: '单次下载已开始。', deletionQueued: '删除任务已进入队列。' },
    errors: {
      loadSessions: '无法加载归档会话。', loadConfig: '无法加载归档运行状态或策略。', loadDetail: '无法加载会话详情。',
      loadContent: '无法加载敏感正文。', copyContent: '无法复制敏感正文。', savePolicy: '无法保存归档策略。',
      deletePolicy: '无法删除归档策略。', exportPreflight: '无法预检归档导出。', export: '无法签发导出票据。', delete: '无法创建删除任务。',
    },
  },
}
