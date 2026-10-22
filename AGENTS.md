# Agent 协同与工程操作规范

本文档为在此代码库工作的 AI Agent 设定构建、部署与日常开发的约束规范。

## 1. 核心构建与部署原则

所有镜像构建与编译工作必须在本地开发机执行，严禁在远端 VPS 上执行源码编译或直接触发 Dockerfile 构建。

- **禁止远端构建**：VPS 实例硬件配置较低（通常为单核 CPU、1GB 内存），且缺乏 Docker 层级编译缓存。在远端直接构建会全量编译重型底层库（如纯 Go SQLite libc 模块、网络协议栈），极易耗尽算力、引发 OOM 甚至导致实例假死。
- **本地构建交付**：所有容器镜像必须在本地宿主机多核环境下交叉构建目标平台镜像（`linux/amd64`），打包导出后上传至 VPS 直接 `docker load`；或在本地完成静态二进制编译后进行分发交付。

## 2. 标准镜像构建与部署流程

向生产或测试 VPS 发布版本时，必须按以下步骤操作：

### 2.1 本地交叉构建镜像
```bash
# 在本地多核环境下构建 Linux AMD64 镜像
docker buildx build --platform linux/amd64 -t gemini-web2api-go:local --load .
```

### 2.2 本地导出与压缩
```bash
# 导出为压缩包（体积通常约 25MB~35MB）
docker save gemini-web2api-go:local | gzip > /tmp/gemini-web2api-image.tar.gz
```

### 2.3 传输与镜像加载
```bash
# 推送至远端并加载
scp -o ServerAliveInterval=15 /tmp/gemini-web2api-image.tar.gz gcp-vps:/tmp/
ssh -o ServerAliveInterval=15 gcp-vps "docker load < /tmp/gemini-web2api-image.tar.gz && rm -f /tmp/gemini-web2api-image.tar.gz"
```

### 2.4 平滑重启服务
```bash
# 平滑重建容器，确保持久化卷与 Cookie 文件不被覆盖
ssh -o ServerAliveInterval=15 gcp-vps "cd /root/gemini-web2api-go && docker compose up -d --force-recreate"
```

## 3. 开发与运维约束

1. **最小改动原则**：仅修改需求范围内的必要代码，禁止跨模块非必要格式化或臆测性重构。
2. **社区与公共描述合规**：向上游 PR、Issue 或向外输出文本中，严禁出现“逆向”等高危词汇，统一规范使用“兼容支持”、“多模态接口扩展”等中性表述。
3. **数据与持久化保护**：操作容器时严禁执行 `docker compose down -v` 或删除挂载目录，必须严格保护 SQLite 数据库（`gw2a-data`）与 Cookie 凭据。
