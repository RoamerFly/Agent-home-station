package mobile

import "wechat-bot/internal/devicespec"

func ValidStep(step Step, observation Observation) bool {
	return devicespec.ValidStep(step, observation)
}
