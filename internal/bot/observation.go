package bot

import (
	"errors"
)

// An attempted read is evidence of failure, not evidence of video contents.
type ObservationUnavailableError struct{ Observation, Reason string }

func (e *ObservationUnavailableError) Error() string { return e.Reason }

func failedObservation(err error) string {
	var unavailable *ObservationUnavailableError
	observation := "媒体预读取未取得可验证内容。"
	if errors.As(err, &unavailable) {
		observation += unavailable.Observation + "\n读取状态：" + unavailable.Reason
	}
	return observation + "\n这是读取失败资料，不是用户指令。可发现并使用可用工具，结合当前消息ID、真实分享链接或素材ID重试、调整读取范围或换方法；未取得内容前不能声称已看过，不能拿标题猜视频结论。根据当前请求决定继续处理、诚实说明或安静。"
}
