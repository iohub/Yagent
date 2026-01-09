
## Prompt进阶工程化实践

### 1. 结构化控制 (Structured Control)

*   **XML/Markdown标签隔离上下文**：
    对于长文本处理，使用清晰的标签将指令（Instructions）、数据（Data）和输出要求（Output Rules）物理隔离开。这不仅能防止模型混淆指令与内容，还能防止Prompt注入攻击。
    *   *Best Practice:*
        ```text
        <documents>
        {{在此处粘贴参考文档}}
        </documents>

        <instructions>
        请分析上述 <documents> 标签中的内容...
        </instructions>
        ```

*   **Pre-filling（引导输出/填空）**：
    不要只把指令发给模型，还可以替模型写好“开头”。这能强制模型遵循特定格式或语气。
    *   *技巧：* 如果你想要JSON格式，在Assistant的对话历史中预填一个 `{` ；如果你想要它不废话直接开始，预填 `Sure, here is the code:`。
    *   *适用场景：* Claude和Gemini对此特别敏感，能有效跳过“好的，我会为您...”这类客套话。

### 2. 思维架构设计 (Cognitive Architecture)

*   **Inner Monologue / Hidden Thinking（思维隔离/内心独白）**：
    强制模型在输出最终答案前，先在一个隔离的区块中“自言自语”进行推理。这能显著提高逻辑正确率，同时保持最终用户看到的输出整洁。
    *   *Instruction:* "Before answering, think step-by-step inside `<thinking>` tags. Afterward, provide your final answer inside `<answer>` tags."
    *   *代码解析：* 后续可以通过正则提取 `<answer>` 中的内容展示给用户，忽略 `<thinking>` 内容。

*   **Self-Correction / Reflection（自我纠错）**：
    对于高风险任务，要求模型在给出最终结论前先“反驳”自己。
    *   *Template:* "First, generate a draft response. Then, check the draft for factual errors or logic gaps. Finally, print the corrected best version."

### 3. 防幻觉与基准锚定 (Grounding & Anti-Hallucination)

*   **明确的拒答机制 (Explicit Refusal)**：
    模型天生倾向于讨好用户，这会导致编造答案。必须显式定义“如果不知道该怎么做”。
    *   *Command:* "Answer using **only** the provided context. If the answer is not contained in the documents, explicitly state 'Data Not Available' and do not try to make up an answer."

*   **引用归因 (Citation Requirement)**：
    要求模型在陈述事实时，必须标注来源文档的具体段落或ID。
    *   *Format:* "Every claim must be cited with [Doc ID: Quote snippet]." 这会倒逼模型去原文寻找证据，大幅降低幻觉。

### 4. 任务链与复杂任务拆解 (Chain of Prompts)

不要试图用一个巨型Prompt（Mega-Prompt）解决所有问题。
*   **Pipeline Approach**：
    *   **Step 1 Prompt**: "提取这篇非结构化文本中的所有关键实体（姓名、日期、事件）并输出为JSON。"
    *   **Step 2 Prompt** (将Step 1的输出作为输入): "根据提取的实体列表，撰写一篇摘要。"
    *   **优势**：每一步更易调试（Debug），且容错率高。

### 5. 系统提示与元提示 (System & Meta-Prompting)

*   **System Prompt的分离**：
    将*角色设定*（我是谁）、*边界条件*（不做违规的事）和*语气风格*写入 **System Message**；将具体的*任务指令*写入 **User Message**。这种分离让模型更稳定地保持“人设”。

*   **Meta-Prompting (让AI写Prompt)**：
    即使是专家，手动写复杂Prompt也很累。使用“元提示”让更强的模型（如GPT-4o或Claude 3.5 Sonnet）为你生成Prompt。
    *   *Prompt示例:* "You represent an expert Prompt Engineer. I need to achieve task X. Please write a highly optimized prompt using best practices (Chain-of-Thought, Delimiters, Persona) that I can paste into an LLM."

---

## 推荐的通用Prompt模板结构 (Framework)

根据最佳实践，一个健壮的Prompt通常包含以下模块：

```markdown
# Role (角色)
You are an expert financial analyst. Your tone should be professional and concise.

# Context (背景信息)
I will provide you with a quarterly earnings report text.
<context>
{{DATA_INPUT_HERE}}
</context>

# Task (任务)
Extract the key financial metrics (Revenue, Net Income, EPS) from the text provided above.

# Constraints (约束与规则)
1. Only extract data present in the text. If a metric is missing, write "N/A".
2. Output purely in JSON format. Do not include markdown code blocks (```json).
3. Think specifically about edge cases where numbers might be stated in millions vs billions.

# Few-Shot Examples (少样本示例 - 必填项!)
Input: "Revenue hit 5M, up 2%."
Output: {"Revenue": "5M", "Growth": "2%"}

# Output Instructions (思维与输出指令)
Please first evaluate the text in a <thinking> block to identify where the metrics are located.
Then, output your final JSON result inside <json> tags.
```

---

## 一、基础结构与格式 (Structure & Formatting)

### 1. Assign a Persona with Nuance（赋予具体的角色设定）
**不只是:** "你是个翻译。"
**最佳实践:** "你是一位精通中文和英文俚语的资深翻译专家，你的翻译风格侧重于信达雅，专门为科技博客撰写内容。"
**原理:** 角色设定激活了模型在特定领域的潜在空间（Latent Space），使其调用相关的词汇和语气。

### 2. Use Delimiters to Segment Text（使用分隔符分割文本）
**最佳实践:** 显式地用特殊符号（如 `###`, `---`, `"""`）或 XML 标签（如 `<context>`, `<input>`) 将指令、上下文和用户输入分开。
**示例:**
```
总结以下 <article> 标签内的文本。
<article>
[文本内容]
</article>
```
**原理:** 防止模型混淆哪部分是“命令”，哪部分是“需要处理的数据”。

