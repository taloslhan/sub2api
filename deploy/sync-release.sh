#!/usr/bin/env bash
# 一键同步官方上游、验证二开分支，并可选构建推送 Docker 镜像。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

FAST=false
FORCE_BUILD=false
SKIP_DOCKER=false
DOCKER_TAG=""
ORIGINAL_BRANCH=""
UPDATE_COUNT=0
VERSION_BEFORE="unknown"
VERSION_AFTER="unknown"
DOCKER_STATUS="跳过"

CRITICAL_VITEST=(
    "src/views/auth/__tests__/LinuxDoCallbackView.spec.ts"
    "src/views/auth/__tests__/WechatCallbackView.spec.ts"
    "src/views/user/__tests__/PaymentView.spec.ts"
    "src/views/user/__tests__/PaymentResultView.spec.ts"
    "src/components/user/profile/__tests__/ProfileInfoCard.spec.ts"
    "src/views/admin/__tests__/SettingsView.spec.ts"
)

usage() {
    cat <<'EOF'
用法:
  deploy/sync-release.sh [选项]

选项:
  --fast          验证降级为仅编译（go build + 前端 build）
  --force-build   上游无更新时也继续走验证+构建推送
  --skip-docker   只同步不构建镜像
  --tag TAG       透传给 docker-build-push.sh
  -h, --help      显示帮助
EOF
}

log() {
    printf '[sync-release] %s\n' "$*"
}

die() {
    local code="$1"
    shift
    printf '[sync-release] 错误: %s\n' "$*" >&2
    exit "${code}"
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --fast)
                FAST=true
                shift
                ;;
            --force-build)
                FORCE_BUILD=true
                shift
                ;;
            --skip-docker)
                SKIP_DOCKER=true
                shift
                ;;
            --tag)
                DOCKER_TAG="${2:?--tag 需要参数}"
                shift 2
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                die 1 "未知参数: $1"
                ;;
        esac
    done
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die 1 "未找到命令: $1"
}

run_git() {
    git -C "${REPO_ROOT}" "$@"
}

current_branch() {
    run_git rev-parse --abbrev-ref HEAD
}

checkout_original_branch() {
    [[ -n "${ORIGINAL_BRANCH}" ]] || return
    run_git checkout "${ORIGINAL_BRANCH}" >/dev/null
}

read_version() {
    local ref="$1"

    run_git show "${ref}:backend/cmd/server/VERSION" 2>/dev/null || printf 'unknown\n'
}

ensure_clean_tracked_worktree() {
    local status

    status="$(run_git status --porcelain --untracked-files=no)"
    if [[ -n "${status}" ]]; then
        printf '%s\n' "${status}" >&2
        die 1 "存在已跟踪文件改动；请先提交或按需处理，脚本不会自动 stash"
    fi
}

validate_remotes() {
    local origin_url
    local upstream_push_url

    origin_url="$(run_git remote get-url origin 2>/dev/null || true)"
    upstream_push_url="$(run_git remote get-url --push upstream 2>/dev/null || true)"

    [[ "${origin_url}" == *"taloslhan"*"/sub2api"* ]] \
        || die 1 "origin 未指向 taloslhan/sub2api fork: ${origin_url:-<missing>}"
    [[ "${upstream_push_url}" == "DISABLED" ]] \
        || die 1 "upstream push URL 应为 DISABLED，当前为: ${upstream_push_url:-<missing>}"
}

preflight() {
    require_command git
    validate_remotes
    ensure_clean_tracked_worktree
    ORIGINAL_BRANCH="$(current_branch)"
    log "当前分支: ${ORIGINAL_BRANCH}"
}

fetch_upstream() {
    local attempt

    for attempt in 1 2 3; do
        log "拉取 upstream（第 ${attempt}/3 次）"
        if run_git fetch upstream --tags --prune; then
            return
        fi
        sleep 2
    done

    die 1 "git fetch upstream 连续失败 3 次"
}

show_update_overview() {
    log "本次将引入 ${UPDATE_COUNT} 个上游提交"
    run_git log --oneline main..upstream/main | head -20 || true
    run_git diff --stat main upstream/main | tail -1 || true
}

phase_fetch() {
    fetch_upstream
    UPDATE_COUNT="$(run_git rev-list --count main..upstream/main)"

    if [[ "${UPDATE_COUNT}" == "0" && "${FORCE_BUILD}" != "true" ]]; then
        log "上游无更新；如需强制验证和 Docker 构建，请使用 --force-build"
        exit 0
    fi

    VERSION_BEFORE="$(read_version main)"
    show_update_overview
}

refresh_main() {
    log "刷新 main 纯净镜像"
    run_git checkout main
    run_git merge --ff-only upstream/main \
        || die 1 "main 无法 fast-forward 到 upstream/main；请按 SYNC-UPSTREAM.md 的 P3 处理"
    VERSION_AFTER="$(read_version main)"

    run_git push origin main \
        || die 4 "推送 origin main 失败；请按 SYNC-UPSTREAM.md 的 P5 处理，禁止 force push"
}

