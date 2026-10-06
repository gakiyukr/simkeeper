#!/usr/bin/env bash
# SimKeeper 一键部署 / 更新 / 卸载脚本（在 VPS 上以 root 运行）。
#
# 用法：
#   bash simkeeper.sh install  [--version vX.Y.Z]     首次安装（默认拉 GitHub 最新 Release）
#   bash simkeeper.sh update   [--version vX.Y.Z | --file 本地二进制]
#                                                      升级：自动备份 → 替换 → 健康检查 → 失败回滚
#   bash simkeeper.sh remove   [--purge]              卸载（默认保留数据；--purge 连数据一起删）
#
# 环境变量：SIMKEEPER_TZ（install 写入 systemd 的时区，默认 Asia/Shanghai）
set -euo pipefail

REPO="gakiyukr/simkeeper"
INSTALL_DIR="/opt/simkeeper"
DATA_DIR="/var/lib/simkeeper"
SERVICE="simkeeper"
SERVICE_FILE="/etc/systemd/system/${SERVICE}.service"
DEFAULT_TZ="Asia/Shanghai"
KEEP_BACKUPS=5

log() { echo "[simkeeper] $*"; }
die() { echo "[simkeeper] 错误：$*" >&2; exit 1; }
need_root() { [ "$(id -u)" = 0 ] || die "请以 root 运行本脚本"; }

# arch 输出当前机器对应的 release 资产后缀（amd64 / arm64）。
arch() {
  case "$(uname -m)" in
    x86_64) echo "amd64" ;;
    aarch64 | arm64) echo "arm64" ;;
    *) die "不支持的架构：$(uname -m)" ;;
  esac
}

# download_binary <version|latest> <目标路径>
download_binary() {
  local version="$1" dest="$2"
  local asset="simkeeper-linux-$(arch)"
  local url
  if [ "$version" = "latest" ]; then
    url="https://github.com/${REPO}/releases/latest/download/${asset}"
  else
    url="https://github.com/${REPO}/releases/download/${version}/${asset}"
  fi
  log "下载 ${url}"
  curl -fL --retry 3 --connect-timeout 15 -o "${dest}.tmp" "$url" || die "下载失败（可改用 --file 指定本地二进制）"
  mv "${dest}.tmp" "$dest"
}

# service_port 从 systemd 单元里解析 -addr 的端口（健康检查用）。
service_port() {
  local port
  port=$(sed -n 's/.*-addr 127\.0\.0\.1:\([0-9]*\).*/\1/p' "$SERVICE_FILE" 2>/dev/null | head -1)
  echo "${port:-8090}"
}

# prune_backups 只保留最近 KEEP_BACKUPS 份备份。
prune_backups() {
  local prefix="$1"
  ls -1t "${prefix}".bak-* 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -f
}

write_unit() {
  cat > "$SERVICE_FILE" <<UNIT
[Unit]
Description=SimKeeper - eSIM keep-alive reminder
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE}
Group=${SERVICE}
ExecStart=${INSTALL_DIR}/simkeeper -db ${DATA_DIR}/simkeeper.db -addr 127.0.0.1:8090 -trust-proxy -secure-cookies
# 时区影响提醒的「当天」判断，务必与业务时区一致
Environment=TZ=${SIMKEEPER_TZ:-${DEFAULT_TZ}}
Restart=on-failure
RestartSec=5s

# 基础沙箱：程序只需要读写 ${DATA_DIR}（数据库）和 /tmp
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${DATA_DIR}
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
UNIT
}

cmd_install() {
  need_root
  local version="latest"
  while [ $# -gt 0 ]; do
    case "$1" in
      --version) version="$2"; shift 2 ;;
      *) die "install 未知参数：$1" ;;
    esac
  done

  id -u "$SERVICE" >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin -d "$DATA_DIR" "$SERVICE"
  mkdir -p "$INSTALL_DIR" "$DATA_DIR"

  download_binary "$version" "${INSTALL_DIR}/simkeeper.new"
  chmod 755 "${INSTALL_DIR}/simkeeper.new"
  mv "${INSTALL_DIR}/simkeeper.new" "${INSTALL_DIR}/simkeeper"

  if [ ! -f "$SERVICE_FILE" ]; then
    write_unit
    log "已写入 ${SERVICE_FILE}（如需改端口/参数，请直接编辑该文件）"
  else
    log "检测到已有 systemd 单元，保留不覆盖"
  fi
  chown -R "${SERVICE}:${SERVICE}" "$DATA_DIR"
  systemctl daemon-reload
  systemctl enable --now "$SERVICE"
  sleep 2

  if systemctl is-active --quiet "$SERVICE"; then
    log "安装完成，服务运行中"
    log "本机地址：http://127.0.0.1:$(service_port)（首次启动请访问 /setup 创建账号）"
    log "下一步：配置反向代理（HTTPS）后即可通过域名访问，参考 DEPLOY.md"
  else
    journalctl -u "$SERVICE" -n 20 --no-pager
    die "服务启动失败，请查看上方日志"
  fi
}

