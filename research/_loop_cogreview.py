import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """请担任提示词工程顾问，回答一个格式一致性问题（few-shot 提示工程常见问题，与内容无关）：

我们有一个 JSON 输出 few-shot 模板，模板中 assistant 示例使用以下格式：
```json
{"name": "read_file", "arguments": {"path": "~/Desktop/testfile.txt", "max_lines": 10}}
```
模板同时声明了工具的 JSON 描述（- **read_file**: 读取本地文件）。

实测中模型常输出扁平变体（真实观测到）：
```json
{"operation": "read_file", "path": "~/Desktop/testfile.txt"}
```
键名从 name/arguments 漂移到 operation/path（扁平化：把参数直接放顶层）。

问题：
A. 这是提示词工程中常见的"格式漂移"现象。从模板设计的角度，怎么调整措辞/示例来显著提高键名遵循率？（比如把期望键名显式写进模板？在示例里重复强调？使用 schema 风格声明？）
B. 社区常见的 JSON 工具调用键名约定有哪些变体（name/arguments、action、tool、operation、function、call 等）？解析器做兼容时，合理的兼容面是什么（什么该兼容、什么该拒绝）？
C. 防止"正文污染"：模型在输出 JSON 示例块前后附带解释性文本（如"先用这个示例块："），这类文本是否应该允许？还是应该通过模板措辞抑制？如果要抑制，模板怎么写？
D. 给出模板改进的 2-3 个具体方案，按"改动小→见效快→副作用小"排序，每个注明改动位置（示例部分/声明部分/措辞部分）。
E. 检查点 5 条（供后续 review 使用）。

输出 A-E，中文 600 字内。""",
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