classify_conflict_file() {
    local file="$1"

    case "${file}" in
        backend/ent/*)
            printf 'ent 生成代码'
            ;;
        backend/migrations/*.sql)
            printf '数据库迁移'
            ;;
        frontend/pnpm-lock.yaml)
            printf '前端锁文件'
            ;;
        backend/go.mod|backend/go.sum)
            printf '后端依赖'
            ;;
        backend/cmd/server/VERSION)
            printf '版本号'
            ;;
        *)
            printf '源码或其他文件'
            ;;
    esac
}

print_conflicts() {
    local file
    local files

    files="$(run_git diff --name-only --diff-filter=U || true)"
    if [[ -z "${files}" ]]; then
        log "未检测到未解决冲突文件；可能是 rerere 已自动套用解法，仍按 SOP 要求停止人工 review"
        return
    fi

    log "冲突文件分类:"
    while IFS= read -r file; do
        [[ -n "${file}" ]] || continue
        printf '  - [%s] %s\n' "$(classify_conflict_file "${file}")" "${file}" >&2
    done <<<"${files}"
}

abort_merge_and_exit() {
    print_conflicts
    run_git merge --abort >/dev/null 2>&1 || true
    checkout_original_branch
    die 2 "合并 main 到 capybara/main 存在冲突；请按 SYNC-UPSTREAM.md 第 3 节人工处理或交给 AI"
}

merge_capybara() {
    local merge_output

    log "合并 main 到 capybara/main"
    run_git checkout capybara/main
    if ! merge_output="$(run_git merge main --no-edit --no-rerere-autoupdate 2>&1)"; then
        printf '%s\n' "${merge_output}" >&2
        abort_merge_and_exit
    fi
    printf '%s\n' "${merge_output}"

    if grep -E "using previous resolution|rerere" <<<"${merge_output}" >/dev/null 2>&1; then
        abort_merge_and_exit
    fi

    if [[ -n "$(run_git status --porcelain --untracked-files=no)" ]]; then
        abort_merge_and_exit
    fi
}

backend_cmd() {
    (cd "${REPO_ROOT}/backend" && "$@")
}

validation_step() {
    local title="$1"
    shift

    log "${title}"
    "$@" || return 1
}

run_backend_validation() {
    validation_step "后端验证: go build ./..." backend_cmd go build ./... || return 1

    if [[ "${FAST}" == "true" ]]; then
        return
    fi

    validation_step "后端验证: go test ./..." backend_cmd go test ./... || return 1

    validation_step "后端验证: golangci-lint v2.9.0" \
        backend_cmd go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.9.0 run --timeout=30m ./... || return 1
}

run_frontend_validation() {
    validation_step "前端验证: pnpm@9 install --frozen-lockfile" \
        corepack pnpm@9 --dir "${REPO_ROOT}/frontend" install --frozen-lockfile || return 1

    validation_step "前端验证: pnpm@9 run build" \
        corepack pnpm@9 --dir "${REPO_ROOT}/frontend" run build || return 1

    if [[ "${FAST}" == "true" ]]; then
        return
    fi

    validation_step "前端验证: pnpm@9 run lint:check" \
        corepack pnpm@9 --dir "${REPO_ROOT}/frontend" run lint:check || return 1

    validation_step "前端验证: pnpm@9 run typecheck" \
        corepack pnpm@9 --dir "${REPO_ROOT}/frontend" run typecheck || return 1

    validation_step "前端验证: 关键 vitest" \
        corepack pnpm@9 --dir "${REPO_ROOT}/frontend" exec vitest run "${CRITICAL_VITEST[@]}" || return 1
}

validate_merge() {
    require_command go
    require_command corepack

    if [[ "${FAST}" == "true" ]]; then
        log "Phase 4 验证模式: fast（仅编译）"
    else
        log "Phase 4 验证模式: 完整验证"
    fi

    run_backend_validation || return 1
    run_frontend_validation || return 1
}

smoke_check() {
    log "二开 diff 统计:"
    run_git diff --stat main capybara/main | tail -1 || true

    if [[ -f "${REPO_ROOT}/CUSTOM_CHANGES.md" ]]; then
        log "检测到 CUSTOM_CHANGES.md，请结合上方 diff 逐条核对侵入式修改"
    else
        log "CUSTOM_CHANGES.md 不存在，使用二开提交主题作为目视核对清单:"
        run_git log main..capybara/main --oneline || true
    fi
}

push_capybara() {
    log "推送 capybara/main"
    run_git push origin capybara/main \
        || die 4 "推送 origin capybara/main 失败；请按 SYNC-UPSTREAM.md 的 P5 处理，禁止 force push"

    checkout_original_branch
}

run_docker_stage() {
    local docker_args=(--test --push --cleanup)

    if [[ "${SKIP_DOCKER}" == "true" ]]; then
        DOCKER_STATUS="已跳过"
        log "跳过 Docker 阶段"
        return
    fi

    if [[ -n "${DOCKER_TAG}" ]]; then
        docker_args+=(--tag "${DOCKER_TAG}")
    fi

    log "Docker 阶段: docker-build-push.sh ${docker_args[*]}"
    "${SCRIPT_DIR}/docker-build-push.sh" "${docker_args[@]}" \
        || die 4 "Docker 构建、测试或推送失败"
    DOCKER_STATUS="已完成"
}

print_final_report() {
    log "完成"
    printf '  同步提交数: %s\n' "${UPDATE_COUNT}"
    printf '  版本跨度: %s -> %s\n' "${VERSION_BEFORE}" "${VERSION_AFTER}"
    printf '  验证模式: %s\n' "$([[ "${FAST}" == "true" ]] && printf 'fast' || printf '完整')"
    printf '  推送结果: origin/main 与 origin/capybara/main 已推送\n'
    if [[ -n "${DOCKER_TAG}" ]]; then
        printf '  Docker: %s，tag=%s\n' "${DOCKER_STATUS}" "${DOCKER_TAG}"
    else
        printf '  Docker: %s\n' "${DOCKER_STATUS}"
    fi
}

main() {
    parse_args "$@"
    preflight
    phase_fetch
    refresh_main
    merge_capybara
    validate_merge || die 3 "Phase 4 验证失败；已停在 capybara/main 合并现场，禁止推送，请按 SYNC-UPSTREAM.md 的 P4 排查"
    smoke_check
    push_capybara
    run_docker_stage
    print_final_report
}

main "$@"
