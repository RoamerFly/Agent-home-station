package companion

import "testing"

func TestBackgroundResultDoesNotForceRepeatingOldMediaRequest(t *testing.T) {
	turns := []map[string]string{{"role": "user", "content": `{"kind":"current_message","background_result":true,"task_continuation":true,"text":"诊断结果 original_request: 给我下载雨爱发语音"}`}}
	need := resourceNeed(turns)
	if need.Required || need.Fresh {
		t.Fatal("process result turned into another mandatory song request", need)
	}
}
