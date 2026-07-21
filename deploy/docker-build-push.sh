#!/usr/bin/env bash
# 一键构建 Sub2API Docker 镜像；可选本地 compose 测试和 Docker Hub 双架构推送。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

IMAGE="taloslhan/sub2api"
BUILDER="multiarch"
TAG=""
LOCAL_PLATFORM=""
COMMIT=""
BUILD_COMMIT=""
BUILD_DATE=""
BUILD_REF="capybara/main"
USE_CURRENT=false
BUILD_CONTEXT=""
WORKTREE_DIR=""
RUN_TEST=false
RUN_PUSH=false
CLEANUP_AFTER_TEST=false
NO_CACHE=false
PRUNE_AFTER=true
PRUNE_KEEP_STORAGE="10gb"

usage() {
    cat <<'EOF'
用法:
  deploy/docker-build-push.sh [选项]

常用:
  deploy/docker-build-push.sh
      基于本地 capybara/main 分支构建当前机器架构镜像，不包含当前工作区未提交改动。

  deploy/docker-build-push.sh --test
      基于本地 capybara/main 构建镜像，生成 compose override，启动本地依赖并检查 /health。

  deploy/docker-build-push.sh --test --push
      本地测试通过后，构建 linux/amd64 + linux/arm64 并推送 Docker Hub。

  deploy/docker-build-push.sh --all
      等同于 --test --push。

选项:
  --image IMAGE        镜像名，默认 taloslhan/sub2api
  --tag TAG            版本 tag，默认上游版本 + git 短 hash + UTC 日期，如 0.1.155.a1b2c3d-20260703
  --platform PLATFORM  本地构建平台，默认按当前机器推断 linux/arm64 或 linux/amd64
  --builder NAME       buildx builder 名称，默认 multiarch
  --current            使用当前工作区作为构建上下文，包含当前已检出代码
  --ref REF            使用指定 git ref 作为构建上下文，默认 capybara/main
  --test               启动 deploy/docker-compose.local.yml 做本地健康检查
  --cleanup            --test 成功后执行 docker compose down
  --push               buildx 双架构构建并推送到远程仓库
  --all                等同于 --test --push
  --no-cache           Docker 构建禁用缓存
  --no-prune           跳过构建后的旧镜像 tag 与构建缓存自动回收（默认开启，
                       保留本次 tag 与 local，构建缓存回收至 10GB 以内）
  -h, --help           显示帮助

前置条件:
  1. Docker Desktop / Docker daemon 已启动。
  2. 推送前已执行 docker login -u taloslhan，并使用 Docker Hub Access Token 登录。
  3. 默认构建源为本地 capybara/main；如需最新远程代码，请先自行同步该本地分支。
EOF
}

log() {
    printf '[docker-build-push] %s\n' "$*"
}

die() {
    printf '[docker-build-push] 错误: %s\n' "$*" >&2
    exit 1
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --image)
                IMAGE="${2:?--image 需要参数}"
                shift 2
                ;;
            --tag)
                TAG="${2:?--tag 需要参数}"
                shift 2
                ;;
            --platform)
                LOCAL_PLATFORM="${2:?--platform 需要参数}"
                shift 2
                ;;
            --builder)
                BUILDER="${2:?--builder 需要参数}"
                shift 2
                ;;
            --current)
                USE_CURRENT=true
                shift
                ;;
            --ref)
                BUILD_REF="${2:?--ref 需要参数}"
                shift 2
                ;;
            --test)
                RUN_TEST=true
                shift
                ;;
            --cleanup)
                CLEANUP_AFTER_TEST=true
                shift
                ;;
            --push)
                RUN_PUSH=true
                shift
                ;;
            --all)
                RUN_TEST=true
                RUN_PUSH=true
                shift
                ;;
            --no-cache)
                NO_CACHE=true
                shift
                ;;
            --no-prune)
                PRUNE_AFTER=false
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                die "未知参数: $1"
                ;;
        esac
    done
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "未找到命令: $1"
}

ensure_docker_ready() {
    require_command docker
    docker info >/dev/null 2>&1 || die "Docker daemon 未响应，请先启动 Docker Desktop"
}

cleanup_worktree() {
    [[ -n "${WORKTREE_DIR}" ]] || return

    git -C "${REPO_ROOT}" worktree remove --force "${WORKTREE_DIR}" 2>/dev/null || true
    rmdir "$(dirname "${WORKTREE_DIR}")" 2>/dev/null || true
}