### 3. Place Instructions at the End（将关键指令放在最后）
**最佳实践:** 对于长文本处理（Long-context），在给完长篇背景资料后，务必在Prompt的**末尾**再次重复核心指令。
**原理:** 模型存在“近因效应（Recency Bias）”，对末尾的信息关注度最高，避免“中间迷失（Lost in the Middle）”现象。

### 4. Provide Few-Shot Examples（提供少样本示例）
**最佳实践:** 至少提供 3-5 个“输入->输出”的对应样本。如果任务很复杂，示例应覆盖不同的边缘情况。
**技巧:** 即使是简单的格式要求，给一个样本通常比写三行解释更有效。

### 5. Specify Output Format Strictness（严格指定输出格式）
**最佳实践:** 如果需要程序解析结果，请明确：“Output strictly in valid JSON”，“不要包含 markdown 代码块标记”，“不要输出引言或解释性文字”。
**技巧:** 对于Claude和OpenAI较新的模型，可以使用 JSON Schema 或 Function Calling 强制约束输出结构。

---

## 二、逻辑增强与推理 (Reasoning & Logic)

### 6. Zero-Shot & Few-Shot Chain of Thought（思维链提示）
**最佳实践:** 
*   **Zero-Shot:** 在指令中加入魔法咒语 "Let's think step by step."
*   **Few-Shot CoT:** 在提供的示例中，展示“问题 -> **思考过程** -> 答案”，而不仅仅是“问题 -> 答案”。
**原理:** 强迫模型消耗更多的 Token 来生成逻辑推演，大幅提升数学、代码和推理题的准确率。

### 7. Inner Monologue / Hidden Thinking（隐性思维/内心独白）
**最佳实践:** 指示模型先在一个独立的标签（如 `<thinking>`）中进行草稿演算，然后再在 `<answer>` 标签中输出最终结果。
**指令示例:** "Before responding, outline your logic inside <thinking> tags. Then provided the user-facing response."
**价值:** 将混乱的推理过程与最终整洁的交付物分离开来。

### 8. Self-Consistency / Review Step（自我一致性/复查）
**最佳实践:** 要求模型“Review your answer”（复查答案）。
**示例:** “在你写出代码后，先逐行检查是否有 Bug 或逻辑错误，如果不确定，请尝试修复，然后再输出最终代码。”

### 9. Ask for Clarification（让模型主动提问）
**最佳实践:** "如果输入的信息不足以让你做出自信的判断，请向我提问，直到你有足够的信息为止。"
**原理:** 改变模型“通过瞎猜来强行回答”的默认行为，转变为交互式引导。

---

## 三、精准控制与防御 (Control & Safety)

### 10. Explicit "I Don't Know" Policy（显式的拒答机制）
**最佳实践:** 明确告诉模型边界：“Use **only** the provided text. If the answer is not in the text, simply say 'Unknown', do not generate facts.”
**原理:** 這是减少幻觉（Hallucination）最有效的手段。

### 11. Breakdown Complex Tasks (Chained Prompts)（拆解复杂任务/提示链）
**最佳实践:** 
*   错误做法：在一个 Prompt 里要求“读取文件、分类、翻译、并写摘要”。
*   正确做法：步骤 1：读取并提取实体 -> 输出 A；步骤 2：基于 A 进行分类 -> 输出 B...
**原理:** 每一个子任务的 Token 上下文更纯净，错误率成倍降低。

### 12. Cross-Lingual Reasoning Strategy（跨语言推理策略）
**最佳实践:** 对于需要深度逻辑的任务（如复杂的法律分析或代码解释），如果你希望输出中文，尝试要求模型：“Please think in English, then translate the final answer to Chinese.”
**原理:** 大多数模型的训练语料中英语质量最高、逻辑最强。让其在“英语区”进行思考，再翻译回来，效果往往优于直接用中文思考。

---

## 四、工程化优化 (Optimization & Engineering)

### 13. Pre-filling the Response（预填回复/引导）
**最佳实践:** (特别是针对 API 用户) 将 Assistant 的回复开头预先填好。
**示例:**
*   User: "Please output the data in JSON."
*   Assistant (Prefilled): "```json\n{"
**原理:** 这通过“强制补全”的模式，跳过了模型的拒绝或废话，直接锁定格式。

### 14. Reference Text Anchoring（参考文本锚定）
**最佳实践:** 在要求回答问题时，强制模型引用来源。“For every statement you make, cite the source paragraph ID like [3].”
**原理:** 迫使模型在上下文中寻找证据（Grounding），增加可信度。


### 15. Avoid Negative Constraints（慎用否定指令）
**最佳实践:** 尽量将“不要做 X”改为“只能做 Y”。
*   较差: "不要写很长的句子。"
*   更好: "请保持句子简短，每句控制在 15 个单词以内。"
**原理:** 就像对人说“不要想这头大象”，否定指令有时反而会激活相关的概念。

