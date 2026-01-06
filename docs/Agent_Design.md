
# 编码 Agent (Coding Agent) 系统设计与架构文档

该设计采用了**Hub-and-Spoke (星型/中枢型)** 多智能体模式，强调中央管控与专业分工，并引入了“反思（Thinking）”机制以增强系统的鲁棒性。

## 1. 架构概览 (Architecture Overview)

本系统采用 **Hub-and-Spoke (中枢-辐条)** 多智能体架构。系统由一个核心的 **Conductor Agent (指挥家)** 负责全局调度，协调多个功能单一的 **Sub-Agents (子智能体)** 完成具体的代码分析与编写任务。

### 核心设计原则
*   **集中决策，分布执行**：Conductor 掌握上下文全貌，Sub-Agents 专注具体领域的执行。
*   **规划-执行-评估循环**：Conductor 动态维护任务列表，根据 Sub-Agent 的反馈调整后续计划。
*   **自我修正机制**：通过 Thinking 工具，在执行受阻时强制进入反思模式，避免死循环。

---

## 2. 角色定义与职责 (Roles & Responsibilities)

### 2.1. Conductor Agent (中枢/指挥家)
**定位**：项目经理 + 技术主管 (Tech Lead)。它是唯一直接与用户交互的 Agent。

**核心职责**：
1.  **任务分析 (Task Analysis)**：解析用户模糊的需求，拆解为一系列具体的、可执行的 TODO 列表。
2.  **上下文管理 (Context Management)**：维护全局状态（哪些文件被修改了，当前任务进度，环境依赖等）。
3.  **任务委派 (Delegation)**：根据 TODO 类型，唤起 `Repo-Agent` 或 `Coding-Agent`。
4.  **结果评审 (Review & React)**：
    *   接收 Sub-Agent 的返回结果。
    *   **Accept**：标记当前 TODO 完成，推进下一个。
    *   **Reject/Refine**：如果结果不符合预期或引入新 Bug，生成新的修正任务重新委派。

### 2.2. Sub-Agent: Repo-Agent (仓库分析员)
**定位**：资深架构师/代码审计员。只读模式，不修改代码。

**核心职责**：
1.  **代码检索**：根据自然语言描述找到相关的代码文件、函数或类定义。
2.  **依赖分析**：分析改动可能影响的依赖链。
3.  **上下文提供**：为 Coding-Agent 准备必要的背景知识（如：找出定义了 `User` 类的文件路径）。

### 2.3. Sub-Agent: Coding-Agent (编码工程师)
**定位**：高级开发工程师。拥有文件系统的读写权限。

**核心职责**：
1.  **代码编写**：执行具体的增删改操作。
2.  **验证与测试**：编写并运行测试用例，验证改动有效性。
3.  **错误修复**：遇到报错时，利用 Thinking 工具进行自我修正。

---

## 3. 工具系统设计 (Tool System Design)

工具分为三类：**基础工具 (Shared)**、**反思工具 (Cognitive)** 和 **专用工具 (Specialized)**。

### 3.1. 基础工具 (Shared Tools)
所有 Agent (Conductor 可见只读部分) 均可调用的基础能力。

*   `read_file(file_path)`: 读取文件内容。
*   `list_dir(dir_path)`: 查看目录结构。
*   `code_search(query)`: 语义或关键词搜索代码库 (基于 grep 或 向量数据库)。

### 3.2. 反思工具 (Thinking Tool)
**名称**：`thinking`
**适用场景**：当 Sub-Agent 执行出错（如编译失败、测试不通过、找不到文件）时，**强制**优先调用此工具。
**输入**：
*   `error_message`: 错误信息。
*   `current_action`: 导致错误的操作。
*   `observation`: 当前的观察结果。
**行为**：
并不直接执行外部操作，而是生成一段“思维链 (Chain of Thought)”日志，分析错误原因，提出 2-3 种解决方案，并选择最优解。
**作用**：打破 Agent 遇到报错盲目重试的死循环。

### 3.3. 专用工具补充 (For Sub-Agents)

#### 为 Repo-Agent 补充：
*   `get_file_tree(depth)`:以此生成项目的整体结构树。
*   `find_references(symbol)`: 查找某个类或函数的引用位置。