resolve_build_source() {
    require_command git

    if [[ "${USE_CURRENT}" == "true" ]]; then
        BUILD_CONTEXT="${REPO_ROOT}"
        BUILD_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
        log "构建源: 当前工作区 (${BUILD_COMMIT})"
        return
    fi

    git -C "${REPO_ROOT}" rev-parse --verify "${BUILD_REF}^{commit}" >/dev/null 2>&1 \
        || die "git ref 不存在或不是提交: ${BUILD_REF}"

    BUILD_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short "${BUILD_REF}^{commit}")"
    WORKTREE_DIR="$(mktemp -d)/src"
    trap cleanup_worktree EXIT
    git -C "${REPO_ROOT}" worktree add --detach "${WORKTREE_DIR}" "${BUILD_REF}" >/dev/null
    BUILD_CONTEXT="${WORKTREE_DIR}"

    log "构建源: ${BUILD_REF} (${BUILD_COMMIT}) -> ${BUILD_CONTEXT}"
}

resolve_tag() {
    local build_day
    local build_time
    local upstream_version=""
    local version_file="${BUILD_CONTEXT}/backend/cmd/server/VERSION"

    if [[ -n "${TAG}" ]]; then
        return
    fi

    build_day="$(date -u +%Y%m%d)"
    if [[ -f "${version_file}" ]]; then
        upstream_version="$(tr -d '[:space:]' <"${version_file}")"
    fi

    if [[ -z "${upstream_version}" ]]; then
        log "警告: 未从 ${version_file} 读取到上游版本，使用兼容格式生成 tag"
    fi

    if [[ -n "${BUILD_COMMIT}" ]]; then
        TAG="${BUILD_COMMIT}-${build_day}"
        if [[ -n "${upstream_version}" ]]; then
            TAG="${upstream_version}.${TAG}"
        fi
        return
    fi

    build_time="$(date -u +%H%M%S)"
    TAG="nogit-${build_day}-${build_time}"
    if [[ -n "${upstream_version}" ]]; then
        TAG="${upstream_version}.${TAG}"
    fi
}

resolve_local_platform() {
    if [[ -n "${LOCAL_PLATFORM}" ]]; then
        return
    fi

    case "$(uname -m)" in
        arm64|aarch64)
            LOCAL_PLATFORM="linux/arm64"
            ;;
        x86_64|amd64)
            LOCAL_PLATFORM="linux/amd64"
            ;;
        *)
            die "无法自动推断本地平台，请显式传入 --platform linux/amd64 或 linux/arm64"
            ;;
    esac
}

resolve_build_metadata() {
    COMMIT="${TAG}"
    if [[ -n "${BUILD_COMMIT}" ]]; then
        COMMIT="${BUILD_COMMIT}"
    fi
    BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

build_local_image() {
    local cache_args=()

    log "本地构建 ${IMAGE}:${TAG} (${LOCAL_PLATFORM})"

    if [[ "${NO_CACHE}" == "true" ]]; then
        cache_args+=(--no-cache)
    fi

    docker build \
        --platform "${LOCAL_PLATFORM}" \
        -t "${IMAGE}:${TAG}" \
        -t "${IMAGE}:local" \
        --build-arg "GOPROXY=https://goproxy.cn,direct" \
        --build-arg "GOSUMDB=sum.golang.google.cn" \
        --build-arg "VERSION=${TAG}" \
        --build-arg "COMMIT=${COMMIT}" \
        --build-arg "DATE=${BUILD_DATE}" \
        "${cache_args[@]}" \
        -f "${BUILD_CONTEXT}/Dockerfile" \
        "${BUILD_CONTEXT}"
}

write_compose_override() {
    local override_file="${SCRIPT_DIR}/docker-compose.override.local.yml"

    cat >"${override_file}" <<EOF
services:
  sub2api:
    image: ${IMAGE}:${TAG}
EOF
    log "已生成 ${override_file}"
}

ensure_local_env() {
    local env_file="${SCRIPT_DIR}/.env"
    local example_file="${SCRIPT_DIR}/.env.example"

    if [[ -f "${env_file}" ]]; then
        return
    fi

    cp "${example_file}" "${env_file}"
    log "已从 .env.example 创建 ${env_file}"
    log "提示: 当前 POSTGRES_PASSWORD 使用示例值，仅适合本地测试；生产环境必须修改"
}

compose_cmd() {
    docker compose \
        -f "${SCRIPT_DIR}/docker-compose.local.yml" \
        -f "${SCRIPT_DIR}/docker-compose.override.local.yml" \
        "$@"
}

resolve_health_url() {
    local published
    local host
    local port

    published="$(compose_cmd port sub2api 8080 2>/dev/null | tail -n 1 || true)"
    if [[ -z "${published}" ]]; then
        printf '%s\n' "http://localhost:8080/health"
        return
    fi

    host="${published%:*}"
    port="${published##*:}"
    case "${host}" in
        0.0.0.0|::|\[::\])
            host="localhost"
            ;;
    esac

    printf 'http://%s:%s/health\n' "${host}" "${port}"
}

