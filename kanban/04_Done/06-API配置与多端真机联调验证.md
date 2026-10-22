---
priority: high
tags: [integration, testing, settings]
created: 2026-10-08
---

# 06-API配置与多端真机联调验证

### 📌 任务目标
实现家长设置面板（配置服务地址如 `http://192.168.x.x:8082`、API Key、模型名称），打通与本地 `gemini-web2api-go` 的完整生图联调，确保在 iPad 浏览器及桌面端操作流畅稳定。

### 📋 子任务清单 (Checklist)
- [x] 子任务 1：实现带简易防误触的“家长设置齿轮”弹窗（配置保存至 localStorage）
- [x] 子任务 2：对接 `gemini-web2api-go` `/v1/chat/completions` API，处理 loading 动效与错误拦截
- [x] 子任务 3：优化 Pad 触控防止双击缩放与滚动穿透（Touch Action & Overscroll）
- [x] 子任务 4：编写本地测试与多场景验证脚本，确保端到端可用

### 📝 开发记录与进度
- *2026-10-08 13:40*：创建任务卡片。
- *2026-10-08 13:44*：完成端到端生图接口联调验证（HTTP 200，成功返回并解析 249KB Base64 图片）。

### 🔗 关联文件 / 依赖
- 代码路径：`kids-painter.html`
- 服务地址：`http://127.0.0.1:8082`

