package service

import (
	"context"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	openAITTFTTraceHeader = "X-Sub2API-TTFT-Trace"
	openAITTFTTraceGinKey = "openai_ttft_trace"
)

type openAITTFTTraceContextKey struct{}

// OpenAITTFTTrace is request-scoped state for OpenAI TTFT trace logging.
type OpenAITTFTTrace struct {
	mu sync.Mutex

	startedAt time.Time
	finished  bool

	forced          bool
	sampled         bool
	slowThresholdMS int

	model         string
	upstreamModel string
	accountID     int64
	stream        bool

	upstreamStatus int
	connReused     bool
	hasConnReused  bool

	stages    map[string]int64
	finalized bool
}

// InitializeOpenAITTFTTrace starts and binds trace state when the request should be traced.
func InitializeOpenAITTFTTrace(c *gin.Context, cfg *config.Config, model string, stream bool) *OpenAITTFTTrace {
	return MaybeStartOpenAITTFTTrace(c, cfg, model, stream)
}

// MaybeStartOpenAITTFTTrace starts request-scoped TTFT tracing when forced, sampled, or slow tracing is enabled.
func MaybeStartOpenAITTFTTrace(c *gin.Context, cfg *config.Config, model string, stream bool) *OpenAITTFTTrace {
	if c == nil {
		return nil
	}
	if existing := getOpenAITTFTTraceFromGin(c); existing != nil {
		existing.SetModel(model)
		existing.SetStream(stream)
		return existing
	}

	traceCfg := config.OpenAITTFTTraceConfig{}
	if cfg != nil {
		traceCfg = cfg.OpenAITTFTTrace
	}

	forced := openAITTFTTraceForced(c.GetHeader(openAITTFTTraceHeader))
	sampleRate := normalizedOpenAITTFTTraceSampleRate(traceCfg.SampleRate)
	sampled := traceCfg.Enabled && sampleRate > 0 && (sampleRate >= 1 || rand.Float64() < sampleRate)
	slowEnabled := traceCfg.Enabled && traceCfg.SlowMS > 0
	if !forced && !sampled && !slowEnabled {
		return nil
	}

	trace := &OpenAITTFTTrace{
		startedAt:       time.Now(),
		forced:          forced,
		sampled:         sampled,
		slowThresholdMS: traceCfg.SlowMS,
		model:           strings.TrimSpace(model),
		stream:          stream,
		stages:          make(map[string]int64, 32),
	}
	bindOpenAITTFTTrace(c, trace)
	return trace
}

func bindOpenAITTFTTrace(c *gin.Context, trace *OpenAITTFTTrace) {
	if c == nil || trace == nil {
		return
	}
	c.Set(openAITTFTTraceGinKey, trace)
	if c.Request != nil {
		ctx := context.WithValue(c.Request.Context(), openAITTFTTraceContextKey{}, trace)
		c.Request = c.Request.WithContext(ctx)
	}
}

func openAITTFTTraceForced(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func normalizedOpenAITTFTTraceSampleRate(rate float64) float64 {
	if math.IsNaN(rate) || rate <= 0 {
		return 0
	}
	if rate >= 1 {
		return 1
	}
	return rate
}

func getOpenAITTFTTraceFromGin(c *gin.Context) *OpenAITTFTTrace {
	if c == nil {
		return nil
	}
	v, ok := c.Get(openAITTFTTraceGinKey)
	if !ok {
		return nil
	}
	trace, _ := v.(*OpenAITTFTTrace)
	return trace
}

func getOpenAITTFTTraceFromContext(ctx context.Context) *OpenAITTFTTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(openAITTFTTraceContextKey{}).(*OpenAITTFTTrace)
	return trace
}

func HasOpenAITTFTTraceContext(ctx context.Context) bool {
	return getOpenAITTFTTraceFromContext(ctx) != nil
}

// Mark records the elapsed time from trace start to this stage.
func (t *OpenAITTFTTrace) Mark(stage string) {
	if t == nil || t.startedAt.IsZero() {
		return
	}
	t.Set(stage, time.Since(t.startedAt).Milliseconds())
}

