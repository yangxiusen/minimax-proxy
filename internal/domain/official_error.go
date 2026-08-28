package domain

var officialErrorMessages = map[string]string{
	"1000":  "未知错误，稍后重试",
	"1001":  "请求超时，可以重试",
	"1002":  "请求频率超过限制，降低并发并退避重试",
	"1004":  "未授权、Key 与 Group 不匹配，或登录状态失效",
	"1008":  "余额不足，需要充值或检查套餐",
	"1024":  "MiniMax 内部错误，稍后重试",
	"1026":  "输入内容触发安全审核，需要修改提示词、图片或其他输入",
	"1027":  "模型生成内容触发安全审核，需要修改输入后重新生成",
	"1033":  "系统或数据库异常，稍后重试",
	"1039":  "Token 超限，缩短上下文、输入或最大输出长度",
	"1041":  "连接数超过限制，降低并发；持续出现需联系官方",
	"1042":  "不可见字符或非法字符比例过高，清理文本格式",
	"1043":  "语音识别相似度校验失败，检查 file_id 和 text_validation",
	"1044":  "克隆音频与提示文本相似度校验失败",
	"2013":  "请求参数无效，或字形定义格式错误",
	"20132": "样本、file_id 或 voice_id 无效",
	"2037":  "克隆音频过短或过长，需要调整音频时长",
	"2039":  "自定义 voice_id 重复，需要更换",
	"2042":  "无权使用该 voice_id，检查所属账号",
	"2045":  "请求量增长或下降过快，应平滑控制并发",
	"2048":  "提示音频超过限制，应控制在 8 秒以内",
	"2049":  "API Key 无效、错误、过期或已被停用",
	"2056":  "套餐用量超过限制，等待下一个 5 小时窗口释放",
}

func LocalizeOfficialError(fallbackCode, fallbackMessage string, feedback *UpstreamFeedback) (string, string) {
	if feedback == nil {
		return fallbackCode, fallbackMessage
	}
	message, ok := officialErrorMessages[feedback.Code]
	if !ok {
		return fallbackCode, fallbackMessage
	}
	return feedback.Code, message
}
