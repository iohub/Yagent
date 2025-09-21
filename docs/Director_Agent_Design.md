# Director Agent 设计文档

## 1. 概述

### 1.1 设计目标
实现一个 Director Agent 作为系统的核心协调者，负责与用户沟通并指挥其他专业 Agent 完成任务。Director Agent 将作为用户的主要交互界面，通过工具调用的方式委托具体任务给专业化的子 Agent。

### 1.2 核心概念
- **Director Agent**: 主协调者，负责理解用户意图、制定执行计划、协调子 Agent
- **Specialized Agents**: 专业化的子 Agent，包括 Code Agent、Planning Agent、Repository Agent
- **Tool-based Delegation**: 通过工具调用机制实现 Agent 间的协作
- **Unified Interface**: 为用户提供统一的交互界面

## 2. 系统架构

### 2.1 整体架构图
```
┌─────────────────────────────────────────────────────────────┐
│                    Director Agent                           │
│  ┌─────────────────┐  ┌─────────────────┐  ┌──────────────┐ │
│  │   User Interface│  │  Task Planning  │  │ Coordination │ │
│  │   & Communication│  │   & Decision    │  │   & Control  │ │
│  └─────────────────┘  └─────────────────┘  └──────────────┘ │
└─────────────────────────────────────────────────────────────┘
                              │
                              │ Tool Calls
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                Specialized Agents                           │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │ Code Agent  │  │Planning Agent│  │ Repository Agent    │ │
│  │             │  │             │  │                     │ │
│  │ • Code Gen  │  │ • Analysis  │  │ • Code Analysis     │ │
│  │ • Debugging │  │ • Planning  │  │ • Architecture      │ │
│  │ • Testing   │  │ • Design    │  │ • Documentation     │ │
│  └─────────────┘  └─────────────┘  └─────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 组件关系
- Director Agent 作为顶层协调者
- 各专业 Agent 通过工具接口暴露能力
- 统一的配置和消息系统支持所有 Agent
- 共享的对话记忆和状态管理

## 3. Director Agent 设计

### 3.1 核心职责
1. **用户交互管理**: 处理用户输入，理解意图和需求
2. **任务分解**: 将复杂任务分解为可执行的子任务
3. **Agent 选择**: 根据任务类型选择合适的专业 Agent
4. **执行协调**: 管理任务执行流程和依赖关系
5. **结果整合**: 整合各 Agent 的执行结果
6. **状态管理**: 维护全局任务状态和进度

### 3.2 主要功能模块

#### 3.2.1 用户交互模块
```go
type UserInteractionManager struct {
    conversationMemory *ConversationMemory
    intentAnalyzer     *IntentAnalyzer
    responseGenerator  *ResponseGenerator
}
```

#### 3.2.2 任务规划模块
```go
type TaskPlanner struct {
    taskDecomposer    *TaskDecomposer
    agentSelector     *AgentSelector
    executionPlanner  *ExecutionPlanner
}
```

#### 3.2.3 协调控制模块
```go
type CoordinationController struct {
    agentManager      *AgentManager
    executionTracker  *ExecutionTracker
    resultAggregator  *ResultAggregator
}
```

### 3.3 Director Agent 工具集
Director Agent 将拥有以下工具来调用专业 Agent：

1. **delegate_to_code_agent**: 委托代码相关任务
2. **delegate_to_planning_agent**: 委托规划和设计任务
3. **delegate_to_repository_agent**: 委托代码库分析任务
4. **coordinate_agents**: 协调多个 Agent 协作
5. **get_task_status**: 获取任务执行状态
6. **aggregate_results**: 整合执行结果

## 4. 专业 Agent 设计

### 4.1 Code Agent
**职责**: 代码生成、调试、测试、重构
**工具集**: 继承现有的代码操作工具
**特殊能力**:
- 代码生成和修改
- 调试和错误修复
- 单元测试编写
- 代码重构和优化

### 4.2 Planning Agent
**职责**: 技术分析、架构设计、实施规划
**工具集**: 分析和规划专用工具
**特殊能力**:
- 需求分析和技术评估
- 架构设计和模式选择
- 实施计划制定
- 风险评估和缓解

### 4.3 Repository Agent
**职责**: 代码库分析、文档生成、架构理解
**工具集**: 代码库分析专用工具
**特殊能力**:
- 代码库结构分析
- 依赖关系分析
- 架构文档生成
- 代码质量评估

## 5. 实现计划

### 5.1 阶段一：基础架构 (Week 1-2)

#### 5.1.1 创建 Director Agent 基础结构
- [ ] 创建 `director_agent.go` 文件
- [ ] 实现 Director Agent 核心结构体
- [ ] 集成现有的消息系统和配置管理
- [ ] 实现基础的对话管理功能

#### 5.1.2 定义 Agent 工具接口
- [ ] 在 `tools.json` 中添加 Director Agent 工具定义
- [ ] 实现工具调用处理逻辑
- [ ] 建立 Agent 间的通信协议

#### 5.1.3 重构现有 Agent 为工具化
- [ ] 将现有的 CodingAssistant 重构为 Code Agent
- [ ] 创建 Planning Agent 基础结构
- [ ] 创建 Repository Agent 基础结构
- [ ] 实现 Agent 工具化接口

### 5.2 阶段二：核心功能实现 (Week 3-4)

#### 5.2.1 Director Agent 核心功能
- [ ] 实现用户意图分析
- [ ] 实现任务分解逻辑
- [ ] 实现 Agent 选择算法
- [ ] 实现执行协调机制

#### 5.2.2 专业 Agent 功能完善
- [ ] 完善 Code Agent 功能
- [ ] 实现 Planning Agent 核心功能
- [ ] 实现 Repository Agent 核心功能
- [ ] 优化 Agent 间协作机制

#### 5.2.3 状态管理和进度跟踪
- [ ] 实现全局任务状态管理
- [ ] 实现执行进度跟踪
- [ ] 实现结果聚合机制
- [ ] 实现错误处理和恢复

### 5.3 阶段三：集成和优化 (Week 5-6)

#### 5.3.1 系统集成
- [ ] 集成 Director Agent 到主程序
- [ ] 更新 WebSocket 和 HTTP 接口
- [ ] 实现配置管理更新
- [ ] 完善日志和监控

#### 5.3.2 性能优化
- [ ] 优化 Agent 间通信效率
- [ ] 实现并行任务执行
- [ ] 优化内存使用
- [ ] 实现缓存机制

#### 5.3.3 测试和文档
- [ ] 编写单元测试
- [ ] 编写集成测试
- [ ] 更新 API 文档
- [ ] 编写用户使用指南

## 6. 技术实现细节

### 6.1 文件结构
```
internal/assistant/
├── director_agent.go          # Director Agent 主文件
├── agents/
│   ├── code_agent.go         # Code Agent 实现
│   ├── planning_agent.go     # Planning Agent 实现
│   ├── repository_agent.go   # Repository Agent 实现
│   └── agent_interface.go    # Agent 通用接口
├── coordination/
│   ├── task_planner.go       # 任务规划器
│   ├── agent_selector.go     # Agent 选择器
│   └── execution_controller.go # 执行控制器
└── tools/
    ├── director_tools.go     # Director Agent 工具
    └── agent_delegation.go   # Agent 委托工具