// Set records an explicit stage latency in milliseconds.
func (t *OpenAITTFTTrace) Set(stage string, ms int64) {
	stage = normalizeOpenAITTFTTraceStage(stage)
	if t == nil || stage == "" || ms < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finalized {
		return
	}
	t.stages[stage] = ms
}

func (t *OpenAITTFTTrace) setIfAbsent(stage string, ms int64) {
	stage = normalizeOpenAITTFTTraceStage(stage)
	if t == nil || stage == "" || ms < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.stages[stage]; !exists {
		t.stages[stage] = ms
	}
}

// Finish records total trace duration.
func (t *OpenAITTFTTrace) Finish() {
	if t == nil || t.startedAt.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finalized {
		return
	}
	t.finished = true
	t.stages["total_ms"] = time.Since(t.startedAt).Milliseconds()
}

func (t *OpenAITTFTTrace) SetModel(model string) {
	if t == nil {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.finalized {
		t.model = model
	}
}

func (t *OpenAITTFTTrace) SetUpstreamModel(model string) {
	if t == nil {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.finalized {
		t.upstreamModel = model
	}
}

func (t *OpenAITTFTTrace) SetAccountID(accountID int64) {
	if t == nil || accountID <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.finalized {
		t.accountID = accountID
	}
}

func (t *OpenAITTFTTrace) SetStream(stream bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.finalized {
		t.stream = stream
	}
}

func (t *OpenAITTFTTrace) SetUpstreamStatus(status int) {
	if t == nil || status <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.upstreamStatus = status
}

func (t *OpenAITTFTTrace) SetConnReused(reused bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connReused = reused
	t.hasConnReused = true
}

// MarkOpenAITTFTTrace records an elapsed stage on the gin-bound trace.
func MarkOpenAITTFTTrace(c *gin.Context, stage string) {
	if trace := getOpenAITTFTTraceFromGin(c); trace != nil {
		trace.Mark(stage)
	}
}

// SetOpenAITTFTTrace records an explicit stage latency on the gin-bound trace.
func SetOpenAITTFTTrace(c *gin.Context, stage string, ms int64) {
	if trace := getOpenAITTFTTraceFromGin(c); trace != nil {
		trace.Set(stage, ms)
	}
}

// FinishOpenAITTFTTrace records total elapsed time without emitting a log line.
func FinishOpenAITTFTTrace(c *gin.Context) {
	if trace := getOpenAITTFTTraceFromGin(c); trace != nil {
		trace.Finish()
	}
}

func SetOpenAITTFTTraceUpstreamModel(c *gin.Context, model string) {
	if trace := getOpenAITTFTTraceFromGin(c); trace != nil {
		trace.SetUpstreamModel(model)
	}
}

func SetOpenAITTFTTraceAccountID(c *gin.Context, accountID int64) {
	if trace := getOpenAITTFTTraceFromGin(c); trace != nil {
		trace.SetAccountID(accountID)
	}
}

func SetOpenAITTFTTraceUpstreamStatus(c *gin.Context, status int) {
	if trace := getOpenAITTFTTraceFromGin(c); trace != nil {
		trace.SetUpstreamStatus(status)
	}
}

// MarkOpenAITTFTTraceContext records an elapsed stage from a plain context.Context.
func MarkOpenAITTFTTraceContext(ctx context.Context, stage string) {
	if trace := getOpenAITTFTTraceFromContext(ctx); trace != nil {
		trace.Mark(stage)
	}
}

// SetOpenAITTFTTraceContext records an explicit stage latency from a plain context.Context.
func SetOpenAITTFTTraceContext(ctx context.Context, stage string, ms int64) {
	if trace := getOpenAITTFTTraceFromContext(ctx); trace != nil {
		trace.Set(stage, ms)
	}
}

func SetOpenAITTFTTraceConnReusedContext(ctx context.Context, reused bool) {
	if trace := getOpenAITTFTTraceFromContext(ctx); trace != nil {
		trace.SetConnReused(reused)
	}
}

func SetOpenAITTFTTraceUpstreamStatusContext(ctx context.Context, status int) {
	if trace := getOpenAITTFTTraceFromContext(ctx); trace != nil {
		trace.SetUpstreamStatus(status)
	}
}

func normalizeOpenAITTFTTraceStage(stage string) string {
	stage = strings.ToLower(strings.TrimSpace(stage))
	stage = strings.ReplaceAll(stage, "-", "_")
	if stage == "" || !strings.HasSuffix(stage, "_ms") {
		return ""
	}
	for _, r := range stage {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return ""
	}
	return stage
}

type openAITTFTTraceSnapshot struct {
	forced          bool
	sampled         bool
	slowThresholdMS int
	model           string
	upstreamModel   string
	accountID       int64
	stream          bool
	upstreamStatus  int
	connReused      bool
	hasConnReused   bool
	stages          map[string]int64
}

func (t *OpenAITTFTTrace) snapshot() openAITTFTTraceSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	stages := make(map[string]int64, len(t.stages))
	for k, v := range t.stages {
		stages[k] = v
	}
	return openAITTFTTraceSnapshot{
		forced:          t.forced,
		sampled:         t.sampled,
		slowThresholdMS: t.slowThresholdMS,
		model:           t.model,
		upstreamModel:   t.upstreamModel,
		accountID:       t.accountID,
		stream:          t.stream,
		upstreamStatus:  t.upstreamStatus,
		connReused:      t.connReused,
		hasConnReused:   t.hasConnReused,
		stages:          stages,
	}
}

func (t *OpenAITTFTTrace) markFinalized() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finalized {
		return false
	}
	t.finalized = true
	return true
}

