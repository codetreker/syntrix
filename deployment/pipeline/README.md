# Pipeline Infrastructure

Lightweight Docker Compose setup for CI/CD pipelines.

## Services

| Service    | Port  | Description                         |
|------------|-------|-------------------------------------|
| MongoDB    | 27017 | Document storage (replica set)      |
| PostgreSQL | 5432  | User storage                        |
| NATS       | 4222  | Message broker with JetStream       |

## Usage

```bash
# Start all services
docker compose -f deployment/pipeline/docker-compose.yml up -d

# Check status
docker compose -f deployment/pipeline/docker-compose.yml ps

# Stop all services
docker compose -f deployment/pipeline/docker-compose.yml down
```

## Connection Strings

- **MongoDB**: `mongodb://localhost:27017`
- **PostgreSQL**: `postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable`
- **NATS**: `nats://localhost:4222`

## Differences from Dev Environment

This setup is optimized for CI/CD:
- No persistent volumes (ephemeral storage)
- No monitoring stack (Prometheus, Grafana)
- Faster health check intervals
- Minimal resource allocation

## Go CI checks

服务端工作流并行执行构建和 race/coverage 两个 job，各自限时五分钟。只有测试
job 启动上述服务。测试不依赖构建产物，生成的协议源码已提交到仓库。拆分 job
使普通构建的冷缓存编译不再消耗 race 测试的执行预算。

必需检查 `Syntrix Server (Go)` 仅在两个 job 均成功时通过。即使依赖失败、取消
或跳过，该检查仍会执行并拒绝这些结果。覆盖率命令保留固定版本 go-cov 的 CI
门槛：race 检测、包/函数/总覆盖率，以及 critical 未覆盖代码块。
本地 `make coverage` 只报告覆盖率；使用 `CI=true make coverage` 验证相同的
CI 门槛。
