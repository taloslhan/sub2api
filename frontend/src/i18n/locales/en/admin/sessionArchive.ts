export default {
  sessionArchive: {
    title: 'Session Archive',
    description: 'Reconstruct gateway-visible sessions, turns, requests, attempts, tool activity, and ordered model output without placing full content in usage logs.',
    tabs: { sessions: 'Sessions', config: 'Configuration' },
    actions: {
      loadContent: 'Load sensitive content', exportArchive: 'Export archive', exportSft: 'Export SFT',
      deleteFiltered: 'Delete filtered results', newPolicy: 'New policy', download: 'Download', viewArchive: 'View archive',
    },
    workspace: {
      title: 'Archived sessions',
      description: 'Search narrow metadata first. Request, response, Tool, and Raw content is loaded on demand by authenticated administrators.',
      empty: 'No matching archived sessions.', truncated: 'Truncated', controlPlaneOnly: 'Control plane only',
    },
    fields: {
      correlationRequestId: 'Correlation request ID', userId: 'User ID', apiKeyId: 'API Key ID', groupId: 'Group ID',
      model: 'Model', client: 'Client', status: 'Status', startAt: 'Start time', endAt: 'End time',
      lastActivity: 'Last activity', identity: 'User / API Key / group', route: 'Protocol / model', turnsRequests: 'Turns / requests',
      expiresAt: 'Expires at', endpoint: 'Endpoint', billingRequestId: 'Billing request ID', upstreamRequestId: 'Upstream request ID',
      createdAt: 'Created at', account: 'Account', upstreamStatus: 'Upstream status', transform: 'Transform', latency: 'Latency',
    },
    status: {
      active: 'Active', completed: 'Completed', failed: 'Failed', deleting: 'Deleting', pending: 'Pending', running: 'Running',
      canceled: 'Canceled', disabled: 'Disabled', degraded: 'Degraded', error: 'Error', healthy: 'Healthy', ready: 'Ready',
    },
    detail: {
      title: 'Session archive details', timeline: 'Turn timeline', turn: 'Turn {sequence}', request: 'Request {sequence}',
      attempt: 'Attempt {sequence}', finalAttempt: 'Final attempt', noRequests: 'This session has no request projections.',
      sensitiveHint: 'This content is sensitive, never prefetched, and may be unavailable or truncated.',
      contentNotLoaded: 'Select load to retrieve this content.',
      contentIncomplete: 'Incomplete observation: observed {observed}, stored {stored}, reason {reason}.',
      tabs: { attempts: 'Attempts', request: 'Request', upstream: 'Upstream request', response: 'Response', tool: 'Tool', attachment: 'Attachment', raw: 'Raw diagnostic' },
    },
    config: {
      title: 'Runtime and capture policy', description: 'The module stays globally off until storage and persistent encryption keys pass server checks.',
      process: 'Process', collecting: 'Capture is enabled by an effective policy', defaultOff: 'Global default is off', queue: 'Queue events / bytes',
      delivery: 'Stored observations', deliveryDetail: 'Dropped {dropped} · failed {failed} · truncated {truncated}', storage: 'Private storage',
      policies: 'Scope policies', precedence: 'Resolution order: API Key > user > group > global. The most specific explicit on/off value wins.',
      scope: 'Scope', scopeId: 'Scope ID', policyState: 'State', retentionDays: 'Retention (days)', bodyLimit: 'Per-body limit (bytes)',
      capture: 'Captured content', capture_request: 'Original request', capture_response: 'Client-visible response',
      capture_transformed_request: 'Transformed upstream request', capture_tools: 'Tool structures', capture_attachments: 'Attachment references',
      sensitiveBoundary: 'Archived prompts and tool parameters may contain user-supplied secrets. Administrator authorization, required audit, encryption, retention, and deletion—not content rewriting—form the security boundary.',
    },
    scope: { global: 'Global', group: 'Group', user: 'User', api_key: 'API Key' },
    policyState: { inherit: 'Inherit', on: 'On', off: 'Off' },
    deletion: {
      title: 'Deletion jobs', progress: 'Processed {processed} / {total}; failures {failed}', confirmTitle: 'Create an archive deletion job?',
      confirmSession: 'The session becomes unreadable immediately. Metadata and private CAS objects are removed asynchronously and cannot be recovered.',
      confirmFiltered: 'The current normalized filters will be frozen into an asynchronous deletion job. Matching sessions become unreadable immediately.',
      confirm: 'Create deletion job',
    },
    export: {
      preflightTitle: 'Confirm archive export',
      preflightSummary: 'Matched {matched} sessions; {eligible} samples are eligible and {skipped} will be skipped.',
      preflightReasons: 'Skip reasons: {reasons}',
      confirm: 'Issue ticket and download',
    },
    links: { usage: 'Usage', promptAudit: 'Prompt Audit', ops: 'Ops' },
    messages: {
      policySaved: 'Archive policy saved.', policyDeleted: 'Archive policy deleted.', exportStarted: 'The single-use download has started.', deletionQueued: 'Deletion job queued.',
    },
    errors: {
      loadSessions: 'Unable to load archived sessions.', loadConfig: 'Unable to load archive runtime or policies.', loadDetail: 'Unable to load session details.',
      loadContent: 'Unable to load sensitive content.', copyContent: 'Unable to copy sensitive content.', savePolicy: 'Unable to save archive policy.',
      deletePolicy: 'Unable to delete archive policy.', exportPreflight: 'Unable to preflight the archive export.', export: 'Unable to issue an export ticket.', delete: 'Unable to create the deletion job.',
    },
  },
}