// FinalizeOpenAITTFTTrace emits at most one structured openai.ttft_trace log line.
func FinalizeOpenAITTFTTrace(c *gin.Context) {
	trace := getOpenAITTFTTraceFromGin(c)
	if trace == nil {
		return
	}
	if !trace.markFinalized() {
		return
	}
	trace.finalize(c)
}

func (t *OpenAITTFTTrace) finalize(c *gin.Context) {
	// markFinalized prevents further normal Set calls, so write total directly.
	t.mu.Lock()
	if t.startedAt.IsZero() {
		t.mu.Unlock()
		return
	}
	t.finished = true
	t.stages["total_ms"] = time.Since(t.startedAt).Milliseconds()
	t.mu.Unlock()

	mergeOpenAITTFTTraceOps(c, t)
	snap := t.snapshot()
	reason, shouldLog := openAITTFTTraceReason(snap)
	if !shouldLog {
		return
	}

	requestID, path, method := openAITTFTTraceRequestFields(c)
	statusCode := openAITTFTTraceStatusCode(c)
	accountID := snap.accountID
	if accountID <= 0 {
		accountID = openAITTFTTraceContextAccountID(c)
	}
	upstreamStatus := snap.upstreamStatus
	if upstreamStatus <= 0 {
		if upstreamStatusValue, ok := openAITTFTTraceGinInt64(c, OpsUpstreamStatusCodeKey); ok {
			upstreamStatus = int(upstreamStatusValue)
		}
	}
	connReused, hasConnReused := snap.connReused, snap.hasConnReused
	if !hasConnReused {
		connReused, hasConnReused = openAITTFTTraceGinBool(c, OpsOpenAIWSConnReusedKey)
	}

	fields := make([]zap.Field, 0, 16+len(snap.stages))
	if requestID != "" {
		fields = append(fields, zap.String("request_id", requestID))
	}
	if path != "" {
		fields = append(fields, zap.String("path", path))
	}
	if method != "" {
		fields = append(fields, zap.String("method", method))
	}
	if snap.model != "" {
		fields = append(fields, zap.String("model", snap.model))
	}
	if snap.upstreamModel != "" {
		fields = append(fields, zap.String("upstream_model", snap.upstreamModel))
	}
	if accountID > 0 {
		fields = append(fields, zap.Int64("account_id", accountID))
	}
	fields = append(fields, zap.Bool("stream", snap.stream))
	if statusCode > 0 {
		fields = append(fields, zap.Int("status_code", statusCode))
	}
	if upstreamStatus > 0 {
		fields = append(fields, zap.Int("upstream_status", int(upstreamStatus)))
	}
	fields = append(fields, zap.String("trace_reason", reason))
	if hasConnReused {
		fields = append(fields, zap.Bool("conn_reused", connReused))
	}

	stageNames := make([]string, 0, len(snap.stages))
	for name := range snap.stages {
		stageNames = append(stageNames, name)
	}
	sort.Strings(stageNames)
	for _, name := range stageNames {
		fields = append(fields, zap.Int64(name, snap.stages[name]))
	}

	logger.L().Info("openai.ttft_trace", fields...)
}

