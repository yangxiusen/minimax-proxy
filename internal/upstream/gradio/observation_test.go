package gradio

import "testing"

func TestParseObservationParsesStatusQueueAndResourceString(t *testing.T) {
	got := ParseObservation([]any{
		nil,
		"任务运行中",
		"等待队列数量: 2",
		"CPU: 12.5% | 内存: 34% | GPU: 56% | 显存: 78.5%",
		"普通运行日志",
	})

	if got.Status != ObservationRunning || got.PrivateQueue == nil || *got.PrivateQueue != 2 {
		t.Fatalf("status/queue = %q/%v", got.Status, got.PrivateQueue)
	}
	assertPercent(t, "CPU", got.CPUPercent, 12.5)
	assertPercent(t, "memory", got.MemoryPercent, 34)
	assertPercent(t, "GPU", got.GPUPercent, 56)
	assertPercent(t, "VRAM", got.VRAMPercent, 78.5)
}

func TestParseObservationParsesResourceUsageWithTotals(t *testing.T) {
	got := ParseObservation([]any{
		nil,
		"任务执行中",
		"当前队列: 1 个任务",
		"CPU利用率: 9.1%\n内存占用: 88.5/93.6 GB (94.5%)\n显存占用: 11.1/16.0 GB (69.6%)\n显卡利用率: 97%",
	})

	assertPercent(t, "CPU", got.CPUPercent, 9.1)
	assertPercent(t, "memory", got.MemoryPercent, 94.5)
	assertPercent(t, "GPU", got.GPUPercent, 97)
	assertPercent(t, "VRAM", got.VRAMPercent, 69.6)
}

func TestParseObservationSupportsIdleAndResourceMaps(t *testing.T) {
	got := ParseObservation([]any{
		nil,
		"Idle",
		3.0,
		map[string]any{
			"cpu_usage": "11%",
			"memory":    22.5,
			"gpu":       map[string]any{"utilization": "33 %", "memory": "44%"},
		},
	})

	if got.Status != ObservationIdle || got.PrivateQueue == nil || *got.PrivateQueue != 3 {
		t.Fatalf("status/queue = %q/%v", got.Status, got.PrivateQueue)
	}
	assertPercent(t, "CPU", got.CPUPercent, 11)
	assertPercent(t, "memory", got.MemoryPercent, 22.5)
	assertPercent(t, "GPU", got.GPUPercent, 33)
	assertPercent(t, "VRAM", got.VRAMPercent, 44)
}

func TestParseObservationTreatsCompletedAsIdle(t *testing.T) {
	got := ParseObservation([]any{nil, "已完成"})
	if got.Status != ObservationIdle {
		t.Fatalf("Status = %q", got.Status)
	}
}

func TestParseObservationTreatsNoTaskAsIdle(t *testing.T) {
	got := ParseObservation([]any{nil, "无任务\n等待提交..."})
	if got.Status != ObservationIdle {
		t.Fatalf("Status = %q", got.Status)
	}
}

func TestParseObservationRejectsAmbiguousQueueAndInvalidPercents(t *testing.T) {
	got := ParseObservation([]any{nil, 123, "3", map[string]any{
		"cpu":    -1,
		"memory": 101,
		"gpu":    "NaN%",
		"vram":   "unknown",
	}})

	if got.Status != ObservationUnknown || got.PrivateQueue != nil {
		t.Fatalf("status/queue = %q/%v", got.Status, got.PrivateQueue)
	}
	if got.CPUPercent != nil || got.MemoryPercent != nil || got.GPUPercent != nil || got.VRAMPercent != nil {
		t.Fatalf("invalid resource values must be nil: %+v", got)
	}
}

func TestParseObservationClassifiesFailureWithoutKeepingRawLog(t *testing.T) {
	got := ParseObservation([]any{nil, "未知状态", nil, nil, "Bearer secret-token: generation failed at http://10.0.0.1"})
	if got.Status != ObservationFailed {
		t.Fatalf("Status = %q", got.Status)
	}
}

func TestParseObservationDoesNotPanicOnMissingOrWrongTypes(t *testing.T) {
	inputs := [][]any{nil, {}, {nil}, {nil, []any{"running"}, struct{}{}, true, map[string]any{"error": struct{}{}}}}
	for index, input := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("input %d panicked: %v", index, recovered)
				}
			}()
			_ = ParseObservation(input)
		}()
	}
}

func assertPercent(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}
