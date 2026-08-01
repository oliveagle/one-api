# Responses API CRUD 端点实施计划

## 目标

为 OpenAI Responses API 添加完整的 CRUD 支持：
1. `GET /v1/responses/{response_id}` - 获取 response
2. `DELETE /v1/responses/{response_id}` - 删除 response
3. `POST /v1/responses/{response_id}/cancel` - 取消 response
4. `GET /v1/responses/{response_id}/input_items` - 列出 input items

同时添加 README 文档和测试。

## 设计决策

**方案：单处理器方法分支**
- 在 `RelayResponsesHelper` 中根据 HTTP 方法和路径分发到不同处理函数
- 所有端点共享相同的 relay mode (`Responses`)、adaptor/middleware 链
- GET/DELETE/cancel/input_items **不计费**（不消耗 tokens）

## 实施步骤

### Phase 1: Router 注册 (router/relay.go)

在 `POST /responses` 后添加 4 个新路由：
```go
relayV1Router.GET("/responses/:response_id", controller.Relay)
relayV1Router.DELETE("/responses/:response_id", controller.Relay)
relayV1Router.POST("/responses/:response_id/cancel", controller.Relay)
relayV1Router.GET("/responses/:response_id/input_items", controller.Relay)
```

### Phase 2: Middleware 适配 (middleware/distributor.go)

修改 `Distribute()` 处理无 body 的 GET/DELETE 请求：
- 对于这些方法，允许空 model
- 使用空 model 进行 channel 选择（随机或基于 token 允许列表）

### Phase 3: Controller 扩展 (relay/controller/responses.go)

1. **重命名现有函数**：将 `RelayResponsesHelper` 主体重命名为 `relayResponsesCreate`
2. **添加方法分发**：根据 HTTP 方法和路径分发到不同处理器
3. **实现 4 个新处理器**：
   - `relayResponsesGet` - 转发 GET，无计费
   - `relayResponsesDelete` - 转发 DELETE，无计费
   - `relayResponsesCancel` - 转发 POST (cancel)，无计费
   - `relayResponsesInputItems` - 转发 GET (input_items)，无计费
4. **添加通用转发函数** `forwardResponse` - 复制响应头、状态码、body

### Phase 4: 测试

**Router 测试** (router/relay_test.go)：
- 添加 4 个新路由到 expected routes 表

**Controller 测试** (relay/controller/responses_test.go)：
- 测试方法分发逻辑
- 验证各端点不触发计费
- 测试路径参数传递

### Phase 5: 文档更新

**README.md**：
- 添加 Responses API 支持的端点列表
- 说明 passthrough 模式和上游兼容性限制

**ADR** (docs/adr/0001-openai-responses-api-passthrough.md)：
- 更新实现说明，包含新端点

## 文件变更清单

| 文件 | 变更 |
|------|------|
| `router/relay.go` | 添加 4 个新路由 |
| `router/relay_test.go` | 添加路由测试 |
| `middleware/distributor.go` | 处理 GET/DELETE 无 body 请求 |
| `relay/controller/responses.go` | 添加方法分发和 4 个新处理器 |
| `relay/controller/responses_test.go` | 添加新端点测试 |
| `README.md` | 添加 Responses API 文档 |
| `docs/adr/0001-openai-responses-api-passthrough.md` | 更新 ADR |

## 验收标准

- [ ] 所有 4 个新端点可以正确路由
- [ ] GET/DELETE/cancel/input_items 不计费
- [ ] 路径参数正确传递到上游
- [ ] 错误响应正确处理
- [ ] 单元测试通过
- [ ] Router 测试通过
- [ ] README 文档更新
- [ ] ADR 文档更新