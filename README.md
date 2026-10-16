# TypeSafe AI Go SDK

[![CI](https://github.com/guchengod/typesafe-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/guchengod/typesafe-sdk-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/guchengod/typesafe-sdk-go.svg)](https://pkg.go.dev/github.com/guchengod/typesafe-sdk-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[**简体中文**](#typesafe-ai-go-sdk) | [**English**](#english-version)

---

[TypeSafe AI](https://typesafe.ai) 官方 Go 语言客户端 SDK —— 用于对任意文本、结构化数据或图像进行确定性定性判断、分类评级与目标定位（System One 定性决策模型）。

单次调用可并发评估数十个问题，直接返回校准概率、选项标签、评分以及边界框坐标，无需解析和清洗自由格式的大模型文本。

- **纯标准库实现**：仅依赖 `net/http`、`encoding/json` 与 `log/slog`，零外部依赖，极速编译。
- **天然并发安全**：`Client` 不保存每请求可变状态，底层连接池高效复用，方法均支持 `context.Context`。
- **自动透明压缩**：请求体 $\ge 1\text{ KiB}$ 自动启用标准 Gzip 压缩，大幅缩减网络带宽和传输延迟。
- **原生多模态与目标定位**：全面支持 `Image` 图片资产与 `Box` 图像目标检测边界框问答。
- **健壮默认重试**：基于指数退避加抖动的自适应重试，深度支持服务端 `Retry-After` 标头。
- **强类型设计**：全链路结构体类型支持，可配合 `errors.As` 提取细分错误，亦可通过 `SystemOneAs[T]` 反序列化到自定义业务结构体。
- **开箱即用 CLI**：附带纯标准库打造的命令行工具 `cmd/typesafe`，完美适配 Shell 脚本与流水线。

> 需要 **Go 1.26 或更高版本**。

---

## 目录

- [安装](#安装)
- [快速上手](#快速上手)
- [核心问答原语](#核心问答原语)
- [多模态图像与目标定位 (Box)](#多模态图像与目标定位-box)
- [结构化泛型解码 (`SystemOneAs`)](#结构化泛型解码-systemoneas)
- [模型使用规范与模式指南](#模型使用规范与模式指南)
- [🤖 AI Agent 专用系统提示词](#-ai-agent-专用系统提示词)
- [命令行工具 (CLI)](#命令行工具-cli)
- [错误处理](#错误处理)
- [English Version](#english-version)

---

## 安装

```bash
go get github.com/guchengod/typesafe-sdk-go
```

```go
import typesafe "github.com/guchengod/typesafe-sdk-go"
```

---

## 快速上手

配置环境变量 `TYPESAFE_API_KEY`：

```bash
export TYPESAFE_API_KEY="ts_live_..."
```

编写代码发起评估：

```go
package main

import (
	"context"
	"fmt"
	"log"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.NewClient() // 自动读取 TYPESAFE_API_KEY
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	response, err := client.SystemOne(
		context.Background(),
		map[string]any{
			"channel":  "email",
			"document": "信用卡账单 #4912 被重复扣费了，请立即为我办理退款。",
		},
		typesafe.Questions{
			"billing": typesafe.NewNoul("该内容是否与账单或退款相关？"),
			"category": typesafe.NewChoice(
				map[string]any{
					"billing":   "扣费、退款或发票问题",
					"technical": "系统故障、崩溃或 API 问题",
					"other":     nil, // nil 表示该选项无额外细化描述
				},
				typesafe.WithInstructions("该工单应指派到哪个部门？"),
			),
			"urgency": typesafe.NewScore(
				[]any{"可等待至下周", "需在 48 小时内处理", "需当天立即处理"},
				typesafe.WithInstructions("客户情绪和问题的紧急程度如何？"),
			),
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("billing p(true) = %.2f\n", response.Nouls()["billing"].Noul)
	fmt.Printf("category        = %s (置信度 %.2f)\n", response.Choices()["category"].Choice, response.Choices()["category"].Confidence)
	fmt.Printf("urgency score   = %.2f / 2 (置信度 %.2f)\n", response.Scores()["urgency"].Score, response.Scores()["urgency"].Confidence)
}
```

---

## 核心问答原语

TypeSafe 提供了四种面向结构化决策的确定性原子问题类型：

| 原语 | 构造函数 | 输出结果 | 典型场景 |
| --- | --- | --- | --- |
| **Noul** | `NewNoul(instruction, ...)` | `NoulAnswer.Noul` (0.0 ~ 1.0 的 Yes 概率) | 二元判断、安全合规审核、意图判别 |
| **Choice** | `NewChoice(criteria, ...)` | `ChoiceAnswer.Choice` (最高概率选项), `Probabilities`, `Confidence` | 多分类路由、工单分流（最多支持 255 个选项） |
| **Score** | `NewScore(criteria, ...)` | `ScoreAnswer.Score` (概率加权期望分), `Confidence`, `Legend` | 评级打分、情绪指数、优先级评定（支持 2~10 级） |
| **Box** | `NewBox(instruction, ...)` | `BoxAnswer.Box` (上下左右边界分数), `Confidence` | 图像目标定位、目标裁剪与坐标识别 |

---

## 多模态图像与目标定位 (Box)

TypeSafe 支持在 `state`、问题指令或准则中直接包含图像，并使用 `Box` 原语在图像中精准定位物体。

> 开启图片或 Box 问题需要使用 `jev-preview` 模型，并附带 `TypeSafe-Danger-Zone-Images: true` 标头（通过 `typesafe.WithImages(true)` 或调用选项 `typesafe.WithCallImages(true)` 开启）。

```go
package main

import (
	"context"
	"fmt"
	"log"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.NewClient(typesafe.WithImages(true))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	// 从文件加载图像
	photo, err := typesafe.NewImageFromFile("kitchen.jpg")
	if err != nil {
		log.Fatal(err)
	}

	// 图像可直接嵌入 state 中，SDK 将自动收集并维护引用关系
	resp, err := client.SystemOne(
		context.Background(),
		map[string]any{"photo": photo},
		typesafe.Questions{
			"has_cup": typesafe.NewNoul("`photo` 中是否有咖啡杯？"),
			"cup_box": typesafe.NewBox("`photo` 中的咖啡杯"),
		},
		typesafe.WithModel(typesafe.ModelJevPreview),
	)
	if err != nil {
		log.Fatal(err)
	}

	if resp.Nouls()["has_cup"].Noul > 0.8 {
		box := resp.Boxes()["cup_box"].Box
		fmt.Printf("找到咖啡杯！边界: Top=%.2f, Bottom=%.2f, Left=%.2f, Right=%.2f\n",
			box.Top, box.Bottom, box.Left, box.Right)
	}
}
```

---

## 结构化泛型解码 (`SystemOneAs`)

使用 `SystemOneAs[T]` 可直接将返回结果映射到自定义业务结构体中：

```go
type TicketAnalysis struct {
	Model    string                  `json:"model"`
	Usage    typesafe.Usage          `json:"usage"`
	Billing  typesafe.NoulAnswer     `json:"billing"`
	Category typesafe.ChoiceAnswer   `json:"category"`
	Urgency  typesafe.ScoreAnswer    `json:"urgency"`
	Target   *typesafe.BoxAnswer     `json:"target,omitempty"`
}

result, err := typesafe.SystemOneAs[TicketAnalysis](ctx, client, state, questions)
if err != nil {
	log.Fatal(err)
}
fmt.Println(result.Category.Choice, result.Billing.Noul)
```

---

## 模型使用规范与模式指南

TypeSafe 的 **Jev** 模型专为离散决策与定性判断设计，输出为校准概率分布。请将算术计算和确定性逻辑保留在 Go 代码中：

### 1. 置信度三段式路由 (Confidence-Gated Routing)
每个 `Choice` 和 `Score` 答案都包含 `Confidence` (0.0 ~ 1.0)。推荐使用三段式分流策略：
- **高置信度 ($\ge 0.85$)**：模型极度确信，执行全自动化分支；
- **中置信度 ($0.50 \sim 0.85$)**：模型倾向某选项但有轻微歧义，标记给操作员复核；
- **低置信度 ($< 0.50$)**：模型明确表达“我不确定”，回退到人工兜底流程。

### 2. 计数与投机性扇出 (Counting & Speculative Fan-Out)
Jev 不擅长数数。**统计数量时请在 Go 中执行计算**：在一通 `SystemOne` 请求中扇出多个原子的 `Noul` 问题，再由 Go 代码汇总满足条件的计数。

### 3. Score 评分准则边界
- 严禁将 `score` 作为连续实数用于插值还原数学物理量；
- `Score` 用于阈值门禁过滤（如 `score >= 1.5`）；
- `Score` 级数限制为 **2 至 10 级**，`Choice` 选项最多 **255 个**。

---

## 🤖 AI Agent 专用系统提示词

如果你正在使用 AI 编程助手（如 **Cursor**、**Claude Code**、**GitHub Copilot**、**Windsurf**、**Antigravity** 或各类 Coding Agent），将以下提示词添加到项目中的 `.cursorrules`、`CLAUDE.md` 或 Agent 系统提示词中，可以让 Agent 零失误地直接调用和编写本库：

````markdown
### TypeSafe AI Go SDK Coding Guidelines for Agent

You are working with the official TypeSafe AI Go SDK (`github.com/guchengod/typesafe-sdk-go`).
TypeSafe uses calibrated System One models (Jev) for deterministic classification, scoring, verification, and bounding-box object location. It does NOT generate unstructured free text.

#### 1. Core Principles
- Keep arithmetic, counting, string parsing, and business logic strictly in Go. Use Jev strictly for qualitative judgments.
- Always reuse a single `*typesafe.Client` (concurrency-safe, pools connections). Close it when done.
- Pass `context.Context` to all API calls.

#### 2. Initializing Client
```go
import typesafe "github.com/guchengod/typesafe-sdk-go"

client, err := typesafe.NewClient(
    // Optional configurations:
    // typesafe.WithAPIKey("ts_live_..."), // defaults to TYPESAFE_API_KEY env var
    // typesafe.WithTimeout(30 * time.Second),
    // typesafe.WithImages(true), // Danger zone: enables image & box questions
    // typesafe.WithLongContext(true), // Danger zone: enables 262k token window
    // typesafe.WithCompression(true), // Enabled by default for bodies >= 1 KiB
)
```

#### 3. Defining Questions
Use `typesafe.Questions` (map[string]typesafe.Question):
- **Yes/No Probability**: `typesafe.NewNoul("Question text", opts...)`
  Result: `response.Nouls()["key"].Noul` (float64, 0.0=No, 1.0=Yes)
- **Categorization**: `typesafe.NewChoice(map[string]any{"label": "description", "other": nil}, typesafe.WithInstructions("..."))`
  Max 255 options. Result: `response.Choices()["key"].Choice` (string), `.Confidence` (float64)
- **Rubric Rating**: `typesafe.NewScore([]any{"level0", "level1", "level2"}, typesafe.WithInstructions("..."))`
  Between 2 and 10 levels. Result: `response.Scores()["key"].Score` (float64, weighted expectation), `.Confidence` (float64)
- **Object Localization**: `typesafe.NewBox("the target object in `photo`")`
  Requires `typesafe.WithModel(typesafe.ModelJevPreview)` and `typesafe.WithImages(true)` or `typesafe.WithCallImages(true)`.
  Result: `response.Boxes()["key"].Box` (`Top`, `Bottom`, `Left`, `Right` in fractions 0..1), `.Confidence` (*float64)

#### 4. Multimodal Images
```go
img, err := typesafe.NewImageFromFile("path/to/image.jpg") // PNG or JPEG, max 16 MiB
// Embed img directly in state map or slice. The SDK automatically serializes it to a reference string and populates the assets payload.
resp, err := client.SystemOne(ctx, map[string]any{"photo": img}, questions,
    typesafe.WithModel(typesafe.ModelJevPreview),
    typesafe.WithCallImages(true),
)
```

#### 5. Confidence-Gated Decision Routing
Always inspect `Confidence` on Choice/Score:
- `Confidence >= 0.85`: High certainty, trigger automated action.
- `0.50 <= Confidence < 0.85`: Ambiguous, flag for human review.
- `Confidence < 0.50`: Uncertain, route to human triage fallback.

#### 6. Strongly-Typed Decoding (`SystemOneAs`)
Decode straight into a custom struct:
```go
type MyResult struct {
    Billing  typesafe.NoulAnswer   `json:"billing"`
    Category typesafe.ChoiceAnswer `json:"category"`
    Urgency  typesafe.ScoreAnswer  `json:"urgency"`
}
res, err := typesafe.SystemOneAs[MyResult](ctx, client, state, questions)
```

#### 7. Error Handling
SDK errors are strongly typed:
```go
if apiErr, ok := typesafe.AsRateLimitError(err); ok {
    wait, _ := apiErr.RetryAfter()
    time.Sleep(wait)
} else if ptlErr, ok := typesafe.AsPayloadTooLargeError(err); ok {
    // 413 Payload Too Large (>32 MiB)
} else if apiErr, ok := typesafe.AsAPIError(err); ok {
    log.Printf("API error %d: %s (req_id=%s)", apiErr.Status, apiErr.Message, apiErr.RequestID)
}
```
````

---

## 命令行工具 (CLI)

SDK 内置了轻量级 CLI 工具 `typesafe`：

```bash
go install github.com/guchengod/typesafe-sdk-go/cmd/typesafe@latest
```

使用示例：

```bash
export TYPESAFE_API_KEY="ts_live_..."

# 查看可用模型
typesafe models --format text

# 发起问答评估（支持文本与 JSON 输出）
typesafe ask \
  --state '{"body": "发票 #4912 被扣了两次钱。"}' \
  --noul 'billing=这是账单问题吗？' \
  --choice 'queue=billing:财务账单,technical:技术故障,other' \
  --score 'urgency=日常,紧急,严重' \
  --box 'target=图像中的目标物体' \
  --format text
```

配合 `jq` 在 CI/CD 流水线中进行自动化质检：

```bash
curl -s https://example.test/ticket.json | typesafe ask --state - --questions questions.json | jq '.answers'
```

---

## 错误处理

SDK 提供了完整的强类型错误体系：

```go
resp, err := client.SystemOne(ctx, state, questions)
if err != nil {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		log.Println("调用超时")
	default:
		if rateLimitErr, ok := typesafe.AsRateLimitError(err); ok {
			wait, _ := rateLimitErr.RetryAfter()
			log.Printf("触发限频，建议等待: %v", wait)
		} else if ptlErr, ok := typesafe.AsPayloadTooLargeError(err); ok {
			log.Printf("请求体超过 32 MiB 上限: %v", ptlErr)
		} else if apiErr, ok := typesafe.AsAPIError(err); ok {
			log.Printf("API 响应错误: status=%d, request_id=%s", apiErr.Status, apiErr.RequestID)
		}
	}
}
```

---
---

## English Version

Go SDK for [TypeSafe AI](https://typesafe.ai) — calibrated, deterministic System One evaluation models designed for classification, qualitative grading, verification, and bounding box object localization over text, structured JSON, and images.

Ask dozens of questions in a single parallel call and receive calibrated probabilities, categorical labels, scores, and bounding box coordinates rather than unstructured LLM text that requires fragile parsing.

### Highlights

- **Standard Library Only**: Pure Go using `net/http`, `encoding/json`, and `log/slog`. Zero external dependencies.
- **Concurrency-Safe**: Client instances are stateless across calls and share HTTP connection pools. All methods accept `context.Context`.
- **Automatic Gzip Compression**: Transparently compresses request bodies $\ge 1\text{ KiB}$ with standard gzip.
- **Multimodal & Object Localization**: First-class support for `Image` assets and `Box` bounding box question primitives.
- **Resilient Retries**: Exponential backoff with jitter, honoring upstream `Retry-After` response headers.
- **Strongly Typed**: Typed error taxonomy (`errors.As`), typed responses, and `SystemOneAs[T]` for custom struct decoding.
- **Built-in CLI**: `cmd/typesafe` for pipelines, shell scripts, and terminal inspection.

Requires **Go 1.26 or newer**.

### English Quickstart

```bash
export TYPESAFE_API_KEY="ts_live_..."
go get github.com/guchengod/typesafe-sdk-go
```

```go
package main

import (
	"context"
	"fmt"
	"log"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	response, err := client.SystemOne(
		context.Background(),
		map[string]any{
			"channel":  "email",
			"document": "I was charged twice on invoice #4912. Please refund immediately.",
		},
		typesafe.Questions{
			"billing": typesafe.NewNoul("Is this message about billing or payments?"),
			"category": typesafe.NewChoice(
				map[string]any{
					"billing":   "Charges, refunds, or invoices",
					"technical": "Bugs, crashes, or API problems",
					"other":     nil,
				},
				typesafe.WithInstructions("What department should handle this ticket?"),
			),
			"urgency": typesafe.NewScore(
				[]any{"Can wait until next week", "Needs attention within 48 hours", "Requires same-day attention"},
				typesafe.WithInstructions("How urgently does this customer require a response?"),
			),
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("billing p(true) = %.2f\n", response.Nouls()["billing"].Noul)
	fmt.Printf("category        = %s (confidence %.2f)\n", response.Choices()["category"].Choice, response.Choices()["category"].Confidence)
	fmt.Printf("urgency score   = %.2f of 2\n", response.Scores()["urgency"].Score)
}
```

### English Agent Prompt

Add the [AI Agent 专用系统提示词](#-ai-agent-专用系统提示词) block above to your Cursor / Copilot / Claude Code rules file (`.cursorrules` or `CLAUDE.md`). It provides full specification details, constructors, and idiomatic Go usage patterns.

### License

MIT License. See [LICENSE](LICENSE) for details.
