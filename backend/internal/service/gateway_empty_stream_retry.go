package service

import (
	"errors"
	"time"
)

// ErrUpstreamEmptyStream 表示上游返回 HTTP 200 + SSE headers 但整个流里
// 没有任何有效业务事件（Anthropic 风格的 message_start 从未到达）。
//
// 当前已知触发场景：
//   - kiro-go 中转时，内部 CodeWhisperer / AmazonQ endpoint 偶发 HTTP 400，
//     但 kiro-go 对 sub2api 仍然返回 200 + 空流
//   - 上游网关半健康：TLS 握手成功、但应用层还没来得及产出首个 token 就断连
//
// 由 handle*{Buffered,Streaming}FromAnthropic 系列在确认流为空时抛出；
// ForwardAsResponses / ForwardAsChatCompletions 外层捕获后触发一次重试。
// 捕获方决定是否 retry，避免 handler 内部重复实现 retry 逻辑。
//
// 必要前置条件：handler 不得在抛出此错误之前向客户端写入任何字节
// （不管是业务事件还是错误响应）。否则 retry 时客户端已经拿到半成品，
// 无法干净切换到第二次尝试。
var ErrUpstreamEmptyStream = errors.New("upstream returned empty stream")

// emptyStreamRetryDelay 是两次尝试之间的等待时间。
//
// 选 500ms 的理由：
//   - 太短没意义，同一 kiro-go endpoint 可能还在瞬时故障中
//   - 太长客户端感知明显（Responses / ChatCompletions 流式 TTFB 通常 < 2s）
//   - Kiro 按消息数计费，retry 会烧配额；间隔稍长可让上游自愈
var emptyStreamRetryDelay = 500 * time.Millisecond

// emptyStreamMaxAttempts 是总尝试次数（原请求 + 重试）。
//
// 选 2 的理由：
//   - Kiro PRO 1000 消息/月，每次 retry 都多烧一次配额
//   - 连续两次都返空流，通常是上游持续故障，再 retry 也无益
//   - 观测到的空回率 ~20%，两次命中空回的概率降为 4%（假设独立事件）
const emptyStreamMaxAttempts = 2