#### 为 Coding-Agent 补充 (必要补充):
你提到的 Coding Agent 需要更多工具来完成闭环开发：

1.  **文件修改类**:
    *   `write_file(file_path, content)`: 创建或全量覆盖文件。
    *   `apply_patch(file_path, diff_content)`: **(推荐)** 使用 unified diff 格式修改文件，比全量覆盖更节省 Token 且精准。
    *   或者 `replace_block(file_path, search_block, replace_block)`: 精确查找替换代码块。

2.  **环境执行类 (Execution Tools)**:
    *   `run_shell_command(command)`: **(核心)** 执行 shell 命令。用于安装依赖 (`pip install`), 运行测试 (`pytest`), 运行 Linter (`eslint`)。
    *   *安全限制*：需设置白名单或沙箱环境，禁止 `rm -rf /` 等高危命令。

3.  **代码质量与调试类**:
    *   `run_linter(file_path)`: 静态代码分析，提前发现语法错误。
    *   `syntax_check(code_snippet, language)`: 检查代码片段语法是否正确（在写入文件前预检）。

---

## 4. 工作流 (Workflow)

### 阶段一：规划 (Conductor Phase)
1.  **User**: "请把项目中的登录验证从 Session 改为 JWT。"
2.  **Conductor**:
    *   调用 `Repo-Agent` 获取仓库信息、项目结构、依赖列表、目录树等。
    *   生成 Plan/Todo:
        1. [Coding] 修改 auth.py 实现 JWT 生成。
        2. [Coding] 修改 middleware.py 验证 JWT。
        3. [Coding] 运行测试验证。

### 阶段二：分析 (Repo-Agent Phase)
1.  **Conductor**: 委派任务 "找到处理登录逻辑的代码"。
2.  **Repo-Agent**:
    *   调用 `code_search("login auth session")`。
    *   发现 `src/auth.py` 和 `src/routes/login.py`。
    *   返回分析结果给 Conductor。

### 阶段三：执行与反思 (Coding-Agent Phase)
1.  **Conductor**: 委派任务 "修改 src/auth.py 实现 JWT 生成"。
2.  **Coding-Agent**:
    *   调用 `read_file("src/auth.py")`。
    *   生成修改后的代码。
    *   调用 `apply_patch` 修改文件。
    *   **Self-Correction Loop**:
        *   Agent 主动调用 `run_shell_command("pytest tests/test_auth.py")`。
        *   **结果**: Failed. `ImportError: No module named 'jwt'`.
        *   **Action**: Agent 调用 `thinking`。
            *   *Think*: "我修改了代码但忘记安装依赖。我需要先安装库。"
        *   **Action**: 调用 `run_shell_command("pip install pyjwt")`。
        *   **Action**: 再次运行测试 -> Passed。
    *   返回结果 "Success" 给 Conductor。

### 阶段四：验收 (Conductor Phase)
1.  **Conductor**: 检查 Coding-Agent 的输出。
2.  **Decision**: 既然测试通过，标记 Todo 完成。继续下一项。

---

## 5. 状态管理 (State Management - Prompt Context)

为了保证 Conductor 能够有效指挥，Prompt 中需要维护以下结构化信息：

```json
{
  "project_summary": "简短的项目描述",
  "task_list": [
    {"id": 1, "desc": "定位登录代码", "status": "done", "result": "位于 src/auth.py"},
    {"id": 2, "desc": "实现 JWT 逻辑", "status": "in_progress", "assigned_to": "Coding-Agent"}
  ],
  "knowledge_graph": {
    "modified_files": ["src/auth.py"],
    "new_dependencies": ["pyjwt"]
  },
  "last_error": null
}
```

## 6. 总结 (Summary)

该设计通过 **Conductor** 保证了任务的逻辑连贯性，通过 **Repo-Agent** 提高了信息检索的准确度，通过 **Coding-Agent** 配合 **Thinking Tool** 和 **Execution Tools** 实现了具备自我纠错能力的自动化编码。这种架构极大地降低了单一 Agent 因上下文过长而“迷失”的风险。