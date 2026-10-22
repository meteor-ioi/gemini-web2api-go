#!/bin/bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$DIR/gemini-web2api-go"
LOG_DIR="$DIR/logs"
STDOUT_LOG="$LOG_DIR/stdout.log"
STDERR_LOG="$LOG_DIR/stderr.log"
PLIST_LABEL="com.gemini.web2api"
PLIST_PATH="$HOME/Library/LaunchAgents/${PLIST_LABEL}.plist"

PORT="8082"
API_KEY="C&P3fyqZ1x0#"
PROXY="http://127.0.0.1:7897"

mkdir -p "$LOG_DIR"
mkdir -p "$HOME/Library/LaunchAgents"

generate_plist() {
  cat <<EOF > "$PLIST_PATH"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${PLIST_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${BIN}</string>
        <string>--port</string>
        <string>${PORT}</string>
        <string>--api-key</string>
        <string>${API_KEY}</string>
        <string>--proxy</string>
        <string>${PROXY}</string>
    </array>
    <key>WorkingDirectory</key>
    <string>${DIR}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${STDOUT_LOG}</string>
    <key>StandardErrorPath</key>
    <string>${STDERR_LOG}</string>
</dict>
</plist>
EOF
}

load_service() {
  launchctl bootout "gui/$(id -u)/${PLIST_LABEL}" 2>/dev/null || launchctl unload "$PLIST_PATH" 2>/dev/null || true
  if launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null; then
    :
  else
    launchctl load -w "$PLIST_PATH" 2>/dev/null || true
  fi
}

unload_service() {
  launchctl bootout "gui/$(id -u)/${PLIST_LABEL}" 2>/dev/null || launchctl unload -w "$PLIST_PATH" 2>/dev/null || true
}

case "$1" in
  install)
    echo "==> 检查二进制程序..."
    if [ ! -f "$BIN" ]; then
      echo "二进制文件不存在，开始编译..."
      (cd "$DIR" && go build -o gemini-web2api-go .)
    fi
    echo "==> 生成 LaunchAgent 配置文件: $PLIST_PATH"
    generate_plist
    echo "==> 注册并启动开机自启服务..."
    load_service
    echo "==> 开机自启服务安装完成！"
    sleep 1
    "$0" status
    ;;
  uninstall)
    echo "==> 正在卸载自启服务..."
    unload_service
    rm -f "$PLIST_PATH"
    echo "==> 已解除开机自启动并清理 plist。"
    ;;
  start)
    if [ ! -f "$PLIST_PATH" ]; then
      generate_plist
    fi
    load_service
    echo "==> 服务已启动。"
    sleep 1
    "$0" status
    ;;
  stop)
    unload_service
    echo "==> 服务已停止。"
    ;;
  restart)
    unload_service
    sleep 1
    if [ ! -f "$PLIST_PATH" ]; then
      generate_plist
    fi
    load_service
    echo "==> 服务已重启。"
    sleep 1
    "$0" status
    ;;
  status)
    PID=$(lsof -nP -iTCP:${PORT} -sTCP:LISTEN -t 2>/dev/null || true)
    if [ -n "$PID" ]; then
      echo "状态: [运行中] PID: $PID, 端口: $PORT"
      launchctl list | grep "${PLIST_LABEL}" || true
    else
      echo "状态: [未运行]"
    fi
    ;;
  log)
    tail -n 30 -f "$STDERR_LOG" "$STDOUT_LOG"
    ;;
  *)
    echo "用法: $0 {install|uninstall|start|stop|restart|status|log}"
    exit 1
    ;;
esac