```

### 6.2 核心数据结构

#### 6.2.1 Director Agent 结构
```go
type DirectorAgent struct {
    client              *Client
    conversationMemory  *ConversationMemory
    userInteractionMgr  *UserInteractionManager
    taskPlanner         *TaskPlanner
    coordinationCtrl    *CoordinationController
    agentManager        *AgentManager
    publisher           *MessagePublisher
}
```

#### 6.2.2 任务结构
```go
type Task struct {
    ID          string
    Type        TaskType
    Description string
    Status      TaskStatus
    AssignedAgent string
    Dependencies []string
    Results     map[string]interface{}
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

#### 6.2.3 Agent 接口
```go
type Agent interface {
    GetName() string
    GetCapabilities() []string
    ExecuteTask(ctx context.Context, task *Task) (*TaskResult, error)
    GetStatus() AgentStatus
    SetPublisher(publisher *MessagePublisher)
}
```

### 6.3 工具定义

#### 6.3.1 Director Agent 工具
```json
{
    "name": "delegate_to_code_agent",
    "description": "Delegate coding tasks to the specialized Code Agent",
    "parameters": {
        "type": "object",
        "properties": {
            "task_description": {
                "type": "string",
                "description": "Detailed description of the coding task"
            },
            "project_context": {
                "type": "string", 
                "description": "Project context and requirements"
            },
            "priority": {
                "type": "string",
                "enum": ["low", "medium", "high", "urgent"],
                "description": "Task priority level"
            }
        },
        "required": ["task_description"]
    }
}
```

### 6.4 配置更新
在 `config.toml` 中添加 Director Agent 配置：
```toml
[director_agent]
enabled = true
max_concurrent_tasks = 5
default_timeout = "10m"
agent_selection_strategy = "capability_based"

[director_agent.agents]
code_agent_enabled = true
planning_agent_enabled = true
repository_agent_enabled = true

[director_agent.coordination]
parallel_execution = true
result_aggregation_timeout = "30s"
error_retry_attempts = 3
```

## 7. 使用场景示例

### 7.1 复杂功能开发
**用户输入**: "我需要为这个项目添加用户认证功能"

**Director Agent 处理流程**:
1. 分析用户意图：需要实现用户认证功能
2. 任务分解：
   - 分析现有代码结构 (Repository Agent)
   - 设计认证架构 (Planning Agent)
   - 实现认证代码 (Code Agent)
3. 协调执行：
   - 先调用 Repository Agent 分析代码库
   - 基于分析结果调用 Planning Agent 设计架构
   - 最后调用 Code Agent 实现功能
4. 整合结果并反馈给用户

### 7.2 代码重构
**用户输入**: "这个模块的代码太复杂了，需要重构"

**Director Agent 处理流程**:
1. 调用 Repository Agent 分析模块复杂度
2. 调用 Planning Agent 制定重构计划
3. 调用 Code Agent 执行重构
4. 验证重构结果并生成报告

## 8. 优势和价值

### 8.1 用户体验提升
- **统一界面**: 用户只需与 Director Agent 交互
- **智能路由**: 自动选择最适合的 Agent 处理任务
- **透明协调**: 用户无需关心底层 Agent 协作细节

### 8.2 系统可维护性
- **模块化设计**: 各 Agent 职责清晰，易于维护
- **可扩展性**: 易于添加新的专业 Agent
- **可测试性**: 每个 Agent 可独立测试

### 8.3 功能专业化
- **深度优化**: 每个 Agent 可针对特定领域深度优化
- **工具特化**: 不同 Agent 可使用最适合的工具集
- **知识积累**: 专业 Agent 可积累领域特定知识

## 9. 风险评估和缓解

### 9.1 技术风险
- **复杂性增加**: 通过清晰的架构设计和文档缓解
- **性能影响**: 通过优化和缓存机制缓解
- **调试困难**: 通过完善的日志和监控缓解

### 9.2 实施风险
- **开发周期**: 分阶段实施，确保每个阶段都有可用功能
- **兼容性**: 保持向后兼容，渐进式迁移
- **测试覆盖**: 建立完善的测试体系

## 10. 总结

Director Agent 的设计将为系统带来更好的用户体验和更强的功能扩展性。通过专业化的 Agent 分工和智能的任务协调，系统能够更高效地处理复杂的编程任务，同时保持清晰的架构和良好的可维护性。

实施过程中需要重点关注：
1. 保持现有功能的稳定性
2. 确保新架构的可扩展性
3. 提供完善的测试和文档
4. 逐步迁移和优化用户体验