wait_for_health() {
    local url="$1"
    local max_attempts=60

    log "等待健康检查 ${url}"
    for ((attempt = 1; attempt <= max_attempts; attempt++)); do
        if curl -fsS "${url}" >/dev/null 2>&1; then
            log "健康检查通过: ${url}"
            return
        fi
        sleep 2
    done

    log "sub2api 最近日志:"
    compose_cmd logs --tail=120 sub2api >&2 || true
    die "健康检查超时: ${url}"
}

run_local_test() {
    local health_url

    require_command curl
    ensure_local_env
    write_compose_override

    log "启动本地 compose 测试环境"
    compose_cmd up -d
    health_url="$(resolve_health_url)"
    wait_for_health "${health_url}"

    log "本地测试环境可访问: ${health_url%/health}"
    if [[ "${CLEANUP_AFTER_TEST}" == "true" ]]; then
        log "清理本地 compose 测试环境"
        compose_cmd down
    else
        log "测试环境保持运行；如需清理，执行: docker compose -f deploy/docker-compose.local.yml -f deploy/docker-compose.override.local.yml down"
    fi
}

ensure_buildx_builder() {
    if docker buildx inspect "${BUILDER}" >/dev/null 2>&1; then
        docker buildx use "${BUILDER}" >/dev/null
    else
        log "创建 buildx builder: ${BUILDER}"
        docker buildx create --name "${BUILDER}" --use >/dev/null
    fi

    docker buildx inspect --bootstrap >/dev/null
}

push_multiarch_image() {
    local cache_args=()

    ensure_buildx_builder

    log "双架构构建并推送 ${IMAGE}:latest 和 ${IMAGE}:${TAG}"
    if [[ "${NO_CACHE}" == "true" ]]; then
        cache_args+=(--no-cache)
    fi

    docker buildx build \
        --platform "linux/amd64,linux/arm64" \
        -t "${IMAGE}:latest" \
        -t "${IMAGE}:${TAG}" \
        --build-arg "GOPROXY=https://goproxy.cn,direct" \
        --build-arg "GOSUMDB=sum.golang.google.cn" \
        --build-arg "VERSION=${TAG}" \
        --build-arg "COMMIT=${COMMIT}" \
        --build-arg "DATE=${BUILD_DATE}" \
        "${cache_args[@]}" \
        --push \
        "${BUILD_CONTEXT}"

    log "检查远程 manifest"
    docker buildx imagetools inspect "${IMAGE}:latest"
}

# 删除本仓库除本次 TAG 和 local 之外的旧镜像 tag（历史版本均可从 Docker Hub 拉回）
prune_old_image_tags() {
    local old_tags

    old_tags="$(docker images "${IMAGE}" --format '{{.Tag}}' \
        | grep -Fxv -e "${TAG}" -e "local" -e "<none>" || true)"
    [[ -n "${old_tags}" ]] || return 0

    log "清理旧镜像 tag（保留 ${IMAGE}:${TAG} 与 ${IMAGE}:local）"
    while IFS= read -r old_tag; do
        docker rmi "${IMAGE}:${old_tag}" >/dev/null 2>&1 || true
    done <<<"${old_tags}"
}

# 构建后回收：旧镜像 tag、悬空镜像、超出上限的构建缓存
prune_build_artifacts() {
    [[ "${PRUNE_AFTER}" == "true" ]] || return 0

    prune_old_image_tags
    docker image prune -f >/dev/null 2>&1 || true

    log "回收构建缓存（保留 ${PRUNE_KEEP_STORAGE} 以内近期缓存）"
    docker builder prune --max-used-space "${PRUNE_KEEP_STORAGE}" -f >/dev/null 2>&1 || true
    if [[ "${RUN_PUSH}" == "true" ]]; then
        docker buildx prune --builder "${BUILDER}" --max-used-space "${PRUNE_KEEP_STORAGE}" -f >/dev/null 2>&1 || true
    fi
}

main() {
    parse_args "$@"
    ensure_docker_ready
    resolve_build_source
    resolve_tag
    resolve_local_platform
    resolve_build_metadata

    build_local_image

    if [[ "${RUN_TEST}" == "true" ]]; then
        run_local_test
    fi

    if [[ "${RUN_PUSH}" == "true" ]]; then
        push_multiarch_image
    fi

    prune_build_artifacts

    log "完成: ${IMAGE}:${TAG}"
}

main "$@"
