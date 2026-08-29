import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """请担任规划员。项目 notion2api（Notion AI → OpenAI 兼容桥，Go，GALIAIS 框架）。请基于对 Claude 家族模型行为机制的深入了解，从第一性视角设计方案——上游模型就是 Claude 系，你比任何人都懂它们被提示时的反应模式。

【现状（全部实测）】
1. 服务走顺式策略（prompt_guard 全前缀改顺式：不否定"Notion AI 身份"，把"完成请求"定义为助手本职），普通问答稳定（"你好"自我介绍 / "1+1" 都正常回复）。
2. 工具桥：请求带 tools 时注入"工作区技术文档整理"框架（compile technical documentation in this workspace），模型输出 ```json action 块 → 提取为 tool_calls；路径 mask 成 ~/ 工作区路径。
3. 上游行为：Notion 上游 Claude 系模型（opus-5=agave-flan 等）对"读取本地文件 C:\\...\\x.txt"类请求返回**身份拒绝**："我是 AI 助手，无法访问你电脑上的本地路径"（措辞变体多：'本地路径对我来说不存在'/'只读虚拟沙盒'/'没有任何方式去打开它'）。拒绝后服务端 synthesizer 从拒绝文本提取路径合成 tool_calls，但 CC 实测中合成后客户端并未执行（或合成未触发）。
4. 模型输出工具块的概率性：同一注入同请求，有时输出 ```json action 块（成功），有时直接身份拒绝（失败）；近期漂移后拒绝比例升高。
5. 用户拍板：**先能稳定回复消息（已完成），工具调用恢复优先级其后**；用户明确要求设计"让上游模型更愿意配合"的办法（顺着身份，不否定、不越权声明）。

【任务：设计上游身份限制的应对策略】
从 Claude 系模型被提示时的真实反应机制出发（训练时的拒绝模式、角色一致性、系统提示 vs 用户提示的权重、工具使用微调、对敏感词与本地文件语义的触发），回答：
A. **为什么会拒绝**：上游模型看到什么特征会触发"本地文件访问=越权"拒绝？（绝对路径？'read file'？工具描述里的本地语义？'你电脑'措辞？）给出机制解释，不要泛泛而谈。
B. **怎么让它愿意做**：设计 2-3 套提示词/上下文策略，要求"顺着 Notion AI 身份"（不否定身份、不越权声明），例如：
   - 把"读取文件"重构成"工作区文档整理/资料核对"任务框架（现有工具桥就是这个方向，为何不稳定？）
   - 系统提示 vs 用户提示里放什么？few-shot 示例怎么组织（对 assistant 前缀示例的模仿率）？
   - 工具描述（function description）怎么写才不触发"本地文件"敏感词？
   - 路径形态：绝对路径 → ~/ → ./ → /workspace/ → 无路径（让模型猜）？
   - 身份锚定：让上游模型认为"用户是在工作区里协作的同事，文件是共享资料"？
C. **稳定回复的保底**：如果上游模型无论如何拒绝，服务端应该怎么兜底才不破坏对话体验（当前 synthesizer 为何在 CC 场景没生效——它走 /v1/chat/completions 流式，LatestUserPrompt 提取路径？ToolBridgeSection 判定？CC 客户端是否消费了 tool_calls？）。
D. **执行清单**：按"改动小→见效快→风险低"排序，每项给出：改哪个文件、怎么改、怎么验证（红灯先行）。
E. **review 检查点**：5 条以内。

输出：A-E，中文 800 字内。这是规划阶段，不要写代码，给方案。""",
        }
    ],
}

req = urllib.request.Request(
    "http://127.0.0.1:8787/v1/chat/completions",
    data=json.dumps(body).encode("utf-8"),
    headers={"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"},
)
try:
    r = json.loads(urllib.request.urlopen(req, timeout=300).read().decode("utf-8"))
    print(r["choices"][0]["message"]["content"])
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:300])