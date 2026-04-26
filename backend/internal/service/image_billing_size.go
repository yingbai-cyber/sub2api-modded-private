package service

import (
	"sort"
	"strconv"
	"strings"
)

const (
	ImageBillingSize1K = "1K"
	ImageBillingSize2K = "2K"
	ImageBillingSize4K = "4K"

	ImageSizeSourceOutput  = "output"
	ImageSizeSourceInput   = "input"
	ImageSizeSourceDefault = "default"
	ImageSizeSourceLegacy  = "legacy"
)

type ImageBillingSizeResolution struct {
	BillingSize string
	InputSize   string
	OutputSize  string
	Source      string
	Breakdown   map[string]int
}

func ClassifyImageBillingTier(size string) (string, bool) {
	trimmed := strings.TrimSpace(size)
	normalized := strings.ToLower(strings.ReplaceAll(trimmed, "×", "x"))
	switch normalized {
	case "", "auto":
		return "", false
	case "1k":
		return ImageBillingSize1K, true
	case "2k":
		return ImageBillingSize2K, true
	case "4k":
		return ImageBillingSize4K, true
	case "1024x1024":
		return ImageBillingSize1K, true
	case "1536x1024", "1024x1536", "2048x2048", "2048x1152", "1152x2048", "2560x1440", "1440x2560":
		return ImageBillingSize2K, true
	case "3840x2160", "2160x3840":
		return ImageBillingSize4K, true
	}

	width, height, ok := parseImageBillingDimensions(trimmed)
	if !ok {
		return "", false
	}
	if !isValidImageBillingDimensions(width, height) && !isOversizeImageBillingDimension(width, height) {
		return ImageBillingSize2K, true
	}
	if imageBillingPixelsExceed(width, height, 2560*1440) {
		return ImageBillingSize4K, true
	}
	return ImageBillingSize2K, true
}

func NormalizeImageBillingTierOrDefault(size string) string {
	if tier, ok := ClassifyImageBillingTier(size); ok {
		return tier
	}
	return ImageBillingSize2K
}

func ResolveImageBillingSize(inputSize string, outputSizes []string) ImageBillingSizeResolution {
	inputSize = strings.TrimSpace(inputSize)
	outputSizes = compactTrimmedStrings(outputSizes)

	breakdown := map[string]int{}
	outputSize := firstDisplayImageOutputSize(outputSizes)
	outputTier := ""
	for _, output := range outputSizes {
		tier, ok := ClassifyImageBillingTier(output)
		if !ok {
			continue
		}
		breakdown[tier]++
		if imageTierRank(tier) > imageTierRank(outputTier) {
			outputTier = tier
		}
	}
	if outputTier != "" {
		return ImageBillingSizeResolution{
			BillingSize: outputTier,
			InputSize:   inputSize,
			OutputSize:  outputSize,
			Source:      ImageSizeSourceOutput,
			Breakdown:   normalizeImageSizeBreakdown(breakdown),
		}
	}

	if tier, ok := ClassifyImageBillingTier(inputSize); ok {
		return ImageBillingSizeResolution{
			BillingSize: tier,
			InputSize:   inputSize,
			OutputSize:  outputSize,
			Source:      ImageSizeSourceInput,
		}
	}

	return ImageBillingSizeResolution{
		BillingSize: ImageBillingSize2K,
		InputSize:   inputSize,
		OutputSize:  outputSize,
		Source:      ImageSizeSourceDefault,
	}
}

func ApplyOpenAIImageBillingResolution(result *OpenAIForwardResult) {
	if result == nil || result.ImageCount <= 0 {
		return
	}
	inputSize := strings.TrimSpace(result.ImageInputSize)
	if inputSize == "" && strings.TrimSpace(result.ImageSize) != ImageBillingSize2K {
		inputSize = strings.TrimSpace(result.ImageSize)
	}
	outputSizes := result.ImageOutputSizes
	if len(outputSizes) == 0 && strings.TrimSpace(result.ImageOutputSize) != "" {
		outputSizes = []string{result.ImageOutputSize}
	}
	resolved := ResolveImageBillingSize(inputSize, outputSizes)
	applyImageBillingResolution(
		&result.ImageSize,
		&result.ImageInputSize,
		&result.ImageOutputSize,
		&result.ImageSizeSource,
		&result.ImageSizeBreakdown,
		resolved,
	)
}

func ApplyForwardImageBillingResolution(result *ForwardResult) {
	if result == nil || result.ImageCount <= 0 {
		return
	}
	inputSize := strings.TrimSpace(result.ImageInputSize)
	if inputSize == "" && strings.TrimSpace(result.ImageSize) != ImageBillingSize2K {
		inputSize = strings.TrimSpace(result.ImageSize)
	}
	outputSizes := result.ImageOutputSizes
	if len(outputSizes) == 0 && strings.TrimSpace(result.ImageOutputSize) != "" {
		outputSizes = []string{result.ImageOutputSize}
	}
	resolved := ResolveImageBillingSize(inputSize, outputSizes)
	applyImageBillingResolution(
		&result.ImageSize,
		&result.ImageInputSize,
		&result.ImageOutputSize,
		&result.ImageSizeSource,
		&result.ImageSizeBreakdown,
		resolved,
	)
}

func applyImageBillingResolution(
	billingSize *string,
	inputSize *string,
	outputSize *string,
	source *string,
	breakdown *map[string]int,
	resolved ImageBillingSizeResolution,
) {
	*billingSize = resolved.BillingSize
	*inputSize = resolved.InputSize
	*outputSize = resolved.OutputSize
	*source = resolved.Source
	*breakdown = resolved.Breakdown
}

func parseImageBillingDimensions(size string) (int64, int64, bool) {
	parts := strings.Split(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(size), "×", "x")), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	height, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func isValidImageBillingDimensions(width, height int64) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if width%16 != 0 || height%16 != 0 {
		return false
	}
	if width > 3840 || height > 3840 {
		return false
	}

	longSide, shortSide := width, height
	if height > width {
		longSide, shortSide = height, width
	}
	if longSide > shortSide*3 {
		return false
	}

	pixels := width * height
	return pixels >= 655360 && pixels <= 8294400
}

func isOversizeImageBillingDimension(width, height int64) bool {
	longSide, shortSide := width, height
	if height > width {
		longSide, shortSide = height, width
	}
	return longSide >= 3840 && shortSide <= 1024
}

func imageBillingPixelsExceed(width, height int64, threshold int64) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	return width > threshold/height
}

func compactTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func firstDisplayImageOutputSize(outputSizes []string) string {
	for _, output := range outputSizes {
		if trimmed := strings.TrimSpace(output); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func imageTierRank(tier string) int {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case ImageBillingSize1K:
		return 1
	case ImageBillingSize2K:
		return 2
	case ImageBillingSize4K:
		return 3
	default:
		return 0
	}
}

func normalizeImageSizeBreakdown(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for _, tier := range []string{ImageBillingSize1K, ImageBillingSize2K, ImageBillingSize4K} {
		if count := in[tier]; count > 0 {
			out[tier] = count
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func SortedImageBillingBreakdownKeys(breakdown map[string]int) []string {
	keys := make([]string, 0, len(breakdown))
	for key := range breakdown {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := imageTierRank(keys[i]), imageTierRank(keys[j])
		if left == right {
			return keys[i] < keys[j]
		}
		return left < right
	})
	return keys
}