func mergeOpenAITTFTTraceOps(c *gin.Context, trace *OpenAITTFTTrace) {
	if c == nil || trace == nil {
		return
	}
	if v, ok := openAITTFTTraceGinInt64(c, OpsUpstreamLatencyMsKey); ok {
		trace.setIfAbsent("ops_upstream_latency_ms", v)
	}
	if v, ok := openAITTFTTraceGinInt64(c, OpsResponseLatencyMsKey); ok {
		trace.setIfAbsent("ops_response_latency_ms", v)
	}
	if v, ok := openAITTFTTraceGinInt64(c, OpsTimeToFirstTokenMsKey); ok {
		trace.setIfAbsent("first_token_ms", v)
	}
	if v, ok := openAITTFTTraceGinInt64(c, OpsOpenAIWSQueueWaitMsKey); ok {
		trace.setIfAbsent("openai_ws_queue_wait_ms", v)
	}
	if v, ok := openAITTFTTraceGinInt64(c, OpsOpenAIWSConnPickMsKey); ok {
		trace.setIfAbsent("openai_ws_conn_pick_ms", v)
	}
	if v, ok := openAITTFTTraceGinInt64(c, OpsUpstreamStatusCodeKey); ok {
		trace.SetUpstreamStatus(int(v))
	}
	if reused, ok := openAITTFTTraceGinBool(c, OpsOpenAIWSConnReusedKey); ok {
		trace.SetConnReused(reused)
	}
}

func openAITTFTTraceReason(snap openAITTFTTraceSnapshot) (string, bool) {
	observedMS := snap.stages["total_ms"]
	if firstTokenMS, ok := snap.stages["first_token_ms"]; ok && firstTokenMS >= 0 {
		observedMS = firstTokenMS
	}
	slow := snap.slowThresholdMS > 0 && observedMS >= int64(snap.slowThresholdMS)
	switch {
	case snap.forced:
		return "forced", true
	case slow:
		return "slow", true
	case snap.sampled:
		return "sampled", true
	default:
		return "", false
	}
}

func openAITTFTTraceRequestFields(c *gin.Context) (requestID, path, method string) {
	if c == nil || c.Request == nil {
		return "", "", ""
	}
	requestID, _ = c.Request.Context().Value(ctxkey.RequestID).(string)
	if c.Request.URL != nil {
		path = strings.TrimSpace(c.Request.URL.Path)
	}
	method = strings.TrimSpace(c.Request.Method)
	return strings.TrimSpace(requestID), path, method
}

func openAITTFTTraceStatusCode(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return 0
	}
	status := c.Writer.Status()
	if status == http.StatusOK && !c.Writer.Written() {
		return status
	}
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func openAITTFTTraceContextAccountID(c *gin.Context) int64 {
	if c == nil || c.Request == nil {
		return 0
	}
	switch v := c.Request.Context().Value(ctxkey.AccountID).(type) {
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}

func openAITTFTTraceGinInt64(c *gin.Context, key string) (int64, bool) {
	if c == nil || key == "" {
		return 0, false
	}
	v, ok := c.Get(key)
	if !ok {
		return 0, false
	}
	switch value := v.(type) {
	case int64:
		return value, true
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int16:
		return int64(value), true
	case int8:
		return int64(value), true
	case uint:
		return int64(value), true
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(value), true
	case uint32:
		return int64(value), true
	case float64:
		return int64(value), true
	case float32:
		return int64(value), true
	default:
		return 0, false
	}
}

func openAITTFTTraceGinBool(c *gin.Context, key string) (bool, bool) {
	if c == nil || key == "" {
		return false, false
	}
	v, ok := c.Get(key)
	if !ok {
		return false, false
	}
	value, ok := v.(bool)
	return value, ok
}