cmd_update() {
  need_root
  [ -f "$SERVICE_FILE" ] || die "尚未安装（先运行 install）"
  local version="latest" file=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --version) version="$2"; shift 2 ;;
      --file) file="$2"; shift 2 ;;
      *) die "update 未知参数：$1" ;;
    esac
  done

  local stamp
  stamp=$(date +%Y%m%d-%H%M%S)
  local running=0
  systemctl is-active --quiet "$SERVICE" && running=1

  log "停止服务并备份..."
  systemctl stop "$SERVICE"
  cp -a "${DATA_DIR}/simkeeper.db" "${DATA_DIR}/simkeeper.db.bak-${stamp}"
  [ -f "${DATA_DIR}/simkeeper.db-wal" ] && cp -a "${DATA_DIR}/simkeeper.db-wal" "${DATA_DIR}/simkeeper.db-wal.bak-${stamp}"
  cp -a "${INSTALL_DIR}/simkeeper" "${INSTALL_DIR}/simkeeper.bak-${stamp}"
  prune_backups "${DATA_DIR}/simkeeper.db"
  prune_backups "${INSTALL_DIR}/simkeeper"

  log "获取新版本二进制..."
  if [ -n "$file" ]; then
    cp -a "$file" "${INSTALL_DIR}/simkeeper.new"
  else
    download_binary "$version" "${INSTALL_DIR}/simkeeper.new"
  fi
  chmod 755 "${INSTALL_DIR}/simkeeper.new"

  log "替换并启动..."
  mv "${INSTALL_DIR}/simkeeper.new" "${INSTALL_DIR}/simkeeper"
  chown root:root "${INSTALL_DIR}/simkeeper"
  systemctl start "$SERVICE"
  sleep 2

  if systemctl is-active --quiet "$SERVICE" &&
    curl -sf -o /dev/null "http://127.0.0.1:$(service_port)/login"; then
    log "升级完成，服务健康（版本见 journalctl -u ${SERVICE} | tail）"
    log "本次备份：simkeeper.db.bak-${stamp} / simkeeper.bak-${stamp}"
  else
    log "启动或健康检查失败，回滚到上一版本"
    journalctl -u "$SERVICE" -n 15 --no-pager || true
    cp -a "${INSTALL_DIR}/simkeeper.bak-${stamp}" "${INSTALL_DIR}/simkeeper"
    systemctl start "$SERVICE"
    sleep 2
    if systemctl is-active --quiet "$SERVICE"; then
      die "已回滚并恢复运行；新版本未能启动，请检查日志"
    else
      die "回滚后仍未运行，数据备份在 ${DATA_DIR}（.bak-${stamp}），请人工介入"
    fi
  fi
}

cmd_remove() {
  need_root
  local purge=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --purge) purge=1; shift ;;
      *) die "remove 未知参数：$1" ;;
    esac
  done

  if [ -f "$SERVICE_FILE" ]; then
    systemctl disable --now "$SERVICE" || true
  fi
  rm -f "$SERVICE_FILE"
  systemctl daemon-reload
  rm -rf "$INSTALL_DIR"

  if [ "$purge" = 1 ]; then
    echo "将彻底删除数据目录 ${DATA_DIR}（数据库、密钥、备份）。输入 yes 确认："
    read -r answer
    [ "$answer" = "yes" ] || die "已取消（数据保留）"
    rm -rf "$DATA_DIR"
    id -u "$SERVICE" >/dev/null 2>&1 && userdel "$SERVICE" || true
    log "已彻底移除"
  else
    log "已卸载程序；数据保留在 ${DATA_DIR}（含 secret.key 与备份），彻底删除请加 --purge"
  fi
}

usage() {
  cat <<USAGE
用法：bash simkeeper.sh <命令> [参数]

  install  [--version vX.Y.Z]              首次安装（默认下载最新 Release）
  update   [--version vX.Y.Z | --file 路径] 升级（自动备份 + 健康检查 + 失败回滚）
  remove   [--purge]                        卸载（默认保留数据）

环境变量：SIMKEEPER_TZ=Asia/Shanghai（install 时写入 systemd 单元）
USAGE
  exit 1
}

case "${1:-}" in
  install) shift; cmd_install "$@" ;;
  update) shift; cmd_update "$@" ;;
  remove | uninstall) shift; cmd_remove "$@" ;;
  *) usage ;;
esac
